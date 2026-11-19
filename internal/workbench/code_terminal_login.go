package workbench

import (
	"context"
	"errors"
	"time"
)

// A Code terminal can start with the official login flow. Each invocation
// owns a fresh guest directory, even if another terminal is logging in.
func (s *Server) startCodeLogin(input terminalRequest, expectedID string) (*terminalSession, error) {
	epoch, unlocked := s.account.access()
	if !unlocked {
		return nil, errClaudeAccessLost
	}
	var err error
	input.Workspace, err = s.projectWorkspace(input.Workspace)
	if err != nil {
		return nil, err
	}
	ctx, done, err := s.beginActivity(epoch)
	if err != nil {
		return nil, err
	}
	draft := claudeSelection{ID: expectedID, GuestID: randomToken()[:24]}
	existingProfile := false
	if expectedID != "" {
		a, err := s.accountForRoute(expectedID)
		if err != nil {
			done()
			return nil, err
		}
		if a.Source == "managed" && !a.CodeUnlinked && a.Email != "" {
			draft.GuestID = a.guestID()
			existingProfile = true
		}
	}
	login, err := s.createTerminalLocked(terminalRequest{Kind: "claude-login", Workspace: input.Workspace, loginDraft: &draft, requestContext: input.requestContext})
	if err != nil {
		done()
		return nil, err
	}
	go func() {
		defer done()
		select {
		case <-ctx.Done():
			return
		case <-login.done:
		}
		if !login.replaceable() {
			return
		}
		login.mu.Lock()
		if existingProfile && !login.exitOK {
			login.loginNotice = "本次登录未完成。"
			login.mu.Unlock()
			return
		}
		login.loginNotice = "正在确认登录账号…"
		login.mu.Unlock()
		check, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		selected, err := s.verifiedCodeAccount(check, draft.GuestID, expectedID)
		if err == nil && selected.ID == "" {
			err = errors.New("未完成 Claude 登录")
		}
		if err == nil && expectedID != "" && selected.ID != expectedID {
			err = errors.New("登录身份与所选账号不同，请重新选择启动账号")
		}
		if err == nil {
			// Admission within the same operation and unlock epoch as an
			// ordinary terminal. Replacement reuses the existing tab and slot.
			s.op.Lock()
			input.ClaudeAccountID = selected.ID
			input.afterLogin = login
			input.requestContext = context.WithValue(ctx, accountEpochKey{}, epoch)
			if ctx.Err() != nil || !login.replaceable() {
				s.op.Unlock()
				return
			}
			_, err = s.createTerminalLocked(input)
			s.op.Unlock()
		}
		if err != nil && ctx.Err() == nil {
			login.mu.Lock()
			login.loginNotice = err.Error()
			login.mu.Unlock()
		}
	}()
	return login, nil
}

type codeLoginHint struct {
	Stamp   string `json:"stamp"`
	Subject string `json:"subject"`
	Owner   string `json:"owner"`
}

type codeLoginObservation struct {
	stamp      string
	current    codeLoginHint
	connection string
	nextTry    time.Time
}

// Terminal links and keyboard /login are the native-browser fallback. Two
// concurrent claims for one physical profile leave ownership unconfirmed.
func (m *terminalManager) ownsLoginRequest(owner *terminalSession, guest string, unbound bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	claimed, sibling := false, false
	for _, t := range m.sessions {
		t.mu.Lock()
		physical := t.physicalProfile
		if physical == "" {
			physical = "default"
		}
		sameProfile := t.kind == "claude" && physical == guest && !t.closed && !t.conversationEnded
		if t != owner && sameProfile {
			sibling = true
		}
		recent := sameProfile && !t.loginRequested.IsZero() && time.Since(t.loginRequested) < 15*time.Minute
		t.mu.Unlock()
		if recent {
			if t != owner {
				return false
			}
			claimed = true
		}
	}
	return claimed || unbound && !sibling
}

func (s *Server) acceptLoginHint(ctx context.Context, record *mobileRecord, subject string) bool {
	query := map[string]any{"connection": record.Connection, "cli_id": record.CLIID, "config": record.GuestConfig, "subject": subject}
	var reply struct {
		OK bool `json:"ok"`
	}
	return s.runCLIChannel(ctx, "account-binding", query, &reply) == nil && reply.OK
}

