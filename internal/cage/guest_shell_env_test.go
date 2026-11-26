package cage

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Programs that read a login shell's environment must receive only standard
// settings from the guest profile, whatever the service environment holds.
func TestGuestShellProfileExportsOnlyStandardSettings(t *testing.T) {
	dir := t.TempDir()
	// The file as an existing guest still has it, without a final newline.
	file := writeGuestShellProfile(t, dir, "TZ=Asia/Shanghai\nLANG=zh_CN.UTF-8\nLC_ALL=zh_CN.UTF-8\nCAGE_LANGUAGE=zh-CN\nBROWSER=/usr/local/bin/cage-open\nPULSE_SERVER=unix:/run/cage-audio/native")
	cases := []struct {
		name    string
		env     []string
		browser string
		pulse   string
	}{
		{"service values", []string{"CAGE_LANGUAGE=en-US", "BROWSER=/usr/local/bin/cage-open", "PULSE_SERVER=unix:/run/cage-audio/native"}, "/usr/local/bin/xdg-open", ""},
		{"empty", nil, "/usr/local/bin/xdg-open", ""},
		{"launcher values", []string{"BROWSER=/tmp/runtime-1/opener.py", "PULSE_SERVER=unix:/tmp/runtime-1/paths/audio"}, "/tmp/runtime-1/opener.py", "unix:/tmp/runtime-1/paths/audio"},
	}
	for _, shell := range []string{"sh", "bash", "zsh"} {
		if _, err := exec.LookPath(shell); err != nil {
			t.Logf("%s unavailable", shell)
			continue
		}
		for _, c := range cases {
			t.Run(shell+"/"+c.name, func(t *testing.T) {
				// Inherited values are exported by the command itself: a zsh
				// reads the system zshenv before the command runs.
				inherited := "export https_proxy=http://127.0.0.1:1"
				for _, value := range c.env {
					inherited += " '" + value + "'"
				}
				cmd := exec.Command(shell, "-c", inherited+`; . "$1"; env`, shell, file)
				cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir}
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("%v: %s", err, out)
				}
				got := map[string]string{}
				for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
					key, value, _ := strings.Cut(line, "=")
					got[key] = value
				}
				want := map[string]string{"TZ": "Asia/Shanghai", "LANG": "zh_CN.UTF-8", "LC_ALL": "zh_CN.UTF-8", "BROWSER": c.browser, "PULSE_SERVER": c.pulse}
				for key, value := range want {
					if got[key] != value {
						t.Errorf("%s=%q, want %q", key, got[key], value)
					}
				}
				// The account name and working directories are not set by the profile.
				var names []string
				for key, value := range got {
					if key == "HOME" || key == "PWD" || key == "OLDPWD" || key == "LOGNAME" || key == "USER" {
						continue
					}
					if strings.HasPrefix(strings.ToUpper(key), "CAGE") || strings.HasPrefix(key, "env_") || strings.Contains(strings.ToLower(value), "cage") || strings.Contains(strings.ToLower(key), "proxy") {
						names = append(names, key)
					}
				}
				sort.Strings(names)
				if len(names) > 0 {
					t.Errorf("shell environment keeps %v: %s", names, out)
				}
			})
		}
	}
}

// An account command exports its node timezone and then starts a login shell;
// that shell must keep the node timezone, while every other shell follows the
// environment's timezone even when it inherited a different TZ.
func TestGuestShellProfileKeepsAccountTimezone(t *testing.T) {
	dir := t.TempDir()
	// POSIX zone strings need no tzdata on the test host.
	file := writeGuestShellProfile(t, dir, "TZ=UTC0\nLANG=C\nLC_ALL=C\n")
	if err := os.WriteFile(filepath.Join(dir, ".bash_profile"), []byte(". '"+file+"'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	report := `printf '%s|%s|%s\n' "${TZ-}" "$(date +%z)" "${CAGE_ACCOUNT_TZ-}"`
	cases := []struct {
		name      string
		inherited []string
		want      string
	}{
		{"account node", []string{"TZ=JST-9", "CAGE_ACCOUNT_TZ=JST-9"}, "JST-9|+0900|JST-9"},
		{"plain shell", nil, "UTC0|+0000|"},
		{"stale inherited", []string{"TZ=JST-9"}, "UTC0|+0000|"},
	}
	for _, shell := range []string{"sh", "bash", "zsh"} {
		if _, err := exec.LookPath(shell); err != nil {
			t.Logf("%s unavailable", shell)
			continue
		}
		modes := [][]string{{"-c"}}
		if shell == "bash" {
			// The terminal, shared CLI and mobile runner start these after the
			// account wrapper; HOME's .bash_profile stands in for /etc/profile.
			modes = append(modes, []string{"-lc"}, []string{"-lic"})
		}
		for _, mode := range modes {
			for _, c := range cases {
				t.Run(shell+mode[0]+"/"+c.name, func(t *testing.T) {
					env := []string{"PATH=/usr/bin:/bin", "HOME=" + dir}
					var cmd *exec.Cmd
					if mode[0] == "-c" {
						// A zsh reads the system zshenv before the command runs, so
						// the command exports the inherited values itself.
						inherited := ":"
						if len(c.inherited) > 0 {
							inherited = "export '" + strings.Join(c.inherited, "' '") + "'"
						}
						cmd = exec.Command(shell, "-c", inherited+`; . "$1"; `+report, shell, file)
					} else {
						env = append(env, c.inherited...)
						cmd = exec.Command(shell, mode[0], report)
					}
					cmd.Dir, cmd.Env = dir, env
					// An interactive shell without a terminal warns on stderr.
					out, err := cmd.Output()
					if err != nil {
						t.Fatalf("%v: %s", err, out)
					}
					lines := strings.Split(strings.TrimSpace(string(out)), "\n")
					if got := lines[len(lines)-1]; got != c.want {
						t.Errorf("TZ|offset|CAGE_ACCOUNT_TZ = %q, want %q", got, c.want)
					}
				})
			}
		}
	}
}

func writeGuestShellProfile(t *testing.T, dir, environment string) string {
	t.Helper()
	_, profile, ok := strings.Cut(guestServices, "sudo tee /etc/profile.d/claude-cage.sh >/dev/null <<'SCRIPT'\n")
	if !ok {
		t.Fatal("missing shell profile")
	}
	profile, _, _ = strings.Cut(profile, "\nSCRIPT\n")
	path := filepath.Join(dir, "environment")
	if err := os.WriteFile(path, []byte(environment), 0600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "profile.sh")
	if err := os.WriteFile(file, []byte(strings.ReplaceAll(profile, "/etc/claude-cage/environment", path)), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}
