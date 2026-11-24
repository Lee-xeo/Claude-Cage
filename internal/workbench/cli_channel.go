package workbench

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/google/uuid"
	"github.com/sylvinus/agent-vm/internal/diagnostics"
)

//go:embed cli_channel.py
var cliChannelHelper string

//go:embed cli_channel.resume.md
var cliResumePrompt string

//go:embed claude_account_resume.md
var claudeAccountResumePrompt string

// Continuation instructions travel over the existing control RPC only when
// resuming. They do not consume the Windows SSH command-line budget.
func resumeChannelRequest(record *mobileRecord) map[string]any {
	return map[string]any{"cli_id": record.CLIID, "connection": record.Connection, "action": "resume",
		"resume_text": cliResumePrompt, "account_resume_text": claudeAccountResumePrompt}
}

// The helper travels once on SSH standard input and is executed from the
// guest by digest. Command lines then stay small on every host: Windows limits
// CreateProcessW to 32767 UTF-16 units, including the ssh.exe arguments.
var cliChannelBundle = sync.OnceValue(func() []byte {
	var helper strings.Builder
	for _, line := range strings.Split(cliChannelHelper, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		helper.WriteString(line)
		helper.WriteByte('\n')
	}
	var compressed bytes.Buffer
	writer, _ := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	_, _ = writer.Write([]byte(helper.String()))
	_ = writer.Close()
	return compressed.Bytes()
})

// Exit status of the bootstrap when this helper version is not yet installed.
const cliChannelMissing = 75

func cliChannelDigest() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(cliChannelHelper+cliResumePrompt+claudeAccountResumePrompt)))
}

// Installs the compressed helper read from standard input. The file name is
// the version digest; the content must match the expected SHA-256.
func cliChannelInstallCommand() string {
	content := sha256.Sum256(cliChannelBundle())
	script := fmt.Sprintf(`import gzip,hashlib,os,pathlib,sys,tempfile
d=pathlib.Path.home()/".local/share/cage/cli-channel-code"
d.mkdir(parents=True,exist_ok=True,mode=0o700)
p=d/"%s.py"
b=sys.stdin.buffer.read()
if hashlib.sha256(b).hexdigest()!="%x":sys.exit(2)
if not p.is_file():
 with tempfile.NamedTemporaryFile(dir=d,delete=False) as f:
  f.write(gzip.decompress(b))
  t=f.name
 os.replace(t,p)
`, cliChannelDigest(), content)
	return shellescape.QuoteCommand([]string{"python3", "-I", "-c", script})
}

func (s *Server) installCLIChannel(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	script := "exec /bin/bash -lc " + shellescape.Quote("exec "+cliChannelInstallCommand())
	bundle := cliChannelBundle()
	var err error
	if reply, sent, runErr := s.controlSession.runInput(ctx, s.lifetimeContext(), s.guestSessionCommand, script, bundle, 64<<10); sent {
		err = runErr
		if err == nil && reply.code != 0 {
			err = fmt.Errorf("exit status %d", reply.code)
		}
	} else {
		command := "exec /bin/bash -c " + shellescape.Quote(script)
		cmd := s.sshCommand(ctx, "-T", s.sshTarget(), "--", command)
		cmd.Stdin = bytes.NewReader(bundle)
		cmd.WaitDelay = 2 * time.Second
		err = cmd.Run()
	}
	if err != nil {
		_ = diagnostics.New(s.profile).Record("warn", "cli-channel", fmt.Sprintf("operation=install failure=guest_command context=%v", ctx.Err()))
		return errors.New("电脑未能准备会话通道，聊天记录已保留。")
	}
	return nil
}

// Python's RFC 1924 base85 alphabet has no single quote. It fits the compressed
// helper within Windows' command limit without expanding shell quoting.
func cliTransport85(data []byte) string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz!#$%&()*+-;<=>?@^_`{|}~"
	var out strings.Builder
	for len(data) > 0 {
		n := min(4, len(data))
		var value uint64
		for i := 0; i < 4; i++ {
			value <<= 8
			if i < n {
				value |= uint64(data[i])
			}
		}
		var block [5]byte
		for i := 4; i >= 0; i-- {
			block[i] = alphabet[value%85]
			value /= 85
		}
		out.Write(block[:n+1])
		data = data[n:]
	}
	return out.String()
}

