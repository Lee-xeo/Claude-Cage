package workbench

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/diagnostics"
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

var (
	errUnsupportedAccountNetwork = errors.New("当前环境不支持这个账号出口")
	errAccountRouteFailed        = errors.New("账号专属网络启动失败，已阻止回落到环境出口")
)

type accountRouteError struct {
	message string
	cause   error
}

func (e *accountRouteError) Error() string { return e.message }
func (e *accountRouteError) Unwrap() error { return e.cause }
func (e *accountRouteError) Is(target error) bool {
	return target == errAccountRouteFailed
}

type limitedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if rem := b.max - b.buf.Len(); rem > 0 {
		if len(p) > rem {
			b.buf.Write(p[:rem])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// accountNetworkBridge bridges an authenticated loopback TCP listener to a
// local Unix domain socket on Windows, where Win32-OpenSSH cannot dial local
// AF_UNIX sockets in -R reverse forwards. Every connection must present the
// per-link bearer token in Proxy-Authorization before any bytes reach target.
type accountNetworkBridge struct {
	ln    net.Listener
	token string
	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

func startAccountNetworkBridge(target string) (*accountNetworkBridge, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	b := &accountNetworkBridge{
		ln:    ln,
		token: randomToken(),
		conns: map[net.Conn]struct{}{},
	}
	go b.serve(target)
	return b, nil
}

func (b *accountNetworkBridge) addr() string { return b.ln.Addr().String() }

func (b *accountNetworkBridge) close() {
	_ = b.ln.Close()
	b.mu.Lock()
	for c := range b.conns {
		_ = c.Close()
	}
	b.mu.Unlock()
}

func (b *accountNetworkBridge) track(c net.Conn) {
	b.mu.Lock()
	b.conns[c] = struct{}{}
	b.mu.Unlock()
}

func (b *accountNetworkBridge) untrack(c net.Conn) {
	b.mu.Lock()
	delete(b.conns, c)
	b.mu.Unlock()
}

func (b *accountNetworkBridge) serve(target string) {
	for {
		client, err := b.ln.Accept()
		if err != nil {
			return
		}
		b.track(client)
		go func(c net.Conn) {
			defer func() {
				b.untrack(c)
				_ = c.Close()
			}()
			b.handle(c, target)
		}(client)
	}
}

func (b *accountNetworkBridge) handle(client net.Conn, target string) {
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReaderSize(client, 8192)
	var cleaned bytes.Buffer
	total := 0
	authorized := false
	wantHeader := "Bearer " + b.token
	for i := 0; ; i++ {
		line, err := reader.ReadSlice('\n')
		if err != nil {
			return
		}
		total += len(line)
		if total > 4096 {
			return
		}
		trimmed := strings.TrimRight(string(line), "\r\n")
		if trimmed == "" {
			cleaned.Write(line)
			break
		}
		if i > 0 {
			if colon := strings.IndexByte(trimmed, ':'); colon > 0 && strings.EqualFold(strings.TrimSpace(trimmed[:colon]), "Proxy-Authorization") {
				got := strings.TrimSpace(trimmed[colon+1:])
				if subtle.ConstantTimeCompare([]byte(got), []byte(wantHeader)) == 1 {
					authorized = true
				}
				continue
			}
		}
		cleaned.Write(line)
	}
	if !authorized {
		_, _ = client.Write([]byte("HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n"))
		return
	}
	_ = client.SetReadDeadline(time.Time{})
	upstream, err := net.DialTimeout("unix", target, 5*time.Second)
	if err != nil {
		_, _ = client.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nConnection: close\r\n\r\n"))
		return
	}
	b.track(upstream)
	defer func() {
		b.untrack(upstream)
		_ = upstream.Close()
	}()
	if _, err := upstream.Write(cleaned.Bytes()); err != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(upstream, reader)
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	_, _ = io.Copy(client, upstream)
	if cw, ok := client.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
	<-done
}

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
	if err := s.validateAccountNode(node); err != nil {
		return err
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
		diagnostics.Log("warn", "account-network", fmt.Sprintf("prepare node failed err=%v", err))
		return &accountRouteError{
			message: "账号节点尚未就绪，请更新 Cage 环境并应用代理配置",
			cause:   err,
		}
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
	forwardTarget := target
	relayFlags := ""
	var bridge *accountNetworkBridge
	if runtime.GOOS == "windows" {
		bridge, err = startAccountNetworkBridge(target)
		if err != nil {
			diagnostics.Log("warn", "account-network", fmt.Sprintf("windows bridge listen failed err=%v", err))
			return &accountRouteError{
				message: "账号专属网络本地中继启动失败，已阻止回落到环境出口",
				cause:   err,
			}
		}
		forwardTarget = bridge.addr()
		relayFlags = " --stdin-auth"
	}
	cmdline := "set -eu; sudo /bin/bash -c " + shellescape.Quote(setupAccountNetwork) + " -- " + id +
		"; exec sudo ip netns exec " + ns + " setpriv --reuid=\"$(id -u)\" --regid=\"$(id -g)\" --init-groups python3 -I -c " + shellescape.Quote(accountRelay) + " " + remote + " /run/account-routes/" + id + "/browser.sock" + relayFlags
	life, cancel := context.WithCancel(s.lifetimeContext())
	cmd := s.sshCommandWithForwarding(life, "-T", "-o", "ExitOnForwardFailure=yes", "-R", remote+":"+forwardTarget, s.sshTarget(), "--", "/bin/bash -c "+shellescape.Quote(cmdline))
	stderr := &limitedBuffer{max: 2048}
	cmd.Stderr = stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		if bridge != nil {
			bridge.close()
		}
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		cancel()
		if bridge != nil {
			bridge.close()
		}
		return err
	}
	startedAt := time.Now()
	if err = cmd.Start(); err != nil {
		cancel()
		in.Close()
		if bridge != nil {
			bridge.close()
		}
		diagnostics.Log("warn", "account-network", fmt.Sprintf("ssh start failed err=%v", err))
		return &accountRouteError{
			message: "账号专属网络启动失败，已阻止回落到环境出口",
			cause:   err,
		}
	}
	if bridge != nil {
		if _, writeErr := io.WriteString(in, bridge.token+"\n"); writeErr != nil {
			cancel()
			in.Close()
			bridge.close()
			_ = cmd.Wait()
			diagnostics.Log("warn", "account-network", fmt.Sprintf("relay auth write failed err=%v", writeErr))
			return &accountRouteError{
				message: "账号专属网络启动失败，已阻止回落到环境出口",
				cause:   writeErr,
			}
		}
	}
	link := &accountNetworkLink{node: node, done: make(chan struct{}), cancel: cancel}
	type readySignal struct {
		pid     int
		readErr error
	}
	ready := make(chan readySignal, 1)
	waitDone := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(out)
		line, readErr := reader.ReadString('\n')
		prefix, ok := strings.CutPrefix(strings.TrimSpace(line), "ready ")
		pid := 0
		if ok {
			pid, _ = strconv.Atoi(prefix)
		}
		ready <- readySignal{pid: pid, readErr: readErr}
		io.Copy(io.Discard, reader)
		waitErr := cmd.Wait()
		waitDone <- waitErr
		in.Close()
		cancel()
		if bridge != nil {
			bridge.close()
		}
		close(link.done)
		m.mu.Lock()
		if m.links[id] == link {
			delete(m.links, id)
		}
		m.mu.Unlock()
	}()
	timer := time.NewTimer(25 * time.Second)
	defer timer.Stop()
	var failStage string
	var cause error
	select {
	case sig := <-ready:
		if sig.pid > 0 {
			link.pid = sig.pid
			if m.links == nil {
				m.links = map[string]*accountNetworkLink{}
			}
			m.links[id] = link
			return nil
		}
		failStage = "handshake"
		cause = sig.readErr
	case <-ctx.Done():
		failStage = "canceled"
		cause = ctx.Err()
	case <-timer.C:
		failStage = "timeout"
		cause = context.DeadlineExceeded
	}
	cancel()
	in.Close()
	if bridge != nil {
		bridge.close()
	}
	var waitErr error
	select {
	case waitErr = <-waitDone:
	case <-time.After(1 * time.Second):
	}
	exitCode := -1
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		exitCode = exitErr.ExitCode()
	}
	if cause == nil {
		cause = waitErr
	}
	stderrSummary := sanitizeAccountNetworkStderr(stderr.String())
	diagnostics.Log("warn", "account-network", fmt.Sprintf(
		"stage=%s elapsed_ms=%d exit=%d err=%v stderr=%q",
		failStage, time.Since(startedAt).Milliseconds(), exitCode, cause, stderrSummary,
	))
	msg := "账号专属网络启动失败，已阻止回落到环境出口"
	switch failStage {
	case "timeout":
		msg += "（等待客机专属通道就绪超时）"
	case "canceled":
		msg += "（请求已取消）"
	default:
		if exitCode >= 0 {
			msg += fmt.Sprintf("（SSH 通道退出码 %d）", exitCode)
		}
	}
	return &accountRouteError{message: msg, cause: cause}
}

