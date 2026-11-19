package workbench

import (
	"bufio"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/managedproxy"
)

//go:embed account-network.sh
var setupAccountNetwork string

//go:embed account-relay.py
var accountRelay string

type accountNetworkLink struct {
	pid    int
	node   string
	done   chan struct{}
	cancel context.CancelFunc
}
type accountNetworks struct {
	mu    sync.Mutex
	links map[string]*accountNetworkLink
}

var errUnsupportedAccountNetwork = errors.New("当前环境不支持这个账号出口")

// Guest profile IDs can outlive registry IDs after recovery. The default
// profile can represent different subscriptions, so match its known subject.
func (s *Server) boundGuestAccount(id, subject string) (*claudeAccount, error) {
	// Recovery can already hold the account mutex. The registry is replaced atomically.
	list, err := s.loadClaudeAccounts()
	if err != nil {
		return nil, err
	}
	if id != "" && id == list.PendingLogin && list.PendingProxyNode != "" {
		if !s.accountDesktopsSupported() {
			return nil, errUnsupportedAccountNetwork
		}
		return &claudeAccount{ID: id, ProxyNodeID: list.PendingProxyNode}, nil
	}
	for i := range list.Accounts {
		a := &list.Accounts[i]
		if a.guestID() == id && a.ProxyNodeID != "" && (id != "default" || subject == "" || claudeIdentityHash(a.Email, a.Organization) == subject) {
			if !s.accountDesktopsSupported() {
				return nil, errUnsupportedAccountNetwork
			}
			return a, nil
		}
	}
	return nil, nil
}

