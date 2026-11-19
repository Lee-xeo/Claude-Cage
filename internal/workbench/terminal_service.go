package workbench

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"al.essio.dev/pkg/shellescape"
)

var (
	errTerminalsClosed  = errors.New("后台正在退出，请重新打开 Cage。")
	errTerminalBusy     = errors.New("环境正在执行其他操作")
	errTerminalNotReady = errors.New("环境尚未就绪或正在重启")
	errTerminalLimit    = errors.New("最多保留 12 个终端，请先关闭不用的会话")
	errTerminalStart    = errors.New("无法创建终端连接")
)

type terminalRequest struct {
	BackgroundLogin bool    `json:"background_login,omitempty"`
	LoginProxyNode  *string `json:"login_proxy_node,omitempty"`
	Workspace       string  `json:"workspace"`
	Kind            string  `json:"kind"`
	ClaudeAccountID string  `json:"claude_account_id"`
	AutoHandoff     *bool   `json:"auto_handoff,omitempty"`
	loginDraft      *claudeSelection
	afterLogin      *terminalSession
	sharedID        string
	requestContext  context.Context
}

// terminalManager owns host PTYs; subscriptions only borrow their output. The
// manager serializes process creation with closure so no process can be
// published after shutdown has collected the sessions it needs to close.
type terminalManager struct {
	mu       sync.Mutex
	sessions map[string]*terminalSession
	sequence int
	closed   bool
	workers  sync.WaitGroup
}

func (m *terminalManager) startSession(cmd *exec.Cmd, workspace, kind string, selection claudeSelection, finished func()) (*terminalSession, error) {
	return m.startSessionWithPTY(cmd, workspace, kind, selection, finished, func() (terminalPTY, error) { return startTerminal(cmd, 100, 30) })
}

func (m *terminalManager) startSessionWithPTY(cmd *exec.Cmd, workspace, kind string, selection claudeSelection, finished func(), start func() (terminalPTY, error)) (*terminalSession, error) {
	return m.startReplacing(nil, false, cmd, workspace, kind, selection, finished, start)
}

func (m *terminalManager) startReplacing(previous *terminalSession, background bool, cmd *exec.Cmd, workspace, kind string, selection claudeSelection, finished func(), start func() (terminalPTY, error)) (*terminalSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.admissionErrorLocked(previous); err != nil {
		return nil, err
	}
	file, err := start()
	if err != nil {
		return nil, errTerminalStart
	}
	m.sequence++
	title := fmt.Sprintf("终端 %d", m.sequence)
	if kind == "claude" {
		title = fmt.Sprintf("Claude %d", m.sequence)
	} else if kind == "claude-login" {
		title = "Claude 账号登录"
	}
	session := newTerminalSession(file)
	session.backgroundLogin = background
	session.id, session.title, session.workspace, session.cmd = randomToken()[:16], title, workspace, cmd
	if previous != nil {
		session.id = previous.id
	}
	session.kind, session.claudeAccountID, session.physicalProfile = kind, selection.ID, selection.GuestID
	if kind == "claude-login" {
		session.loginGuestID = selection.GuestID
	}
	if selection.GuestID == "default" {
		session.claudeAccountSource = "default"
	} else if selection.ID != "" {
		session.claudeAccountSource = "managed"
	}
	if m.sessions == nil {
		m.sessions = make(map[string]*terminalSession)
	}
	m.sessions[session.id] = session
	m.workers.Add(1)
	go func() {
		defer m.workers.Done()
		session.run()
		if finished != nil {
			finished()
		}
		if background {
			m.mu.Lock()
			if m.sessions[session.id] == session {
				delete(m.sessions, session.id)
			}
			m.mu.Unlock()
		}
	}()
	return session, nil
}

func (m *terminalManager) admissionErrorLocked(previous *terminalSession) error {
	if m.closed {
		return errTerminalsClosed
	}
	if previous != nil {
		if m.sessions[previous.id] != previous || !previous.replaceable() {
			return errors.New("登录终端已关闭")
		}
		return nil
	}
	if len(m.sessions) >= 12 {
		return errTerminalLimit
	}
	return nil
}

func (m *terminalManager) canStart() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.admissionErrorLocked(nil)
}

