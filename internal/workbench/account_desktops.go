package workbench

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/http"

	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/sylvinus/agent-vm/internal/containerenv"
)

//go:embed linux-account-desktop.sh
var linuxAccountDesktop string

type desktopEntry struct {
	Generation int    `json:"generation,omitempty"`
	AccountID  string `json:"account_id,omitempty"`
	ID         string `json:"id"`
	Number     uint32 `json:"number"`
	Label      string `json:"label"`
	Running    bool   `json:"running"`
	Dedicated  bool   `json:"dedicated"`
}

func (s *Server) accountDesktopsSupported() bool {
	return !containerenv.Configured(s.profile)
}

func (s *Server) accountDesktop(w http.ResponseWriter, r *http.Request) {
	id, action := r.PathValue("id"), r.PathValue("action")
	if !validClaudeID(id) || !(r.Method == "GET" && (action == "vnc" || action == "vnc-credentials") || r.Method == "POST" && (action == "start" || action == "stop")) {
		http.NotFound(w, r)
		return
	}
	if r.Method == "POST" {
		var input struct{}
		if !readJSON(w, r, &input) {
			return
		}
	}
	if !s.accountDesktopsSupported() {
		http.Error(w, "此环境尚未支持独立 Desktop", http.StatusNotImplemented)
		return
	}
	if !s.vmRunning() {
		http.Error(w, "请先启动环境", 409)
		return
	}
	s.claudeAccounts.mu.Lock()
	list, err := s.loadClaudeAccounts()
	s.claudeAccounts.mu.Unlock()
	if err != nil {
		requestError(w, err)
		return
	}
	if list.desktopOwner(id) == nil && list.PendingDesktop != id && (list.find(id) == nil || list.DesktopRedirects[id] != "") {
		http.NotFound(w, r)
		return
	}
	run := "/run/desktop-" + id
	switch action {
	case "vnc":
		s.streamDesktopSocket(w, r, run+"/vnc.sock")
		return
	case "vnc-credentials":
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		b, err := s.guest(ctx, "systemctl is-active --quiet desktop-"+id+".service && cat "+run+"/password")
		if err != nil || len(b) != 8 {
			http.Error(w, "此 Desktop 尚未启动", 409)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		jsonReply(w, map[string]string{"password": string(b)})
		return
	}
	s.launch(w, "desktop-instance-"+action, func() error {
		// Reload inside the job's operation lock. A draft can be removed or
		// its dedicated-network selection can change between admission and execution.
		s.claudeAccounts.mu.Lock()
		current, err := s.loadClaudeAccounts()
		if err != nil {
			s.claudeAccounts.mu.Unlock()
			return err
		}
		selected := current.desktopOwner(id)
		// Older callers can launch an existing account by its registry ID.
		if selected == nil && current.PendingDesktop != id && current.DesktopRedirects[id] == "" {
			if a := current.find(id); a != nil && a.DesktopInstanceID == "" && action == "start" {
				a.DesktopInstanceID = id
				err = s.saveClaudeAccounts(current)
				selected = a
			}
		}
		var owner claudeAccount
		if selected != nil {
			owner = *selected
		}
		s.claudeAccounts.mu.Unlock()
		if err != nil {
			return err
		}
		if selected == nil && current.PendingDesktop != id {
			return errors.New("Desktop 归属已变化，请刷新列表")
		}
		return s.changeAccountDesktop(s.operationContext(), id, owner, action)
	})
}

func (s *Server) changeAccountDesktop(parent context.Context, instance string, account claudeAccount, action string) error {
	var err error
	scope := "stop"
	var key []byte
	if action == "start" {
		scope = "shared"
		if account.DesktopDedicatedNetwork {
			scope = "dedicated"
		}
		if account.ProxyNodeID != "" {
			if err = s.ensureAccountNetwork(parent, account.ID, account.ProxyNodeID); err != nil {
				return err
			}
			scope = "node-" + account.ID
		}
		key, err = s.account.desktopKey()
		if err != nil {
			return err
		}
		defer clear(key)
	}
	ctx, cancel := context.WithTimeout(parent, 100*time.Second)
	defer cancel()
	code := account.guestID()
	if code == "" {
		code = "default"
	}
	command := fmt.Sprintf("exec /bin/bash -c %s -- %s %s %s", shellescape.Quote(linuxAccountDesktop), instance, code, scope)
	cmd := s.sshCommand(ctx, "-T", s.sshTarget(), "--", command)
	cmd.Stdin = bytes.NewReader(key)
	output, err := cmd.CombinedOutput()
	if err != nil {
		msg := "Desktop 操作失败，请确认已安装 Claude Desktop 并检查客机服务"
		if bytes.Contains(output, []byte("专属网络尚未就绪")) {
			msg = "此账号的专属网络尚未就绪"
		}
		return errors.New(msg)
	}
	if action == "start" {
		s.watchDesktopLogins()
	}
	return nil
}