type terminalConversation struct {
	LoginHint           codeLoginHint `json:"login_hint"`
	NativeCCSUnverified bool          `json:"native_ccs_unverified"`
	AccountFailure      string        `json:"account_failure"`
	ResumePending       bool          `json:"resume_pending"`
	NativeHistory       bool          `json:"native_history"`
	CLIID               string        `json:"cli_id"`
	Config              string        `json:"config"`
	Connection          string        `json:"connection"`
	Workspace           string        `json:"workspace"`
	Title               string        `json:"title"`
	UpdatedAt           float64       `json:"updated_at"`
	Turns               []mobileTurn  `json:"turns"`
	CanSend             bool          `json:"can_send"`
	ChannelReady        bool          `json:"channel_ready"`
	HasHistory          bool          `json:"has_history"`
	Running             bool          `json:"running"`
	ClaudeDesktop       bool          `json:"claude_desktop"`
	Active              bool          `json:"active"`
	ConnectionNotice    string        `json:"connection_notice"`
	HistoryTruncated    bool          `json:"history_truncated"`
	// Token identifies the guest inputs of this view; Unchanged answers a
	// request that already held the view for that token.
	Token     string `json:"token"`
	Unchanged bool   `json:"unchanged"`
}

// conversationView is the last full view read for a record. Terminal monitors
// and open session views poll every few seconds; an unchanged transcript and
// state reuse it instead of the guest reparsing the whole transcript.
type conversationView struct {
	cliID, connection, config string
	current                   terminalConversation
}

const conversationViewLimit = 32

func cliChannelCommand(mode, argument string) string {
	// Versioned helpers let an open terminal keep its original protocol while a
	// rebuilt host begins new sessions. No account configuration is overwritten.
	bootstrap := fmt.Sprintf(`import os,pathlib,sys
p=pathlib.Path.home()/".local/share/cage/cli-channel-code"/"%s.py"
if not p.is_file():sys.exit(%d)
os.execv(sys.executable,[sys.executable,"-I",str(p),*sys.argv[1:]])
`, cliChannelDigest(), cliChannelMissing)
	return shellescape.QuoteCommand([]string{"python3", "-I", "-c", bootstrap, mode, argument})
}

