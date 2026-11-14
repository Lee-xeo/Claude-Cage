//go:build darwin && (amd64 || (arm64 && cgo && !no_vz))

// Package maclogin serves the authenticated login and maintenance surfaces of a bound local Mac.
package maclogin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"io"
	"os/exec"

	"al.essio.dev/pkg/shellescape"
	"time"

	"github.com/sylvinus/agent-vm/internal/guestos"
)

type request = guestos.MacLoginRequest
type reply = guestos.MacLoginReply

type loginSession struct {
	surface                  loginSurface
	profile                  string
	token                    string
	expires                  time.Time
	screenID                 string
	setup                    bool
	maintenance              string
	maintenanceFrame         string
	maintenanceMachineID     string
	maintenanceAdministrator string
	passwordSelection        passwordSelection
}

func (s *loginSession) currentScreen() string {
	if s.maintenance != "" {
		surface, err := s.maintenanceProbe(false)
		if err != nil {
			return ""
		}
		return "maintenance-" + surface.Session
	}
	// Check via the separate provisioning account. Guest development files and
	// the standard user's shell initialization cannot grant this view.
	bound, err := guestos.LoadMacOSProfileOrRecovery(s.profile)
	if err != nil || bound == nil {
		return ""
	}
	if s.setup {
		if bound.SetupID == "" {
			return ""
		}
		return "setup-" + bound.SetupID
	}
	user, err := guestos.MacOSProvisionUser(s.profile)
	if err != nil {
		return ""
	}
	args, err := guestos.MacOSSSHArgs(s.profile, user)
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	script := `set -eu
/usr/bin/pgrep -x loginwindow >/dev/null
owner=$(/usr/bin/stat -f '%u %Su' /dev/console)
if [ "$owner" = "0 root" ]; then /usr/bin/printf 'system-login'; exit 0; fi
uid=$(/usr/bin/id -u ` + shellescape.Quote(bound.User) + `)
/usr/bin/printf '%s %s\n' "$owner" "$uid"
/usr/sbin/ioreg -a -d1 -c IORegistryEntry -k IOConsoleUsers -l | /usr/bin/plutil -extract IOConsoleUsers xml1 -o - -`
	cmd := exec.CommandContext(ctx, "/usr/bin/ssh", append(args, "-T", "cage-macos", "--", "exec /bin/sh -c "+shellescape.Quote(script))...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	reader, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return ""
	}
	b, readErr := io.ReadAll(io.LimitReader(reader, (32<<10)+1))
	if readErr != nil || len(b) > 32<<10 {
		cancel()
	}
	if err = cmd.Wait(); err != nil || readErr != nil || len(b) > 32<<10 {
		return ""
	}
	return permittedLoginScreen(bytes.TrimSpace(b), bound.User)
}

func (s *loginSession) close() {
	if s.token != "" {
		s.surface.Enable(false)
	}
	s.token = ""
	s.screenID = ""
	s.setup = false
	s.maintenance = ""
	s.maintenanceFrame = ""
	s.maintenanceMachineID = ""
	s.maintenanceAdministrator = ""
	s.passwordSelection = passwordSelection{}
}

func (s *loginSession) handle(in request) reply {
	if in.Maintenance != "" || (s.maintenance != "" && in.Action != "open") {
		return s.handleMaintenance(in)
	}
	if in.Action == "finish" && in.Setup {
		bound, err := guestos.LoadMacOSProfile(s.profile)
		if err != nil || bound == nil || bound.SetupID == "" {
			return reply{Closed: true}
		}
		s.close()
		if err = guestos.FinishMacOSSetup(s.profile, bound.SetupID); err != nil {
			return reply{Error: "未能保存首次设置，请重试"}
		}
		return reply{Closed: true}
	}
	if in.Action == "open" {
		s.close()
		s.setup = in.Setup
		s.screenID = s.currentScreen()
		if s.screenID == "" {
			return reply{Error: "客机已解锁，或尚未进入系统登录画面。"}
		}
		s.token = rand.Text()
		s.expires = time.Now().Add(2 * time.Minute)
		if s.setup {
			s.expires = time.Now().Add(15 * time.Minute)
		}
		s.surface.Enable(true)
		return reply{Session: s.token}
	}
	if s.token == "" || s.setup != in.Setup || subtle.ConstantTimeCompare([]byte(in.Session), []byte(s.token)) != 1 {
		return reply{Closed: true}
	}
	if time.Now().After(s.expires) || in.Action == "close" || s.currentScreen() != s.screenID {
		s.close()
		return reply{Closed: true}
	}
	switch in.Action {
	case "fill-password":
		if err := s.fillSetupPassword(in.Password); err != nil {
			return reply{Error: err.Error()}
		}
	case "frame":
		frame, err := s.surface.Image(nil)
		if err != nil {
			return reply{Error: "尚未收到 Apple 登录画面"}
		}
		if s.currentScreen() != s.screenID {
			s.close()
			return reply{Closed: true}
		}
		return reply{Image: frame, MIME: "image/jpeg"}
	case "click":
		s.passwordSelection = passwordSelection{}
		if in.X < 0 || in.Y < 0 || in.X >= 1024 || in.Y >= 640 || !s.surface.Click(in.X, in.Y) {
			return reply{Error: "无效的登录位置"}
		}
		if s.setup {
			s.selectPasswordPosition(in.X, in.Y)
		}
	case "key":
		s.passwordSelection = passwordSelection{}
		// ANSI printable keys, Return, Tab, Escape and Backspace. No global shortcuts.
		setupNavigation := s.setup && (in.Code >= 123 && in.Code <= 126 || in.Code == 116 || in.Code == 121)
		if in.Code < 0 || (in.Code > 53 && !setupNavigation) || !s.surface.Key(in.Code, in.Shift) {
			return reply{Error: "不支持此登录按键"}
		}
	case "scroll":
		s.passwordSelection = passwordSelection{}
		if !s.setup || in.X < 0 || in.Y < 0 || in.X >= 1024 || in.Y >= 640 ||
			in.DeltaX < -512 || in.DeltaX > 512 || in.DeltaY < -512 || in.DeltaY > 512 ||
			!s.surface.Scroll(in.X, in.Y, in.DeltaX, in.DeltaY) {
			return reply{Error: "无效的首次设置滚动操作"}
		}
	default:
		return reply{Error: "未知的登录操作"}
	}
	return reply{}
}