func sanitizeAccountNetworkStderr(raw string) string {
	lines := strings.FieldsFunc(raw, func(r rune) bool { return r == '\r' || r == '\n' })
	var kept []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Keep short diagnostic lines; avoid echoing full filesystem paths or tokens.
		if len(line) > 160 {
			line = line[:160] + "..."
		}
		kept = append(kept, line)
		if len(kept) >= 4 {
			break
		}
	}
	return strings.Join(kept, " | ")
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
	regionExports := ""
	if tz, _ := s.accountRegionOverrides(a.ProxyNodeID); tz != "" {
		regionExports = " TZ=" + shellescape.Quote(tz)
	}
	return wrapAccountNamespace("ip netns exec net-"+a.ID+" unshare -m", "export CAGE_ACCOUNT_BROWSER="+opener+" BROWSER="+opener+regionExports+"; "+command), nil
}

const accountNamespaceMaskSockets = `set -eu
mount --make-rprivate /
for endpoint in /run/systemd/resolve/io.systemd.Resolve /run/nscd/socket /run/dbus/system_bus_socket; do
  if [ -S "$endpoint" ]; then mount --bind /dev/null "$endpoint"; fi
done`

const accountNamespaceRunPayload = `script=$ROUTE_COMMAND; unset ROUTE_COMMAND SUDO_COMMAND SUDO_UID SUDO_GID SUDO_USER; exec /bin/bash -c "$script"`