func (m *terminalManager) snapshot() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]map[string]any, 0, len(m.sessions))
	for _, session := range m.sessions {
		out = append(out, session.summary())
	}
	return out
}

func (m *terminalManager) hasLive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, session := range m.sessions {
		session.mu.Lock()
		closed := session.closed
		session.mu.Unlock()
		if !closed {
			return true
		}
		if session.done != nil {
			select {
			case <-session.done:
			default:
				return true
			}
		}
	}
	return false
}

func (m *terminalManager) get(id string) *terminalSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

func (m *terminalManager) forConversation(connection string) *terminalSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, session := range m.sessions {
		session.mu.Lock()
		matches := session.connectionID == connection && !session.closed
		session.mu.Unlock()
		if matches {
			return session
		}
	}
	return nil
}

// Reattaching a repeated login request must not create a second OAuth flow.
// A profile can serve many normal terminals or a single exclusive login.
func (m *terminalManager) existingClaudeLogin(id string, login bool) (*terminalSession, error) {
	return m.existingProfileLogin(claudeSelection{ID: id}, login)
}
func (m *terminalManager) existingProfileLogin(selection claudeSelection, login bool) (*terminalSession, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, session := range m.sessions {
		sameAccount := session.accountID() == selection.ID
		if !sameAccount && (selection.GuestID == "" || selection.GuestID != session.physicalProfile) {
			continue
		}
		select {
		case <-session.done:
			continue
		default:
		}
		if sameAccount && session.kind == "claude-login" && login {
			return session, nil
		}
		if login || session.kind == "claude-login" {
			return nil, errors.New("请先结束此账号的登录或运行中的终端，再更改登录身份")
		}
	}
	return nil, nil
}

func (m *terminalManager) remove(id string) {
	m.mu.Lock()
	session := m.sessions[id]
	if session != nil {
		session.mu.Lock()
		session.dismissed = true
		session.mu.Unlock()
	}
	m.mu.Unlock()
	if session != nil {
		// Keep its Claude account reserved until the old PTY has exited.
		session.close()
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
	}
}

// closeAll keeps ended sessions visible until explicitly removed and permits
// new terminals after the environment starts again. Environment operations
// serialize this call with createTerminal using Server.op.
func (m *terminalManager) closeAll() {
	m.mu.Lock()
	sessions := make([]*terminalSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.mu.Unlock()
	for _, session := range sessions {
		session.close()
	}
}

func (m *terminalManager) close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	m.closeAll()
	// The final login-status observation also belongs to the session worker.
	m.workers.Wait()
}

func (s *Server) createTerminal(input terminalRequest) (*terminalSession, error) {
	if !s.op.TryLock() {
		return nil, errTerminalBusy
	}
	defer s.op.Unlock()
	if input.BackgroundLogin && input.Kind != "claude-login" {
		return nil, errors.New("后台授权只能用于 Claude 登录")
	}
	if input.AutoHandoff != nil && input.Kind != "claude" {
		return nil, errors.New("只有 Claude Code 会话可选择自动接续")
	}
	if input.Kind == "claude" && input.sharedID == "" {
		s.mu.Lock()
		launcher := claudeTerminalCommand(s.preferences)
		s.mu.Unlock()
		explicit := input.ClaudeAccountID != "" && input.ClaudeAccountID != "auto"
		if launcher != "claude" && (explicit || input.AutoHandoff != nil) {
			return nil, errors.New("手选账号与自动接续需要使用官方 claude 启动命令")
		}
		if explicit && input.AutoHandoff == nil {
			disabled := false
			input.AutoHandoff = &disabled
		}
		if input.ClaudeAccountID == "login" {
			return s.startCodeLogin(input, "")
		}
		if validClaudeID(input.ClaudeAccountID) {
			a, err := s.accountForRoute(input.ClaudeAccountID)
			if err != nil {
				return nil, err
			}
			if a.Status != "ready" || a.CodeUnlinked {
				return s.startCodeLogin(input, a.ID)
			}
		}
	}
	return s.createTerminalLocked(input)
}

