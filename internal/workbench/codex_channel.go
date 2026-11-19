package workbench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A private host setting enables this local preview once for all new Claude
// terminals. No guest request can enable it or select a different backend.
func (s *Server) codexWindow() string {
	marker, err := os.Lstat(filepath.Join(s.profile, "codex-window.enabled"))
	if err != nil || !marker.Mode().IsRegular() {
		return ""
	}
	return localCodex()
}

type codexDelivery struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	OK      bool   `json:"ok"`
}

// The terminal's host-side record selects the project and lifetime. The guest
// may supply only a bounded message and an opaque request receipt.
func (s *Server) serveCodexChannel(terminal *terminalSession, record *mobileRecord, bin string) {
	epoch, ok := s.account.access()
	if !ok || bin == "" {
		return
	}
	ctx, cancel := context.WithCancel(s.lifetimeContext())
	stop := context.AfterFunc(epoch, cancel)
	s.activities.Add(1)
	go func() {
		defer s.activities.Done()
		defer cancel()
		defer stop()
		go func() {
			select {
			case <-ctx.Done():
			case <-terminal.done:
				cancel()
			}
		}()
		s.runCodexRequests(ctx, bin, record.Workspace, false, terminal.ownsConversation, func(ctx context.Context, req map[string]string, out *codexDelivery) error {
			req["connection"], req["cli_id"] = record.Connection, record.CLIID
			return s.runCLIChannel(ctx, "rpc", req, out)
		})
	}()
}

func (s *Server) runCodexRequests(ctx context.Context, bin, workspace string, computerUse bool, owned func() bool, exchange func(context.Context, map[string]string, *codexDelivery) error) {
	var session *codexConversation
	defer func() {
		if session != nil {
			session.close()
		}
	}()
	base := map[string]string{"action": "codex-read"}
	var lastID, answer, status string
	for ctx.Err() == nil && owned() && s.codexWindow() == bin {
		var pending codexDelivery
		err := exchange(ctx, base, &pending)
		if err == nil && pending.ID == "" && !pending.OK {
			return
		}
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}
		if pending.ID == "" {
			continue
		}
		if !validCLIUUID(pending.ID) || strings.TrimSpace(pending.Message) == "" || len(pending.Message) > 8192 {
			return
		}
		if ctx.Err() != nil || !owned() || s.codexWindow() != bin {
			return
		}
		if pending.ID != lastID {
			project, err := s.projectWorkspace(workspace)
			if err != nil || isClientMountPath(project) {
				return
			}
			lastID = pending.ID
			if session == nil {
				session, err = startCodexConversationMode(ctx, bin, project, computerUse)
			}
			if err == nil {
				answer, err = session.ask(pending.Message)
			}
			status = "completed"
			if err != nil {
				status, answer = "failed", "The result is unavailable. Check the project before retrying. Your next message starts a fresh conversation."
				if session != nil {
					session.close()
					session = nil
				}
			}
		}
		// An interrupted acknowledgement can only redeliver the saved answer.
		var ack codexDelivery
		_ = exchange(ctx, map[string]string{"action": "codex-done", "id": lastID, "status": status, "answer": answer}, &ack)
	}
}