// No shell supplied by the caller runs before setpriv drops root privileges.
// Used for persistent root-owned account namespaces (`ip netns exec net-<id>`)
// which inherently require passwordless sudo / CAP_SYS_ADMIN in the guest.
func wrapAccountNamespace(namespace, command string) string {
	enter := accountNamespaceMaskSockets + "\n" + `exec setpriv --reuid="$SUDO_UID" --regid="$SUDO_GID" --init-groups /bin/bash -c '` + accountNamespaceRunPayload + `'`
	return "exec env ROUTE_COMMAND=" + shellescape.Quote(command) + " sudo -n --preserve-env=ROUTE_COMMAND,HOME,USER,LOGNAME,TERM " + namespace + " /bin/bash -c " + shellescape.Quote(enter)
}

// wrapOfflineAccountNamespace isolates local identity discovery in an empty
// network namespace and private mount namespace (masking host resolver/IPC
// sockets) without sending traffic. It prefers privileged `unshare -nm`
// (dropping back to the invoking UID via setpriv) when available, and falls
// back to unprivileged `unshare -Urnm` without setpriv in non-root guests
// where `unshare -nm` returns EPERM (#814). It never falls back to running
// unisolated.
func wrapOfflineAccountNamespace(command string) string {
	enterPrivileged := accountNamespaceMaskSockets + "\n" + `exec setpriv --reuid="$SUDO_UID" --regid="$SUDO_GID" --init-groups /bin/bash -c '` + accountNamespaceRunPayload + `'`
	enterDirect := accountNamespaceMaskSockets + "\n" + accountNamespaceRunPayload
	probePrivileged := `set -eu; mount --make-rprivate /; exec setpriv --reuid="$SUDO_UID" --regid="$SUDO_GID" --init-groups true`
	return "ROUTE_COMMAND=" + shellescape.Quote(command) + "; export ROUTE_COMMAND\n" +
		"if [ \"$(id -u)\" -eq 0 ] && unshare -nm /bin/bash -c 'set -eu; mount --make-rprivate /' 2>/dev/null; then\n" +
		"  exec unshare -nm /bin/bash -c " + shellescape.Quote(enterDirect) + "\n" +
		"fi\n" +
		"if command -v sudo >/dev/null 2>&1 && sudo -n --preserve-env=ROUTE_COMMAND,HOME,USER,LOGNAME,TERM unshare -nm /bin/bash -c " + shellescape.Quote(probePrivileged) + " 2>/dev/null; then\n" +
		"  exec sudo -n --preserve-env=ROUTE_COMMAND,HOME,USER,LOGNAME,TERM unshare -nm /bin/bash -c " + shellescape.Quote(enterPrivileged) + "\n" +
		"fi\n" +
		"exec unshare -Urnm /bin/bash -c " + shellescape.Quote(enterDirect)
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
	if node != "" {
		if err := s.validateAccountNode(node); err != nil {
			return err
		}
	}
	if node == list.PendingProxyNode {
		return nil
	}
	if s.claudeLoginActive(id) {
		return errors.New("已有授权正在进行，请先完成或取消")
	}
	list.PendingProxyNode = node
	return s.saveClaudeAccounts(list)
}