// Caller holds the environment operation lock through PTY publication.
func (s *Server) createTerminalLocked(input terminalRequest) (*terminalSession, error) {
	if input.LoginProxyNode != nil && (input.Kind != "claude-login" || input.ClaudeAccountID != "" || *input.LoginProxyNode != "" && !s.accountDesktopsSupported()) {
		return nil, errors.New("仅可为新增的 Linux 账号选择代理节点")
	}
	var sharedRecord *mobileRecord
	if input.sharedID != "" {
		s.mobileCLI.mu.Lock()
		record, err := s.mobileCLI.record(s.profile, input.sharedID)
		s.mobileCLI.mu.Unlock()
		if err != nil {
			return nil, err
		}
		sharedRecord = record
		if record.Switch != nil && record.Switch.Phase == "preparing" {
			return nil, errors.New("账号交接尚未完成，请先重试或取消交接")
		}
		if record.Archived || record.Deleted {
			return nil, errors.New("此会话已归档或删除，请先从已归档会话恢复")
		}
		// The guest can be unreadable or not yet started while its host PTY
		// already owns this conversation. A stale snapshot cannot start a copy.
		if existing := s.terminals.get(record.TerminalID); existing != nil && existing.ownsConversation() {
			return existing, nil
		}
		if existing := s.terminals.get(record.TerminalID); existing != nil && existing.summary()["closed"] == true {
			s.terminals.remove(record.TerminalID)
		}
	}
	s.mu.Lock()
	closing := s.closing
	componentUpdate := s.guestUpdateRequired
	command := claudeTerminalCommand(s.preferences)
	s.mu.Unlock()
	if closing {
		return nil, errTerminalsClosed
	}
	if sharedRecord != nil && (!sharedCLILauncher(command) || sharedRecord.Launcher != "" && sharedRecord.Launcher != command) {
		return nil, errors.New("主机启动命令与原会话不一致，请恢复原配置后重试")
	}
	if !s.vmRunning() || s.busy() {
		return nil, errTerminalNotReady
	}
	// Reject a full manager before opening project shares. start checks again
	// atomically with process publication because shutdown can race this work.
	if input.Kind != "claude-login" && input.afterLogin == nil {
		if err := s.terminals.canStart(); err != nil {
			return nil, err
		}
	}
	if input.Kind != "" && input.Kind != "shell" && input.Kind != "claude" && input.Kind != "claude-login" {
		return nil, errors.New("终端类型无效")
	}
	managed := input.Kind == "claude" || input.Kind == "claude-login"
	if !managed && input.ClaudeAccountID != "" {
		return nil, errors.New("普通终端不能指定 Claude 账号")
	}
	// Login works even when the last project is disconnected or has moved.
	// It always runs from the guest home, without loading project settings.
	workspace := s.workspace()
	if input.loginDraft != nil {
		workspace = input.Workspace
	}
	var err error
	if input.Kind != "claude-login" {
		workspace, err = s.connectProject(input.Workspace, false)
		if err != nil {
			return nil, err
		}
	}
	launchCommand := command
	if input.Kind == "claude" && !sharedCLILauncher(command) {
		directories, err := s.projectAdditionalDirectories(workspace)
		if err != nil {
			return nil, err
		}
		for _, directory := range directories {
			launchCommand += " --add-dir " + shellescape.Quote(directory)
		}
	}
	script := terminalScript(guestPath(workspace), input.Kind, launchCommand)
	var selection claudeSelection
	var finished func()
	if managed {
		login := input.Kind == "claude-login"
		if input.loginDraft != nil {
			selection = *input.loginDraft
		} else if sharedRecord != nil && sharedRecord.Terminal {
			selection = sharedRecord.Account
		} else {
			selection, err = s.selectTerminalClaudeAccount(command, input.ClaudeAccountID, login, workspace)
		}
		if err != nil {
			return nil, err
		}
		if input.LoginProxyNode != nil {
			if err = s.setLoginDraftNode(selection.ID, *input.LoginProxyNode); err != nil {
				return nil, err
			}
		}
		if selection.ID != "" || input.loginDraft != nil {
			if componentUpdate {
				return nil, errGuestUpdateRequired
			}
			if (input.loginDraft == nil && !validClaudeID(selection.ID)) || !validClaudeGuestID(selection.GuestID) || (login && selection.GuestID == "default") {
				return nil, errors.New("Claude 账号 ID 无效")
			}
			if login && selection.ID != "" && s.mobileCLI.activeAccounts()[selection.ID] > 0 {
				return nil, errors.New("请先结束该账号正在执行的移动 CLI 轮次，再重新登录")
			}
			if selection.ID != "" {
				if existing, conflict := s.terminals.existingProfileLogin(selection, login); existing != nil || conflict != nil {
					if existing != nil && input.loginDraft != nil {
						return nil, errors.New("请先完成此账号正在进行的登录")
					}
					return existing, conflict
				}
			}
			if !login && selection.Subject == "" {
				return nil, errors.New("Claude 账号状态已变化，请重试")
			}
			script = managedClaudeTerminalScript(guestPath(workspace), launchCommand, selection.GuestID, selection.Subject, login)
			if login {
				if input.loginDraft == nil {
					finished = func() {
						ctx, cancel := context.WithTimeout(s.lifetimeContext(), 12*time.Second)
						defer cancel()
						_ = s.confirmClaudeLogin(ctx, selection.ID)
					}
				}
			}
		}
	}
	ordinaryShell := !managed && !componentUpdate
	if ordinaryShell {
		finished = s.refreshShellLoginWatch
	}
	var conversation *mobileRecord
	if input.Kind == "claude" && sharedCLILauncher(command) {
		script, conversation, err = s.prepareTerminalConversation(workspace, selection, input.sharedID, input.AutoHandoff)
		if err != nil {
			return nil, err
		}
	}
	if selection.ID != "" {
		script, err = s.routeAccountCommand(s.lifetimeContext(), selection.ID, script)
		if err != nil {
			return nil, err
		}
	}
	cmd := s.sshCommand(s.lifetimeContext(), "-tt", s.sshTarget(), "--", script)
	if input.requestContext != nil {
		s.account.mu.Lock()
		if !s.mobileAuthorized(input.requestContext) {
			s.account.mu.Unlock()
			return nil, errMobileLocked
		}
	}
	session, err := s.terminals.startReplacing(input.afterLogin, input.BackgroundLogin, cmd, workspace, input.Kind, selection, finished, func() (terminalPTY, error) { return startTerminal(cmd, 100, 30) })
	if input.requestContext != nil {
		s.account.mu.Unlock()
	}
	if err == nil && ordinaryShell {
		s.refreshShellLoginWatch()
	}
	if err == nil && conversation != nil {
		session.mu.Lock()
		session.sharedID = conversation.ID
		session.connectionID = conversation.Connection
		session.mu.Unlock()
		s.mobileCLI.mu.Lock()
		conversation.TerminalID = session.id
		if saveErr := writePrivate(mobileRecordPath(s.profile, conversation.ID), conversation); saveErr != nil {
			err = saveErr
		}
		s.mobileCLI.mu.Unlock()
		if err != nil {
			s.terminals.remove(session.id)
			return nil, err
		}
		s.prepareCLIConnection(session, conversation.ID)
		s.monitorClaudeAccount(session, conversation.ID)
	}
	if err == nil && input.Kind == "claude" && selection.ID != "" {
		s.noteClaudeSelection(selection.ID, workspace)
	}
	return session, err
}

