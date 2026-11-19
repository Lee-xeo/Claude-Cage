package workbench

import (
	"context"
	"errors"
	"slices"
	"time"
)

// The guest default directory is one mutable login slot. Every identity joined
// from it has its own registry ID, participation setting, and quota history.
type claudeDetected struct {
	Identity       string       `json:"identity"`
	Email          string       `json:"email"`
	Organization   string       `json:"organization,omitempty"`
	Plan           string       `json:"plan,omitempty"`
	Quota          *claudeQuota `json:"quota,omitempty"`
	CheckedAt      int64        `json:"checked_at"`
	UsageStatus    string       `json:"usage_status,omitempty"`
	UsageCheckedAt int64        `json:"usage_checked_at,omitempty"`
	UsageChecking  bool         `json:"usage_checking,omitempty"`
}

type claudePanel struct {
	Scheduling             claudeScheduling `json:"scheduling"`
	UsagePanelRunning      bool             `json:"usage_panel_running"`
	UsageUnavailableReason string           `json:"usage_unavailable_reason,omitempty"`
	Accounts               []claudeAccount  `json:"accounts"`
	Detected               *claudeDetected  `json:"detected,omitempty"`
	DetectionStatus        string           `json:"detection_status"`
	DetectionError         string           `json:"detection_error,omitempty"`
	LoginNotice            string           `json:"login_notice,omitempty"`
}

func (s *Server) probeClaude(ctx context.Context, guestID string) (claudeGuestSnapshot, error) {
	if probe := s.claudeAccounts.probe; probe != nil {
		return probe(ctx, guestID)
	}
	return s.readClaudeGuestSnapshot(ctx, guestID)
}

func (list claudeAccountList) bySubject(subject string) *claudeAccount {
	if subject == "" {
		return nil
	}
	for i := range list.Accounts {
		a := &list.Accounts[i]
		if a.Email != "" && claudeIdentityHash(a.Email, a.Organization) == subject {
			return a
		}
	}
	return nil
}

// Panel reads coalesce for 20 seconds. A join or a launch verifies the
// identity again. The timeout covers both waiting for a probe and running it.
func (s *Server) inspectClaudeDefault(parent context.Context, force bool) (claudeGuestSnapshot, time.Time, error) {
	var empty claudeGuestSnapshot
	epoch, unlocked := s.account.access()
	if !unlocked {
		return empty, time.Time{}, errors.New("Cage 已锁定，请先解锁")
	}
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	stop := context.AfterFunc(epoch, cancel)
	defer func() { stop(); cancel() }()
	m := &s.claudeAccounts
	m.mu.Lock()
	if m.defaultGate == nil {
		m.defaultGate = make(chan struct{}, 1)
	}
	gate := m.defaultGate
	m.mu.Unlock()
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return empty, time.Time{}, errors.New("默认登录检查未完成，请重试")
	}
	m.mu.Lock()
	if !force && time.Since(m.defaultChecked) < 20*time.Second {
		reading, checked, err := m.defaultReading, m.defaultChecked, m.defaultError
		m.mu.Unlock()
		return reading, checked, err
	}
	m.mu.Unlock()
	reading, err := s.probeClaude(ctx, "default")
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil || epoch.Err() != nil || err != nil {
		reading = empty
		err = errors.New("暂时无法读取环境的默认登录，请确认 Claude Code 和环境组件可用后重试")
	} else {
		list, loadErr := s.loadClaudeAccounts()
		if loadErr != nil {
			err = loadErr
		} else {
			if reading.subject() != "" || slices.ContainsFunc(list.Accounts, func(a claudeAccount) bool { return a.Source == "default" }) {
				err = s.archiveDefaultIfAvailable(ctx, &list, reading, now, force)
				if err == nil {
					err = s.saveClaudeAccounts(list)
				}
			}
		}
	}
	m.defaultReading, m.defaultChecked, m.defaultError = reading, now, err
	if epoch.Err() != nil || parent.Err() != nil {
		m.defaultChecked = time.Time{}
	}
	return reading, now, err
}

func (s *Server) joinClaudeDefault(ctx context.Context, expected string) error {
	reading, now, err := s.inspectClaudeDefault(ctx, true)
	if err != nil {
		return err
	}
	if expected == "" || expected != reading.subject() {
		return errors.New("环境中的登录身份已经变化，请刷新后确认要加入的账号")
	}
	m := &s.claudeAccounts
	m.mu.Lock()
	defer m.mu.Unlock()
	// Another completed discovery may have already observed a new identity.
	if m.defaultReading.subject() != expected || m.defaultError != nil || ctx.Err() != nil {
		return errors.New("默认登录状态已变化，请刷新后重试")
	}
	list, err := s.loadClaudeAccounts()
	if err != nil {
		return err
	}
	a := list.bySubject(expected)
	if a == nil {
		return errors.New("登录身份尚未归档")
	}
	if a.Source != "default" || a.Enabled {
		return nil
	}
	a.Enabled = true
	a.CheckedAt = now.Unix()
	return s.saveClaudeAccounts(list)
}

func (s *Server) claudePanelView(ctx context.Context) (claudePanel, claudeAccountList, error) {
	s.mu.Lock()
	ready, update := s.ready, s.guestUpdateRequired
	s.mu.Unlock()
	view := claudePanel{DetectionStatus: "offline"}
	if unavailable := s.usageAvailability(); unavailable != nil {
		view.UsageUnavailableReason = unavailable.Error()
	}
	var reading claudeGuestSnapshot
	if update {
		view.DetectionStatus = "update_required"
		view.DetectionError = "请同步环境组件后再检测普通终端的登录"
	} else if ready {
		var err error
		reading, _, err = s.inspectClaudeDefault(ctx, false)
		if err != nil {
			view.DetectionStatus, view.DetectionError = "unavailable", err.Error()
		} else if reading.subject() == "" {
			view.DetectionStatus = "signed_out"
		} else {
			view.DetectionStatus = "ready"
		}
	}
	m := &s.claudeAccounts
	m.mu.Lock()
	list, err := s.loadClaudeAccounts()
	m.mu.Unlock()
	if err != nil {
		return view, list, err
	}
	if ready && !update && list.PendingLogin != "" && !s.claudeLoginActive(list.PendingLogin) {
		_ = s.recoverClaudePending(ctx, list.PendingLogin, false)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	list, err = s.loadClaudeAccounts()
	if err != nil {
		return view, list, err
	}
	view.Accounts, view.LoginNotice = list.Accounts, m.loginNotice
	view.Scheduling = list.Scheduling
	view.UsagePanelRunning = m.panelRunning
	for i := range view.Accounts {
		a := &view.Accounts[i]
		a.UsageChecking = m.usageActive[claudeIdentityHash(a.Email, a.Organization)] != nil
	}
	return view, list, nil
}