func (s *Server) runCLIChannel(parent context.Context, mode string, input any, output any) error {
	started := time.Now()
	epoch, ok := s.account.access()
	if !ok {
		return errMobileLocked
	}
	baseCtx, baseCancel := context.WithCancel(parent)
	stop := context.AfterFunc(epoch, baseCancel)
	defer func() { stop(); baseCancel() }()
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	// Profile output goes to stderr; the login shell keeps the user's PATH.
	script := "exec 3>&1 1>&2; exec /bin/bash -lc " + shellescape.Quote("exec 1>&3 3>&-; exec "+cliChannelCommand(mode, "-"))
	var result bytes.Buffer
	var stderr fileControlStderr
	exit := -1
	transportLost := false
	sent := false
	var lastCtxErr error
	for attempt := 0; ; attempt++ {
		ctx, cancel := context.WithTimeout(baseCtx, 15*time.Second)
		result, stderr = bytes.Buffer{}, fileControlStderr{}
		exit, transportLost, sent = -1, false, false
		// Session views and discovery run every few seconds for each terminal.
		// The shared control connection avoids an SSH session and the user's
		// shell startup per call; a request that was never sent uses its own.
		reply, usedShared, runErr := s.controlSession.runInput(ctx, s.lifetimeContext(), s.guestSessionCommand, script, data, 4<<20)
		sent = usedShared
		if sent {
			result.Write(reply.stdout)
			stderr.data = reply.stderr
			err = runErr
			if err == nil {
				exit = reply.code
				if reply.code != 0 || len(reply.stdout) > 4<<20 {
					err = fmt.Errorf("exit status %d", reply.code)
				}
			} else {
				transportLost = errors.Is(err, errGuestSessionEnded)
			}
		} else {
			cmd := s.sshCommand(ctx, "-T", s.sshTarget(), "--", "exec /bin/bash -c "+shellescape.Quote(script))
			cmd.Stdin = bytes.NewReader(data)
			cmd.Stderr = &stderr
			cmd.WaitDelay = 2 * time.Second
			cmd.Stdout = &boundedStatusWriter{buffer: &result, remaining: 4 << 20}
			err = cmd.Run()
			if cmd.ProcessState != nil {
				exit = cmd.ProcessState.ExitCode()
				transportLost = exit == 255
			}
		}
		lastCtxErr = ctx.Err()
		cancel()
		// A fresh or rebuilt guest lacks this helper version: install it once.
		if attempt > 0 || err == nil || exit != cliChannelMissing {
			break
		}
		if err = s.installCLIChannel(baseCtx); err != nil {
			return err
		}
	}
	if err != nil || json.Unmarshal(result.Bytes(), output) != nil {
		failure := "guest_response"
		message := "电脑未能读取会话状态，聊天记录已保留。"
		if errors.Is(lastCtxErr, context.DeadlineExceeded) {
			failure, message = "timeout", "电脑读取运行环境超时，暂时无法获取会话状态。"
		} else if err != nil {
			failure = "guest_command"
			if transportLost {
				failure, message = "ssh_connection", "电脑暂时无法连接运行环境，聊天记录已保留。"
			}
		}
		// Record transport and protocol evidence without terminal output,
		// prompts, account paths or raw SSH/Python error messages.
		_ = diagnostics.New(s.profile).Record("warn", "cli-channel", fmt.Sprintf("operation=%s failure=%s exit=%d elapsed_ms=%d context=%v output_bytes=%d stderr_bytes=%d shared=%t", mode, failure, exit, time.Since(started).Milliseconds(), lastCtxErr, result.Len(), len(stderr.data), sent))
		return errors.New(message)
	}
	return nil
}

// Session lists are polled every few seconds by each open list (desktop
// panels, archive view, phones). A discovery result is reused for
// conversationDiscoveryReuse unless a write request or a terminal exit ran
// since; operations that must see current processes use the uncached form.
const conversationDiscoveryReuse = 4 * time.Second

type conversationDiscovery struct {
	key        string
	generation [2]uint64 // write requests, finished terminals
	at         time.Time
}

func (s *Server) invalidateConversationDiscovery() { s.conversationGeneration.Add(1) }

func (s *Server) discoverTerminalConversationsCached(ctx context.Context, paths []string) error {
	return s.discoverConversations(ctx, paths, true)
}

func matchDiscoveredAccount(list claudeAccountList, guestConfig string) (claudeSelection, string) {
	config := strings.TrimSuffix(strings.ReplaceAll(guestConfig, "\\", "/"), "/")
	for _, account := range list.Accounts {
		if account.guestID() == "default" {
			if strings.HasSuffix(config, "/.claude") {
				return claudeSelection{ID: account.ID, GuestID: "default", Subject: claudeIdentityHash(account.Email, account.Organization)}, account.Label
			}
			continue
		}
		if strings.Contains(config, "/claude-accounts/"+account.guestID()+"/") {
			return claudeSelection{ID: account.ID, GuestID: account.guestID(), Subject: claudeIdentityHash(account.Email, account.Organization)}, account.Label
		}
	}
	return claudeSelection{GuestID: "default"}, ""
}

func (s *Server) discoverTerminalConversations(ctx context.Context, paths []string) error {
	return s.discoverConversations(ctx, paths, false)
}

