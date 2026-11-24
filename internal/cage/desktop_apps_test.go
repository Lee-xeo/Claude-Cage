package cage

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A browser link of a scheme a desktop application registered goes to the
// application's opener on standard input; any other scheme keeps going to
// the file broker as before.
func TestGuestOpenHandsAppSchemesToTheirApplication(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0755); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(dir, "schemes")
	if err := os.WriteFile(registry, []byte("codex 0123456789abcdef01234567\n"), 0644); err != nil {
		t.Fatal(err)
	}
	opener := filepath.Join(dir, "opener")
	files := filepath.Join(dir, "files")
	for path, body := range map[string]string{
		opener:                    "#!/bin/sh\nprintf 'app %s %s\\n' \"$*\" \"$(cat)\"",
		files:                     "#!/bin/sh\nprintf 'files %s\\n' \"$*\"",
		filepath.Join(bin, "sudo"): "#!/bin/sh\nshift 4\nexec \"$@\"",
		filepath.Join(bin, "id"):   "#!/bin/sh\nprintf '%s' \"$TEST_IDENTITY\"",
	} {
		if err := os.WriteFile(path, []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	script := strings.NewReplacer("/usr/local/lib/desktop-apps/schemes", registry, "/usr/local/bin/desktop-app-open", opener,
		"/usr/local/bin/cage-desktop-files", files).Replace(guestOpen)
	for _, identity := range []string{"cage", "cage-browser"} {
		for link, want := range map[string]string{
			"codex://auth/callback?code=private-fixture": "app --stdin codex://auth/callback?code=private-fixture\n",
			"other://auth":                    "files --open other://auth\n",
			"file:///home/user/a.txt":         "files --open file:///home/user/a.txt\n",
			"codex+x://auth":                  "files --open codex+x://auth\n",
		} {
			cmd := exec.Command("sh", "-c", script, "open", link)
			cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin", "TEST_IDENTITY="+identity)
			out, err := cmd.CombinedOutput()
			if err != nil || string(out) != want {
				t.Fatalf("%s as %s: %q %v", link, identity, out, err)
			}
		}
	}
}

func TestDesktopAppSupportScriptInstallsTheOpener(t *testing.T) {
	script := DesktopAppSupportScript()
	for _, want := range []string{"/usr/local/bin/desktop-app-open", "NOPASSWD: /usr/local/bin/desktop-app-open --stdin", "/usr/local/bin/cage-open", "/usr/local/bin/cage-desktop-files"} {
		if !strings.Contains(script, want) {
			t.Fatalf("support script lacks %q", want)
		}
	}
	for name, body := range map[string]string{"support": script, "opener": desktopAppOpen} {
		cmd := exec.Command("bash", "-n")
		cmd.Stdin = strings.NewReader(body)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v %s", name, err, out)
		}
	}
}
