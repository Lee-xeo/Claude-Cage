package desktop

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/sylvinus/agent-vm/internal/host"
)

// Scope is how far the user's answer to AskScope reaches.
type Scope int

const (
	Deny Scope = iota
	Once
	Always
)

// The button labels. Always, which the caller remembers until the user
// takes it back, is not offered on Windows, whose message box
// has fixed labels.
const (
	denyLabel   = "拒绝"
	onceLabel   = "允许一次"
	alwaysLabel = "始终允许"
)

// AskScope shows a dialog offering Deny (the default), Once and Always,
// until ctx ends (a refusal). ok is false when no dialog could be shown: the
// scope is then Deny and nobody answered. Text always goes as arguments or
// environment, never as script.
func AskScope(ctx context.Context, title, msg string) (scope Scope, ok bool) {
	switch runtime.GOOS {
	case "darwin":
		res := run(ctx, "osascript", "-e", "on run argv",
			"-e", `display dialog (item 2 of argv) with title (item 1 of argv) buttons {"`+denyLabel+`", "`+alwaysLabel+`", "`+onceLabel+`"} default button "`+denyLabel+`" with icon caution`,
			"-e", "end run", title, msg)
		return macScope(res)
	case "linux":
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return Deny, false
		}
		if _, err := exec.LookPath("zenity"); err == nil {
			res := run(ctx, "zenity", "--question", "--title", title, "--text", msg,
				"--ok-label="+onceLabel, "--cancel-label="+denyLabel, "--extra-button="+alwaysLabel, "--default-cancel", "--no-markup")
			if scope, ok := zenityScope(res); ok {
				return scope, true
			}
		}
		if _, err := exec.LookPath("kdialog"); err == nil {
			res := run(ctx, "kdialog", "--title", title, "--warningyesnocancel", msg,
				"--yes-label", onceLabel, "--no-label", alwaysLabel, "--cancel-label", denyLabel)
			if scope, ok := kdialogScope(res); ok {
				return scope, true
			}
		}
	case "windows":
		cmd := exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", windowsAskScript)
		cmd.Env = append(os.Environ(), "ASK_TITLE="+title, "ASK_TEXT="+msg)
		host.HideCommandWindow(cmd)
		out, err := cmd.Output()
		return windowsScope(strings.TrimSpace(string(out)), err, ctx.Err() != nil)
	}
	return Deny, false
}

// The second button is the default, so Enter refuses.
const windowsAskScript = `Add-Type -AssemblyName System.Windows.Forms; [System.Windows.Forms.MessageBox]::Show($env:ASK_TEXT, $env:ASK_TITLE, 'YesNo', 'Warning', 'Button2', 'DefaultDesktopOnly')`

func macScope(res result) (Scope, bool) {
	switch {
	case res.err == nil:
		switch {
		case strings.Contains(res.stdout, "button returned:"+onceLabel):
			return Once, true
		case strings.Contains(res.stdout, "button returned:"+alwaysLabel):
			return Always, true
		}
		return Deny, true
	case res.timedOut:
		return Deny, true
	}
	// Closing the dialog is "User canceled. (-128)". Without a graphical
	// session osascript fails with another error.
	return Deny, strings.Contains(res.stderr, "(-128)")
}

// zenity prints the extra button's label and exits 1 when it is chosen; Deny
// and closing the dialog exit 1 with no output.
func zenityScope(res result) (Scope, bool) {
	if res.err == nil {
		return Once, true
	}
	if res.timedOut {
		return Deny, true
	}
	if res.code != 1 {
		return Deny, false
	}
	stderr := strings.ToLower(res.stderr)
	for _, s := range []string{"cannot open display", "failed to open display", "unable to init server"} {
		if strings.Contains(stderr, s) {
			return Deny, false
		}
	}
	if strings.TrimSpace(res.stdout) == alwaysLabel {
		return Always, true
	}
	return Deny, true
}

// kdialog exits 0 for yes, 1 for no and 2 for cancel.
func kdialogScope(res result) (Scope, bool) {
	if res.err == nil {
		return Once, true
	}
	if res.timedOut {
		return Deny, true
	}
	stderr := strings.ToLower(res.stderr)
	for _, s := range []string{"could not connect to display", "cannot connect to x server", "no qt platform plugin"} {
		if strings.Contains(stderr, s) {
			return Deny, false
		}
	}
	switch res.code {
	case 1:
		return Always, true
	case 2:
		return Deny, true
	}
	return Deny, false
}

func windowsScope(output string, err error, ended bool) (Scope, bool) {
	switch {
	case ended:
		return Deny, true
	case err != nil:
		return Deny, false
	case output == "Yes":
		return Once, true
	case output == "No":
		return Deny, true
	}
	return Deny, false
}