func (s *Server) discoverConversations(ctx context.Context, paths []string, reuse bool) error {
	s.conversationMu.Lock()
	defer s.conversationMu.Unlock()
	generation := [2]uint64{s.conversationGeneration.Load(), s.terminals.exits.Load()}
	s.mu.Lock()
	ready := s.ready && !s.closing
	s.mu.Unlock()
	if !ready {
		return nil
	}
	accounts := []string{"default"}
	s.claudeAccounts.mu.Lock()
	list, err := s.loadClaudeAccounts()
	s.claudeAccounts.mu.Unlock()
	if err != nil {
		return err
	}
	for _, account := range list.Accounts {
		if account.guestID() != "default" {
			accounts = append(accounts, account.guestID())
		}
	}
	workspaces := make(map[string]string, len(paths))
	for _, path := range paths {
		workspaces[guestPath(path)] = path
	}
	request := map[string]any{"accounts": accounts, "workspaces": workspaces}
	keyData, _ := json.Marshal(request)
	key := string(keyData)
	if last := s.conversationDiscovered; reuse && last.key == key && last.generation == generation && time.Since(last.at) >= 0 && time.Since(last.at) < conversationDiscoveryReuse {
		return nil
	}
	s.conversationDiscovered = conversationDiscovery{}
	if !reuse {
		request["fresh"] = true
	}
	var found []terminalConversation
	if err := s.runCLIChannel(ctx, "discover", request, &found); err != nil {
		return err
	}
	completed := false
	defer func() {
		if completed {
			s.conversationDiscovered = conversationDiscovery{key: key, generation: generation, at: time.Now()}
		}
	}()
	m := &s.mobileCLI
	m.mu.Lock()
	defer m.mu.Unlock()
	known := map[string]*mobileRecord{}
	running := map[string]bool{}
	desktop := map[string]bool{}
	for id := range m.catalog {
		record, err := m.record(s.profile, id)
		if err != nil {
			return err
		}
		known[record.CLIID] = record
		for _, execution := range record.Executions {
			known[execution.CLIID] = record
		}
	}
	for _, item := range found {
		var workspace, matched string
		for guest, path := range workspaces {
			if cliWorkspaceWithin(item.Workspace, guest) && len(guest) > len(matched) {
				workspace, matched = path, guest
			}
		}
		if workspace == "" || !validCLIUUID(item.CLIID) {
			continue
		}
		record := known[item.CLIID]
		if record != nil && record.CLIID != item.CLIID {
			continue
		}
		if record != nil && (record.Deleted || record.Archived) {
			continue
		}
		if record != nil && !record.Terminal {
			continue
		}
		if record == nil {
			record = &mobileRecord{mobileSession: mobileSession{mobileSummary: mobileSummary{ID: uuid.NewString(), Workspace: workspace}, Turns: []mobileTurn{}}, CLIID: item.CLIID, Terminal: true, Resume: true}
			record.Generation = m.epochLocked()
			record.Account, record.AccountLabel = matchDiscoveredAccount(list, item.Config)
		} else if record.Launcher == "" && record.Account.ID == "" && item.Config != "" {
			record.Account, record.AccountLabel = matchDiscoveredAccount(list, item.Config)
		}
		if a := list.find(record.Account.ID); a != nil {
			record.AccountLabel = a.Label
		}
		record.Workspace = workspace
		record.Active = item.Active
		record.Title, record.GuestConfig, record.Connection = item.Title, item.Config, item.Connection
		record.GuestWorkspace = item.Workspace
		if item.Connection != "" {
			if terminal := s.terminals.forConversation(item.Connection); terminal != nil {
				record.TerminalID = terminal.id
				terminal.mu.Lock()
				terminal.sharedID = record.ID
				terminal.mu.Unlock()
			}
		}
		record.UpdatedAt = time.UnixMilli(int64(item.UpdatedAt * 1000)).UTC()
		if err := writePrivate(mobileRecordPath(s.profile, record.ID), record); err != nil {
			return err
		}
		m.catalog[record.ID] = record.summary()
		running[record.ID] = running[record.ID] || item.Running
		desktop[record.ID] = desktop[record.ID] || item.Running && item.ClaudeDesktop
		known[record.CLIID] = record
	}
	m.running = running
	m.desktop = desktop
	completed = true
	return nil
}

func (s *Server) terminalConversationView(ctx context.Context, id string) (mobileSession, error) {
	return s.readTerminalConversation(ctx, id, nil)
}