// Ordinary monitor ticks compare a file version alongside the existing CLI
// view. Only a different OAuth identity starts an official auth status check.
func (s *Server) observeCodeLogin(ctx context.Context, view mobileSession, state *codeLoginObservation) bool {
	hint := state.current
	if hint.Stamp == "" || hint.Stamp == state.stamp {
		return false
	}
	if hint.Subject == "" {
		state.stamp = hint.Stamp
		return false
	}
	m := &s.mobileCLI
	m.mu.Lock()
	record, err := m.record(s.profile, view.ID)
	m.mu.Unlock()
	if err != nil || record.Launcher != "claude" || record.Connection != state.connection {
		return true
	}
	if hint.Subject == record.Account.Subject {
		state.stamp = hint.Stamp
		return false
	}
	if time.Now().Before(state.nextTry) {
		return true
	}
	state.nextTry = time.Now().Add(time.Minute)
	guest := record.Account.GuestID
	if guest == "" {
		guest = "default"
	}
	terminal := s.terminals.get(record.TerminalID)
	if terminal == nil {
		return true
	}
	owns := record.Connection != "" && hint.Owner == record.Connection || hint.Owner == "" && s.terminals.ownsLoginRequest(terminal, guest, record.Account.ID == "")
	if !owns {
		// A sibling may share this CLI directory. Another process cannot take over this session.
		reading, e := s.probeCodeLogin(ctx, guest, record.Account.ID)
		if e != nil || ctx.Err() != nil || reading.subject() == "" {
			return true
		}
		if reading.subject() == record.Account.Subject {
			if !s.acceptLoginHint(ctx, record, hint.Subject) {
				return true
			}
			state.stamp = hint.Stamp
			return false
		}
		terminal.mu.Lock()
		if terminal.connectionID != record.Connection || terminal.claudeAccountID != record.Account.ID {
			terminal.mu.Unlock()
			return true
		}
		terminal.loginNotice = "这个账号的登录已在其他终端更改。原会话已停止，历史和账号绑定已保留。"
		terminal.mu.Unlock()
		terminal.close()
		state.stamp = hint.Stamp
		return true
	}
	result, err := s.verifiedCodeAccount(ctx, guest, record.Account.ID)
	if err != nil || ctx.Err() != nil {
		return true
	}
	if result.Subject == record.Account.Subject && record.Account.ID == result.ID {
		if !s.acceptLoginHint(ctx, record, hint.Subject) {
			return true
		}
		state.stamp = hint.Stamp
		return false
	}
	if !s.op.TryLock() {
		state.nextTry = time.Time{}
		return true
	}
	defer s.op.Unlock()
	from, err := s.accountForRoute(record.Account.ID)
	if err != nil {
		return true
	}
	to, err := s.accountForRoute(result.ID)
	if err != nil {
		return true
	}
	m.mu.Lock()
	current, err := m.record(s.profile, view.ID)
	if err != nil || current.Connection != record.Connection || current.Account != record.Account || current.Archived || current.Deleted || ctx.Err() != nil {
		m.mu.Unlock()
		return true
	}
	// History and the running CLI stay in the physical login directory;
	// new terminals for this identity use its canonical registry directory.
	result.GuestID = guest
	current.Account, current.AccountLabel = result, to.Label
	current.changed()
	err = writePrivate(mobileRecordPath(s.profile, current.ID), current)
	if err == nil && m.catalog != nil {
		m.catalog[current.ID] = current.summary()
	}
	m.mu.Unlock()
	if err != nil {
		return true
	}
	terminal = s.terminals.get(current.TerminalID)
	if terminal != nil {
		terminal.mu.Lock()
		matches := terminal.connectionID == record.Connection
		if matches {
			terminal.loginRequested = time.Time{}
			terminal.claudeAccountID = result.ID
			terminal.claudeAccountSource = "managed"
			if guest == "default" {
				terminal.claudeAccountSource = "default"
			}
			if from.ProxyNodeID != to.ProxyNodeID {
				terminal.loginNotice = "账号出口已改变，请重新打开会话以使用该账号的出口。"
			}
		}
		terminal.mu.Unlock()
		if matches && from.ProxyNodeID != to.ProxyNodeID {
			terminal.close()
		}
	}
	if from.ProxyNodeID == to.ProxyNodeID && !s.acceptLoginHint(ctx, current, hint.Subject) && terminal != nil {
		terminal.mu.Lock()
		terminal.loginNotice = "已确认新账号。请重新打开会话以应用登录。"
		terminal.mu.Unlock()
		terminal.close()
	}
	state.stamp = hint.Stamp
	return true
}
