package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Read identity from the official CLI in the directory that actually logged in.
// All three entry points match by subscription identity, never by display label.
func (s *Server) verifiedCodeAccount(ctx context.Context, guest, routeID string) (claudeSelection, error) {
	var empty claudeSelection
	if !validClaudeGuestID(guest) {
		return empty, errors.New("账号登录目录无效")
	}
	epoch, ok := s.account.access()
	if !ok {
		return empty, errClaudeAccessLost
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	stop := context.AfterFunc(epoch, cancel)
	defer func() { stop(); cancel() }()
	reading, err := s.probeCodeLogin(ctx, guest, routeID)
	if err != nil {
		return empty, err
	}
	if reading.subject() == "" {
		return empty, errors.New("Claude 尚未确认订阅身份")
	}
	m := &s.claudeAccounts
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil {
		return empty, ctx.Err()
	}
	list, err := s.loadClaudeAccounts()
	if err != nil {
		return empty, err
	}
	return s.archiveCodeReading(ctx, &list, guest, reading)
}

func (s *Server) probeCodeLogin(ctx context.Context, guest, routeID string) (claudeGuestSnapshot, error) {
	if routeID == "" || s.claudeAccounts.probe != nil {
		return s.probeClaude(ctx, guest)
	}
	var reading claudeGuestSnapshot
	a, err := s.accountForRoute(routeID)
	if err != nil {
		return reading, err
	}
	if a.ProxyNodeID == "" {
		return s.probeClaude(ctx, guest)
	}
	cmd, err := s.routeSelected(ctx, a, claudeSnapshotCommand(guest))
	if err != nil {
		return reading, err
	}
	data, err := s.guest(ctx, cmd)
	if err != nil {
		return reading, err
	}
	err = json.Unmarshal(data, &reading)
	return reading, err
}

// Caller holds the account lock. Use the canonical registry ID for labels
// and the login directory that the official CLI verified for future launches.
func (s *Server) archiveCodeReading(ctx context.Context, list *claudeAccountList, guest string, reading claudeGuestSnapshot) (claudeSelection, error) {
	var empty claudeSelection
	now := time.Now()
	subject := reading.subject()
	if subject == "" {
		return empty, errors.New("尚未确认 Claude 账号身份")
	}
	dest := list.bySubject(subject)
	if guest == "default" {
		if err := s.archiveDefaultIfAvailable(ctx, list, reading, now, true); err != nil {
			return empty, err
		}
		dest = list.bySubject(subject)
	} else {
		for i := range list.Accounts {
			owner := &list.Accounts[i]
			if owner.Source != "managed" || owner.guestID() != guest || owner == dest {
				continue
			}
			if owner.Email == "" && dest == nil {
				dest = owner
				break
			}
			// The caller explicitly switched identities inside an existing
			// client. Its old history must stay with its original profile.
			owner.Status = "changed"
			if dest == nil || dest.CodeUnlinked || dest.Source == "default" {
				target := randomToken()[:24]
				snap, err := s.archiveGuestLogin(ctx, guest, target, subject)
				if err != nil || snap.subject() != subject {
					return empty, errors.New("已识别账号变更，无法验证独立登录资料，请在账号管理中重新授权")
				}
				guest = target
			}
			break
		}
		if dest == nil {
			id := guest
			if list.find(id) != nil {
				id = randomToken()[:24]
			}
			list.Accounts = append(list.Accounts, claudeAccount{ID: id, Source: "managed", Label: reading.email(), ProfileID: guest})
			dest = &list.Accounts[len(list.Accounts)-1]
		}
		if dest.Email == "" || dest.CodeUnlinked || dest.Source == "default" {
			dest.Source, dest.ProfileID, dest.CodeUnlinked = "managed", guest, false
		}
		if dest.guestID() != guest && dest.Status != "ready" {
			recovered, err := s.recoverClaudeLogin(ctx, dest.ID, dest.guestID(), guest, subject)
			if err != nil || recovered.subject() != subject {
				return empty, errors.New("请从原账号卡片重新授权 Code，以保留会话目录")
			}
		}
		if err := list.observe(dest.ID, reading, now); err != nil {
			return empty, err
		}
	}
	if list.PendingLogin == guest {
		if dest.ProxyNodeID == "" {
			dest.ProxyNodeID = list.PendingProxyNode
		}
		list.PendingLogin, list.PendingProxyNode = "", ""
	}
	if ctx.Err() != nil {
		return empty, ctx.Err()
	}
	if err := s.saveClaudeAccounts(*list); err != nil {
		return empty, err
	}
	return claudeSelection{ID: dest.ID, GuestID: dest.guestID(), Subject: subject}, nil
}

func (list *claudeAccountList) observeDefault(reading claudeGuestSnapshot, now time.Time) error {
	if subject := reading.subject(); subject != "" {
		a := list.bySubject(subject)
		if a == nil {
			list.Accounts = append(list.Accounts, claudeAccount{ID: randomToken()[:24], Label: reading.email(), Source: "default"})
		} else if a.CodeUnlinked {
			a.Source, a.ProfileID, a.CodeUnlinked = "default", "", false
		}
	}
	for i := range list.Accounts {
		a := &list.Accounts[i]
		if a.Source == "default" {
			if err := list.observe(a.ID, reading, now); err != nil {
				return err
			}
		}
	}
	return nil
}

// Preserve a file-backed default login in its own directory when first seen.
// Platform keychains still belong to the official client; those identities keep
// using the default slot, with a fresh subject check at terminal admission.
func (s *Server) archiveDefaultIfAvailable(ctx context.Context, list *claudeAccountList, reading claudeGuestSnapshot, now time.Time, retry bool) error {
	subject := reading.subject()
	previous := list.bySubject(subject)
	archive := subject != "" && (previous == nil || previous.CodeUnlinked || retry && previous.Source == "default")
	if err := list.observeDefault(reading, now); err != nil {
		return err
	}
	if !archive || !s.accountDesktopsSupported() || !s.vmRunning() {
		return nil
	}
	a := list.bySubject(subject)
	target := randomToken()[:24]
	recovered, err := s.archiveGuestLogin(ctx, "default", target, subject)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil && recovered.subject() == subject {
		a.Source, a.ProfileID = "managed", target
	}
	return nil
}

func (s *Server) archiveGuestLogin(ctx context.Context, source, target, subject string) (claudeGuestSnapshot, error) {
	return s.readClaudeReportForSubject(ctx, source, subject, "exec /usr/local/bin/cage-claude-account archive-login "+target+" "+source+" "+subject)
}