func validClaudeGuestID(id string) bool { return id == "default" || validClaudeID(id) }

func (s *Server) selectTerminalClaudeAccount(command, requested string, login bool, workspace string) (claudeSelection, error) {
	parts, valid := mobileLaunchArgs(command, claudeSelection{})
	if !login && valid == nil && len(parts) == 2 && parts[0] == "ccs" {
		if requested != "" && requested != "auto" {
			return claudeSelection{}, errors.New("CCS 使用自己的配置，不能指定 Cage 账号")
		}
		return claudeSelection{}, nil
	}
	return s.selectClaudeAccount(requested, login, workspace)
}

func (s *Server) readClaudeGuestSnapshot(ctx context.Context, id string) (claudeGuestSnapshot, error) {
	return s.readClaudeGuestReport(ctx, id, claudeSnapshotCommand(id))
}

func (s *Server) readClaudeGuestUsage(ctx context.Context, id, subject string) (claudeGuestSnapshot, error) {
	if _, err := hex.DecodeString(subject); err != nil || len(subject) != 64 {
		return claudeGuestSnapshot{}, errors.New("Claude 账号身份无效")
	}
	return s.readClaudeReportForSubject(ctx, id, subject, claudeGuestReportCommand(id, subject))
}
func (s *Server) readClaudeGuestReport(ctx context.Context, id, command string) (claudeGuestSnapshot, error) {
	return s.readClaudeReportForSubject(ctx, id, "", command)
}

