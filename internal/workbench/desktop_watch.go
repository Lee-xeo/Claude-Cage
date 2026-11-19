package workbench

import (
	"bufio"
	"context"
	_ "embed"
	"sync"
	"time"

	"al.essio.dev/pkg/shellescape"
)

//go:embed desktop-watch.py
var desktopWatch string

type desktopWatchState struct {
	sync.Mutex
	active bool
	starts uint64
}

func (s *Server) watchDesktopLogins() {
	epoch, unlocked := s.account.access()
	if !unlocked {
		return
	}
	monitor := &s.desktopLoginWatch
	monitor.Lock()
	monitor.starts++
	if monitor.active {
		monitor.Unlock()
		return
	}
	monitor.active = true
	start := monitor.starts
	monitor.Unlock()
	ctx, finish, err := s.beginActivity(epoch)
	if err != nil {
		monitor.Lock()
		monitor.active = false
		monitor.Unlock()
		return
	}
	go func() {
		defer finish()
		defer func() {
			monitor.Lock()
			monitor.active = false
			retry := monitor.starts != start
			monitor.Unlock()
			if retry && ctx.Err() == nil {
				s.watchDesktopLogins()
			}
		}()
		cmd := s.sshCommand(ctx, "-T", s.sshTarget(), "--", "exec /usr/bin/python3 -I -c "+shellescape.Quote(desktopWatch))
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			return
		}
		if err = cmd.Start(); err != nil {
			pipe.Close()
			return
		}
		defer cmd.Wait()
		scanner := bufio.NewScanner(pipe)
		for scanner.Scan() {
			if scanner.Text() != "changed" {
				continue
			}
			s.op.Lock()
			if ctx.Err() == nil {
				check, cancel := context.WithTimeout(ctx, 90*time.Second)
				if seen, err := s.inspectDesktops(check); err == nil {
					_ = s.reconcileDesktops(check, seen)
					s.desktopCatalogRevision.Add(1)
				}
				cancel()
			}
			s.op.Unlock()
		}
	}()
}