func (s *Server) validateAccountNode(node string) error {
	store, err := managedproxy.Load(s.profile)
	if err != nil {
		return err
	}
	if !validClaudeID(node) {
		return errors.New("代理节点无效，请重新选择")
	}
	_, err = store.NodeEndpoint(node)
	return err
}

// The caller holds the operation and account locks, also used by terminal admission.
func (s *Server) setAccountNode(list *claudeAccountList, in claudeAccountInput) error {
	a := list.find(in.ID)
	if a == nil || in.Action != "update" || !s.accountDesktopsSupported() {
		return errors.New("当前环境不支持账号出口")
	}
	node := *in.ProxyNodeID
	if node != "" {
		if err := s.validateAccountNode(node); err != nil {
			return err
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
			if err := s.checkAccountNetworkIdle(ctx, a.ID, "无法确认账号进程状态，暂未更改出口", "该账号仍有后台进程，请结束后再切换出口"); err != nil {
				return err
			}
		}
	}
	a.ProxyNodeID = node
	a.DesktopDedicatedNetwork = false
	return nil
}

func (s *Server) checkAccountNetworkIdle(ctx context.Context, id, probeErrMsg, busyErrMsg string) error {
	m := &s.accountNetworks
	m.mu.Lock()
	relayPID := 0
	if link := m.links[id]; link != nil {
		select {
		case <-link.done:
		default:
			relayPID = link.pid
		}
	}
	m.mu.Unlock()
	ns := "net-" + id
	pids, err := s.guest(ctx, "if test -e /run/netns/"+ns+"; then sudo ip netns pids "+ns+"; fi")
	if err != nil {
		return errors.New(probeErrMsg)
	}
	for _, pid := range strings.Fields(string(pids)) {
		if pid != strconv.Itoa(relayPID) {
			return errors.New(busyErrMsg)
		}
	}
	return nil
}

func (s *Server) stopAccountNetworkLink(id string) {
	m := &s.accountNetworks
	m.mu.Lock()
	link := m.links[id]
	if link != nil {
		delete(m.links, id)
	}
	m.mu.Unlock()
	if link != nil && link.cancel != nil {
		link.cancel()
	}
}