func (s *Server) readClaudeReportForSubject(ctx context.Context, id, subject, command string) (claudeGuestSnapshot, error) {
	var snapshot claudeGuestSnapshot
	if !validClaudeGuestID(id) {
		return snapshot, errors.New("Claude 账号 ID 无效")
	}
	a, err := s.boundGuestAccount(id, subject)
	if err != nil {
		return snapshot, err
	}
	if a != nil {
		if id == "default" && subject == "" {
			// Discover the mutable default identity locally before sending traffic.
			command = wrapAccountNamespace("unshare -nm", command)
		} else if command, err = s.routeSelected(ctx, *a, command); err != nil {
			return snapshot, err
		}
	}
	data, err := s.guest(ctx, command)
	if err != nil || json.Unmarshal(data, &snapshot) != nil {
		return snapshot, errors.New("无法读取账号状态，请确认环境已启动并同步环境组件")
	}
	return snapshot, nil
}

func claudeSnapshotCommand(id string) string {
	return claudeGuestReportCommand(id, "")
}

func claudeGuestReportCommand(id, subject string) string {
	command := "exec /usr/local/bin/cage-claude-account snapshot " + id
	if subject != "" {
		command = "exec /usr/local/bin/cage-claude-account usage " + id + " " + shellescape.Quote(subject)
	}
	if id == "default" {
		// Use the same login shell a regular terminal opens, including its
		// configured API/provider environment. Isolate startup banners from
		// the JSON output and keep the probe outside every shared project.
		command = `set -o pipefail; cd "$HOME" && env TERM=xterm-256color COLORTERM=truecolor CAGE_WORKBENCH=1 /bin/bash -lic ` +
			shellescape.Quote(`printf '\nCAGE_CLAUDE_SNAPSHOT '; `+command) +
			` | sed -n 's/^CAGE_CLAUDE_SNAPSHOT //p'`
	}
	return command
}

func managedClaudeTerminalScript(workspace, command, accountID, subject string, login bool) string {
	if login {
		return "cd \"$HOME\" && exec /usr/local/bin/cage-claude-account login " + shellescape.Quote(accountID)
	}
	// The interactive shell still interprets the user's saved command and
	// remains useful when Claude exits. The shim scopes subsequent invocations
	// in this shell to the same official CLI profile.
	env := `export CAGE_CLAUDE_ACCOUNT_ID=` + shellescape.Quote(accountID) +
		` CAGE_CLAUDE_SUBJECT=` + shellescape.Quote(subject) + "; "
	if accountID != "default" {
		env += `export CLAUDE_CONFIG_DIR="$HOME/.local/share/cage/claude-accounts/$CAGE_CLAUDE_ACCOUNT_ID/config"; `
	}
	env += `export PATH="/usr/local/lib/cage-claude-bin:$PATH"; `
	body := env + `eval "$CAGE_TERMINAL_COMMAND"; unset CAGE_TERMINAL_COMMAND; exec bash -i`
	return "cd " + shellescape.Quote(workspace) +
		" && exec env TERM=xterm-256color COLORTERM=truecolor CAGE_WORKBENCH=1 CAGE_TERMINAL_COMMAND=" +
		shellescape.Quote(command) + " bash -lic " + shellescape.Quote(body)
}

// The saved command is interpreted only by the guest's interactive shell.
// Reconnecting a viewer never executes it again; exiting Claude leaves a shell.
func terminalScript(workspace, kind, command string) string {
	prefix := "cd " + shellescape.Quote(workspace) + " && exec env TERM=xterm-256color COLORTERM=truecolor CAGE_WORKBENCH=1 "
	if kind != "claude" {
		return prefix + "bash -l"
	}
	return prefix + "CAGE_TERMINAL_COMMAND=" + shellescape.Quote(command) + " bash -lic " + shellescape.Quote("eval \"$CAGE_TERMINAL_COMMAND\"; unset CAGE_TERMINAL_COMMAND; exec bash -il")
}