func (s *Server) ensureAccountNetwork(ctx context.Context, id, node string) error {
	if !validClaudeID(id) || !validClaudeID(node) || !s.accountDesktopsSupported() {
		return errUnsupportedAccountNetwork
	}
	m := &s.accountNetworks
	m.mu.Lock()
	defer m.mu.Unlock()
	gate, err := cage.OpenGateClient(s.instanceDir())
	if err != nil {
		return err
	}
	target, err := gate.PrepareAccountNode(ctx, node)
	if err != nil {
		return errors.New("账号节点尚未就绪，请更新 Cage 环境并应用代理配置")
	}
	if old := m.links[id]; old != nil {
		select {
		case <-old.done:
		default:
			if old.node == node {
				return nil
			}
			old.cancel()
			select {
			case <-old.done:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	ns := "net-" + id
	remote := "/tmp/socket-" + randomToken()[:24]
	cmdline := "set -eu; sudo /bin/bash -c " + shellescape.Quote(setupAccountNetwork) + " -- " + id +
		"; exec sudo ip netns exec " + ns + " setpriv --reuid=\"$(id -u)\" --regid=\"$(id -g)\" --init-groups python3 -I -c " + shellescape.Quote(accountRelay) + " " + remote + " /run/account-routes/" + id + "/browser.sock"
	life, cancel := context.WithCancel(s.lifetimeContext())
	cmd := s.sshCommandWithForwarding(life, "-T", "-o", "ExitOnForwardFailure=yes", "-R", remote+":"+target, s.sshTarget(), "--", "/bin/bash -c "+shellescape.Quote(cmdline))
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		cancel()
		return err
	}
	if err = cmd.Start(); err != nil {
		cancel()
		in.Close()
		return err
	}
	link := &accountNetworkLink{node: node, done: make(chan struct{}), cancel: cancel}
	ready := make(chan int, 1)
	go func() {
		reader := bufio.NewReader(out)
		line, _ := reader.ReadString('\n')
		prefix, ok := strings.CutPrefix(strings.TrimSpace(line), "ready ")
		pid := 0
		if ok {
			pid, _ = strconv.Atoi(prefix)
		}
		ready <- pid
		io.Copy(io.Discard, reader)
		cmd.Wait()
		in.Close()
		cancel()
		close(link.done)
		m.mu.Lock()
		if m.links[id] == link {
			delete(m.links, id)
		}
		m.mu.Unlock()
	}()
	timer := time.NewTimer(25 * time.Second)
	defer timer.Stop()
	select {
	case pid := <-ready:
		if pid > 0 {
			link.pid = pid
			if m.links == nil {
				m.links = map[string]*accountNetworkLink{}
			}
			m.links[id] = link
			return nil
		}
	case <-ctx.Done():
	case <-timer.C:
	}
	cancel()
	in.Close()
	return errors.New("账号专属网络启动失败，已阻止回落到环境出口")
}

func (s *Server) accountForRoute(id string) (claudeAccount, error) {
	if id == "" {
		return claudeAccount{}, nil
	}
	s.claudeAccounts.mu.Lock()
	defer s.claudeAccounts.mu.Unlock()
	list, err := s.loadClaudeAccounts()
	if err != nil {
		return claudeAccount{}, err
	}
	a := list.find(id)
	if a == nil {
		if list.PendingLogin != id {
			return claudeAccount{}, errors.New("账号记录已变化，请重新选择账号")
		}
		return claudeAccount{ID: id, ProxyNodeID: list.PendingProxyNode}, nil
	}
	return *a, nil
}

// Namespace entry happens outside the interactive shell and the Claude runner,
// preserving PTYs, supervision and the official CLI's complete process tree.
func (s *Server) routeAccountCommand(ctx context.Context, id, command string) (string, error) {
	a, err := s.accountForRoute(id)
	if err != nil {
		return "", err
	}
	if a.Source != "" {
		command = "export CAGE_LOGIN_OWNER=" + a.ID + "; " + command
	}
	if a.ProxyNodeID == "" {
		return command, nil
	}
	return s.routeSelected(ctx, a, command)
}

func (s *Server) routeSelected(ctx context.Context, a claudeAccount, command string) (string, error) {
	if err := s.ensureAccountNetwork(ctx, a.ID, a.ProxyNodeID); err != nil {
		return "", err
	}
	opener := "/usr/local/lib/account-routes/" + a.ID + "/open"
	return wrapAccountNamespace("ip netns exec net-"+a.ID+" unshare -m", "export CAGE_ACCOUNT_BROWSER="+opener+" BROWSER="+opener+"; "+command), nil
}

// No shell supplied by the caller runs before setpriv drops root privileges.
func wrapAccountNamespace(namespace, command string) string {
	enter := `set -eu
mount --make-rprivate /
for endpoint in /run/systemd/resolve/io.systemd.Resolve /run/nscd/socket /run/dbus/system_bus_socket; do
  if [ -S "$endpoint" ]; then mount --bind /dev/null "$endpoint"; fi
done
exec setpriv --reuid="$SUDO_UID" --regid="$SUDO_GID" --init-groups /bin/bash -c 'script=$ROUTE_COMMAND; unset ROUTE_COMMAND SUDO_COMMAND SUDO_UID SUDO_GID SUDO_USER; exec /bin/bash -c "$script"'`
	return "exec env ROUTE_COMMAND=" + shellescape.Quote(command) + " sudo --preserve-env=ROUTE_COMMAND,HOME,USER,LOGNAME,TERM " + namespace + " /bin/bash -c " + shellescape.Quote(enter)
}

func (s *Server) setLoginDraftNode(id, node string) error {
	m := &s.claudeAccounts
	m.mu.Lock()
	defer m.mu.Unlock()
	list, err := s.loadClaudeAccounts()
	if err != nil {
		return err
	}
	if id == "" || list.PendingLogin != id {
		return errors.New("新增账号授权已结束，请刷新")
	}
	if node == list.PendingProxyNode {
		return nil
	}
	if s.claudeLoginActive(id) {
		return errors.New("已有授权正在进行，请先完成或取消")
	}
	if node != "" {
		store, err := managedproxy.Load(s.profile)
		if err != nil {
			return err
		}
		if _, ok := store.Find(node); !validClaudeID(node) || !ok {
			return errors.New("代理节点已移除，请刷新")
		}
	}
	list.PendingProxyNode = node
	return s.saveClaudeAccounts(list)
}

// The caller holds the operation and account locks, also used by terminal admission.
func (s *Server) setAccountNode(list *claudeAccountList, in claudeAccountInput) error {
	a := list.find(in.ID)
	if a == nil || in.Action != "update" || !s.accountDesktopsSupported() {
		return errors.New("当前环境不支持账号出口")
	}
	node := *in.ProxyNodeID
	if node != "" {
		store, err := managedproxy.Load(s.profile)
		if err != nil {
			return err
		}
		if _, ok := store.Find(node); !ok || !validClaudeID(node) {
			return errors.New("代理节点已移除，请刷新账号列表")
		}
	}
	if node == a.ProxyNodeID && !a.DesktopDedicatedNetwork {
		return nil
	}
	for _, t := range s.terminals.snapshot() {
		if t["claude_account_id"] == a.ID && t["closed"] == false {
			return errors.New("请先关闭该账号的终端，再切换出口")
		}
	}
	if s.mobileCLI.activeAccounts()[a.ID] > 0 {
		return errors.New("请等待该账号的手机任务结束后再切换出口")
	}
	if s.vmRunning() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		id := a.DesktopInstanceID
		if id == "" {
			id = a.ID
		} // Directories created by earlier workbenches.
		unit := "desktop-" + id + ".service"
		if id == "default" {
			unit = "cage-claude-desktop.service"
		}
		state, err := s.guest(ctx, fmt.Sprintf("systemctl show --property=ActiveState --value %s", unit))
		if err != nil {
			return errors.New("无法确认账号 Desktop 状态，暂未更改出口")
		}
		switch strings.TrimSpace(string(state)) {
		case "inactive", "failed":
		default:
			return errors.New("请先关闭该账号的 Desktop，再切换出口")
		}
		if a.ProxyNodeID != "" {
			m := &s.accountNetworks
			m.mu.Lock()
			relayPID := 0
			if link := m.links[a.ID]; link != nil {
				select {
				case <-link.done:
				default:
					relayPID = link.pid
				}
			}
			m.mu.Unlock()
			ns := "net-" + a.ID
			pids, err := s.guest(ctx, "if test -e /run/netns/"+ns+"; then sudo ip netns pids "+ns+"; fi")
			if err != nil {
				return errors.New("无法确认账号进程状态，暂未更改出口")
			}
			for _, pid := range strings.Fields(string(pids)) {
				if pid != strconv.Itoa(relayPID) {
					return errors.New("该账号仍有后台进程，请结束后再切换出口")
				}
			}
		}
	}
	a.ProxyNodeID = node
	a.DesktopDedicatedNetwork = false
	return nil
}