func (s *Server) readTerminalConversation(ctx context.Context, id string, observation *codeLoginObservation) (mobileSession, error) {
	s.conversationMu.Lock()
	defer s.conversationMu.Unlock()
	m := &s.mobileCLI
	m.mu.Lock()
	record, err := m.record(s.profile, id)
	m.mu.Unlock()
	if err != nil {
		return mobileSession{}, err
	}
	sampledCLI, sampledConnection := record.CLIID, record.Connection
	var current terminalConversation
	input := map[string]any{"cli_id": record.CLIID, "config": record.GuestConfig, "connection": record.Connection, "account": record.Account.GuestID}
	if observation != nil && record.Launcher == "claude" {
		input["login_stamp"] = observation.stamp
	}
	cached, known := s.conversationViews[id]
	known = known && cached.cliID == record.CLIID && cached.connection == record.Connection && cached.config == record.GuestConfig
	if known {
		input["known"] = cached.current.Token
	}
	if err := s.runCLIChannel(ctx, "view", input, &current); err != nil {
		return mobileSession{}, err
	}
	if current.Unchanged {
		if !known || current.Token != cached.current.Token {
			return mobileSession{}, errors.New("电脑未能读取会话状态，聊天记录已保留。")
		}
		hint := current.LoginHint
		current = cached.current
		current.LoginHint = hint
	} else if current.Token != "" {
		if s.conversationViews == nil {
			s.conversationViews = map[string]conversationView{}
		}
		if _, ok := s.conversationViews[id]; !ok && len(s.conversationViews) >= conversationViewLimit {
			for other := range s.conversationViews {
				delete(s.conversationViews, other)
				break
			}
		}
		cachedView := current
		cachedView.LoginHint = codeLoginHint{}
		s.conversationViews[id] = conversationView{cliID: record.CLIID, connection: record.Connection, config: record.GuestConfig, current: cachedView}
	}
	root := guestPath(record.Workspace)
	if current.Workspace == "" && !current.HasHistory && record.TerminalID != "" {
		if record.Switch != nil && record.Switch.Phase == "starting" && !current.Running {
			if owner := s.terminals.get(record.TerminalID); owner == nil || !owner.ownsConversation() {
				m.mu.Lock()
				record, err = m.record(s.profile, id)
				if err == nil && record.CLIID == sampledCLI && record.Connection == sampledConnection &&
					record.Switch != nil && record.Switch.Phase == "starting" {
					record.Switch.Phase, record.Switch.Error = "prepared", "目标账号未建立会话，请重试交接"
					record.changed()
					err = writePrivate(mobileRecordPath(s.profile, id), record)
				}
				m.mu.Unlock()
				if err != nil {
					return mobileSession{}, err
				}
			}
		}
		return record.snapshot(), nil
	}
	if !cliWorkspaceWithin(current.Workspace, root) {
		return mobileSession{}, errors.New("终端的项目已变化，请重新读取会话列表")
	}
	if terminal := s.terminals.get(record.TerminalID); terminal != nil && current.Connection != "" && current.Workspace != "" {
		terminal.mu.Lock()
		if terminal.connectionID == current.Connection {
			terminal.conversationEnded = !current.Running
		}
		terminal.mu.Unlock()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Reload after SSH so another request's identity and terminal attachment are
	// retained. The guest owns transcript content, the host owns the public ID.
	record, err = m.record(s.profile, id)
	if err != nil {
		return mobileSession{}, err
	}
	if record.Deleted {
		return mobileSession{}, os.ErrNotExist
	}
	// A handoff can replace the underlying CLI while its previous view is in flight.
	if record.CLIID != sampledCLI || record.Connection != sampledConnection {
		return record.snapshot(), nil
	}
	before, _ := json.Marshal(record)
	record.Turns = adoptedMobileTurns(record.LegacyTurns, current.Turns)
	if record.Turns == nil {
		record.Turns = []mobileTurn{}
	}
	record.Title, record.ConnectionNotice = current.Title, current.ConnectionNotice
	record.CanSend, record.HistoryTruncated = &current.CanSend, current.HistoryTruncated
	record.GuestConfig, record.Connection = current.Config, current.Connection
	record.GuestWorkspace = current.Workspace
	record.Resume = record.Resume || current.HasHistory
	record.TerminalRunning = current.Running
	record.Active = current.Active
	record.AccountFailure = current.AccountFailure
	if current.NativeHistory {
		record.ResumeTranscript = ""
	}
	if record.Switch != nil && record.Switch.Phase == "starting" && current.CLIID == record.Switch.TargetID {
		if current.ChannelReady && !current.ResumePending || current.HasHistory && !current.Running {
			record.Switch.Phase, record.Switch.Error = "completed", ""
			if current.NativeHistory {
				record.ResumeTranscript = ""
			}
		} else if !current.Running {
			if owner := s.terminals.get(record.TerminalID); owner == nil || !owner.ownsConversation() {
				record.Switch.Phase, record.Switch.Error = "prepared", "目标账号未建立会话，请重试交接"
			}
		}
	}
	after, _ := json.Marshal(record)
	if string(before) != string(after) {
		record.Revision++
		record.UpdatedAt = time.UnixMilli(int64(current.UpdatedAt * 1000)).UTC()
		if err := writePrivate(mobileRecordPath(s.profile, id), record); err != nil {
			return mobileSession{}, err
		}
		if m.catalog != nil {
			m.catalog[id] = record.summary()
		}
	}
	if observation != nil {
		observation.current = current.LoginHint
		observation.connection = current.Connection
	}
	return record.snapshot(), nil
}

func (s *Server) terminalConversationAction(ctx context.Context, id string, input map[string]any) (mobileSession, error) {
	if !s.op.TryLock() {
		return mobileSession{}, errTerminalBusy
	}
	defer s.op.Unlock()
	s.account.mu.Lock()
	authorized := s.mobileAuthorized(ctx)
	s.account.mu.Unlock()
	if !authorized {
		return mobileSession{}, errMobileLocked
	}
	m := &s.mobileCLI
	m.mu.Lock()
	record, err := m.record(s.profile, id)
	m.mu.Unlock()
	if err != nil {
		return mobileSession{}, err
	}
	if record.Deleted {
		return mobileSession{}, os.ErrNotExist
	}
	if input["action"] == "send" {
		for _, previous := range record.LegacyTurns {
			if previous.ID == input["request_id"] {
				if previous.Prompt != input["text"] {
					return mobileSession{}, errors.New("同一发送编号已用于另一条消息")
				}
				view := record.snapshot()
				view.AcknowledgedRequestID = previous.ID
				return view, nil
			}
		}
	}
	if record.Connection == "" {
		return mobileSession{}, errors.New("会话连接尚未就绪，请稍后重试")
	}
	if record.Archived {
		return mobileSession{}, errors.New("此会话已归档，请先撤销归档")
	}
	input["cli_id"], input["connection"] = record.CLIID, record.Connection
	var response struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := s.runCLIChannel(ctx, "rpc", input, &response); err != nil {
		return mobileSession{}, err
	}
	if !response.OK {
		return mobileSession{}, errors.New(response.Error)
	}
	if input["action"] == "send" {
		// The guest already committed this message. Return its receipt now;
		// fetching the transcript is a separate read, not part of admission.
		s.account.mu.Lock()
		defer s.account.mu.Unlock()
		if !s.mobileAuthorized(ctx) {
			return mobileSession{}, errMobileLocked
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		record, err = m.record(s.profile, id)
		if err != nil {
			return mobileSession{}, err
		}
		view := record.snapshot()
		requestID, _ := input["request_id"].(string)
		if findMobileTurn(record, requestID) == nil {
			text, _ := input["text"].(string)
			view.Turns = append(view.Turns, mobileTurn{ID: requestID, Prompt: text, Status: "queued"})
		}
		view.AcknowledgedRequestID = requestID
		return view, nil
	}
	return s.terminalConversationView(ctx, id)
}

func (s *Server) prepareTerminalConversation(workspace string, selection claudeSelection, existing string, autoHandoff *bool) (string, *mobileRecord, error) {
	directories, err := s.projectAdditionalDirectories(workspace)
	if err != nil {
		return "", nil, err
	}
	s.mu.Lock()
	launcher := claudeTerminalCommand(s.preferences)
	s.mu.Unlock()
	s.claudeAccounts.mu.Lock()
	list, loadErr := s.loadClaudeAccounts()
	s.claudeAccounts.mu.Unlock()
	if loadErr != nil {
		return "", nil, loadErr
	}
	if err := s.installCLIChannel(s.lifetimeContext()); err != nil {
		return "", nil, err
	}
	m := &s.mobileCLI
	id := existing
	if id == "" {
		id = uuid.NewString()
	}
	if _, err := m.create(s.profile, workspace, id); err != nil {
		return "", nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record, err := m.record(s.profile, id)
	if err != nil {
		return "", nil, err
	}
	if selection.ID == "" && selection.Subject == "" {
		selection.GuestID = ""
	}
	prefix, err := mobileLaunchArgs(launcher, selection)
	if err != nil {
		return "", nil, err
	}
	record.Terminal, record.Account, record.Launcher = true, selection, launcher
	if record.SwitchPolicy == "" {
		record.SwitchPolicy = list.Scheduling.SwitchPolicy
		if existing == "" && autoHandoff != nil {
			record.SwitchPolicy = "wait"
			if *autoHandoff {
				record.SwitchPolicy = "allow"
			}
		}
		if a := list.find(selection.ID); a != nil {
			record.AccountLabel = a.Label
		}
	}
	record.TerminalID, record.TerminalRunning = "", true
	record.Connection = uuid.NewString()
	if record.ResumeTranscript != "" && record.Switch != nil {
		record.Connection = record.Switch.ID
	}
	payload := map[string]any{"id": record.Connection, "session_id": record.CLIID, "prefix": prefix, "config": record.GuestConfig}
	if s.codexWindow() != "" && !isClientMountPath(workspace) {
		payload["codex_enabled"] = true
	}
	if len(directories) > 0 {
		payload["additional_directories"] = directories
	}
	if record.Resume {
		payload["resume"] = record.CLIID
	}
	if record.ResumeTranscript != "" {
		payload["resume_transcript"] = record.ResumeTranscript
		payload["resume"] = ""
		payload["switch_id"] = record.Switch.ID
		payload["continue_after_switch"] = record.Switch.Continue
	}
	data, _ := json.Marshal(payload)
	record.CanSend = new(bool)
	record.ConnectionNotice = ""
	body := cliChannelCommand("launch", string(data)) + "; exec bash -i"
	directory := record.GuestWorkspace
	if directory == "" {
		directory = guestPath(workspace)
	}
	record.GuestWorkspace = directory
	if err := writePrivate(mobileRecordPath(s.profile, id), record); err != nil {
		return "", nil, err
	}
	script := "cd " + shellescape.Quote(directory) + " && exec env TERM=xterm-256color COLORTERM=truecolor CAGE_WORKBENCH=1 bash -lic " + shellescape.Quote(body)
	return script, record, nil
}

func (s *Server) isTerminalConversation(id string) bool {
	s.mobileCLI.mu.Lock()
	defer s.mobileCLI.mu.Unlock()
	record, err := s.mobileCLI.record(s.profile, id)
	return err == nil && record.Terminal
}

func (s *Server) stopTerminalConversation(ctx context.Context, view mobileSession, turnID string) (mobileSession, error) {
	if len(view.Turns) == 0 || view.Turns[len(view.Turns)-1].ID != turnID || view.Turns[len(view.Turns)-1].Status != "running" {
		return view, errors.New("本轮运行状态已变化，请重新读取")
	}
	s.mobileCLI.mu.Lock()
	record, err := s.mobileCLI.record(s.profile, view.ID)
	s.mobileCLI.mu.Unlock()
	if err != nil {
		return view, err
	}
	terminal := s.terminals.get(record.TerminalID)
	if terminal == nil {
		return view, errors.New("请在电脑终端停止这次运行")
	}
	s.account.mu.Lock()
	if !s.mobileAuthorized(ctx) {
		s.account.mu.Unlock()
		return view, errMobileLocked
	}
	err = terminal.enqueueInput(ctx, "\x1b")
	s.account.mu.Unlock()
	if err != nil {
		return view, err
	}
	return s.terminalConversationAction(ctx, view.ID, map[string]any{"action": "stop"})
}
