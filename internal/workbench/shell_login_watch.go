package workbench

import (
	"bufio"
	"context"
	_ "embed"
	"sync"
	"time"

	"al.essio.dev/pkg/shellescape"
)

//go:embed shell-login-watch.py
var shellLoginWatch string

type shellLoginWatchState struct {
	sync.Mutex
	epoch context.Context
	stop  context.CancelFunc
}

func (m *terminalManager) hasLiveShell() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.sessions {
		t.mu.Lock()
		live := !t.closed && (t.kind == "" || t.kind == "shell")
		t.mu.Unlock()
		if live {
			return true
		}
	}
	return false
}

// Each unlocked Linux guest uses one sleeping inotify reader for all ordinary
// workbench shells. Its stdin closes when the final shell exits; cancelling the
// access epoch or controller lifetime kills the connection immediately.
func (s *Server) refreshShellLoginWatch() {
	if s.account == nil {
		return
	}
	m := &s.shellLoginWatch
	m.Lock()
	defer m.Unlock()
	epoch, unlocked := s.account.access()
	wanted := unlocked && s.terminals.hasLiveShell()
	if m.stop != nil {
		if wanted && m.epoch == epoch {
			return
		}
		m.stop()
		m.stop = nil
	}
	if !wanted {
		return
	}
	ctx, finish, err := s.beginActivity(epoch)
	if err != nil {
		return
	}
	shells, stop := context.WithCancel(ctx)
	m.epoch, m.stop = epoch, stop
	go func() {
		defer finish()
		defer stop()
		for delay := time.Second; shells.Err() == nil; delay = min(30*time.Second, 2*delay) {
			s.observeShellLogins(ctx, shells)
			select {
			case <-shells.Done():
				return
			case <-time.After(delay):
			}
		}
	}()
}

func (s *Server) observeShellLogins(ctx, shells context.Context) {
	cmd := s.sshCommand(ctx, "-T", s.sshTarget(), "--", "exec /usr/bin/python3 -I -c "+shellescape.Quote(shellLoginWatch))
	in, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	defer in.Close()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if err = cmd.Start(); err != nil {
		out.Close()
		return
	}
	stopInput := context.AfterFunc(shells, func() { _ = in.Close() })
	defer stopInput()
	defer cmd.Wait()
	scanner := bufio.NewScanner(out)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		if scanner.Text() == "changed" {
			_, _, _ = s.inspectClaudeDefault(ctx, true)
		}
	}
}
