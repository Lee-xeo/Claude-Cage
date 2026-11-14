//go:build darwin && (amd64 || (arm64 && cgo && !no_vz))

package maclogin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/sylvinus/agent-vm/internal/guestos"
)

// Both device backends use the same session, screen ownership, password target
// and maintenance checks. A surface supplies only the bounded display/input.
type loginSurface interface {
	Enable(bool)
	Image([]int) ([]byte, error)
	Click(int, int) bool
	Key(int, bool) bool
	Scroll(int, int, int, int) bool
	SelectAll() bool
}

func ownsLoginDirectory(dir string) bool {
	config := os.Getenv("CLAUDE_CAGE_CONFIG")
	if !filepath.IsAbs(config) || !sameGuestDirectory(dir, filepath.Join(filepath.Dir(config), "state", "lima", "mac")) {
		return false
	}
	profile, err := guestos.LoadMacOSProfileOrRecovery(filepath.Dir(config))
	return err == nil && profile != nil
}

func serveLogin(ctx context.Context, dir string, surface loginSurface) error {
	if !ownsLoginDirectory(dir) {
		return errors.New("invalid local macOS login view")
	}
	socket := filepath.Join(dir, "cage-login.sock")
	if fi, err := os.Lstat(socket); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return errors.New("unexpected login socket path")
		}
		existing, err := net.DialTimeout("unix", socket, 300*time.Millisecond)
		if err == nil {
			existing.Close()
			return errors.New("this instance already has a login view")
		}
		if err := os.Remove(socket); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return err
	}
	defer ln.Close()
	if err := os.Chmod(socket, 0600); err != nil {
		return err
	}
	s := loginSession{profile: filepath.Dir(os.Getenv("CLAUDE_CAGE_CONFIG")), surface: surface}
	defer s.close()
	for ctx.Err() == nil {
		_ = ln.SetDeadline(time.Now().Add(time.Second))
		conn, err := ln.Accept()
		if err != nil {
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				if s.token != "" && (time.Now().After(s.expires) || s.currentScreen() != s.screenID) {
					s.close()
				}
				continue
			}
			return err
		}
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		var input request
		if json.NewDecoder(io.LimitReader(conn, 4096)).Decode(&input) == nil {
			if input.Maintenance != "" {
				_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
			}
			result := s.handle(input)
			if err := json.NewEncoder(conn).Encode(result); err != nil && result.Session != "" {
				s.close()
			}
		}
		_ = conn.Close()
	}
	return nil
}
