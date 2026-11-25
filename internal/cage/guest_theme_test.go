package cage

import (
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func findPythonForTest(t *testing.T) string {
	t.Helper()
	names := []string{"python3", "python"}
	if runtime.GOOS == "windows" {
		names = []string{"python", "python3"}
	}
	for _, name := range names {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		if out, err := exec.Command(path, "--version").CombinedOutput(); err == nil && strings.Contains(string(out), "Python 3") {
			return path
		}
	}
	t.Skip("python 3 unavailable")
	return ""
}

func TestGuestThemeScript(t *testing.T) {
	py := findPythonForTest(t)
	cmd := exec.CommandContext(t.Context(), py, "-I", "guest-theme.test.py")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("guest-theme.test.py failed: %v\n%s", err, out)
	}
}

func TestGuestThemeEnvironmentAndInstall(t *testing.T) {
	t.Setenv(GuestThemeEnv, "")
	if script := DefaultConfig().EnvironmentScript(); strings.Contains(script, GuestThemeEnv) {
		t.Fatalf("unexpected %s when unset: %s", GuestThemeEnv, script)
	}
	t.Setenv(GuestThemeEnv, "sepia")
	if script := DefaultConfig().EnvironmentScript(); strings.Contains(script, GuestThemeEnv) {
		t.Fatalf("unexpected %s for invalid theme: %s", GuestThemeEnv, script)
	}
	for _, variant := range []string{"light", "dark"} {
		t.Setenv(GuestThemeEnv, variant)
		script := DefaultConfig().EnvironmentScript()
		want := "export " + GuestThemeEnv + "=" + variant + "\n"
		if !strings.Contains(script, want) {
			t.Fatalf("missing %q in EnvironmentScript: %s", want, script)
		}
	}
	if !strings.Contains(GuestSetup, "/usr/local/bin/cage-apply-theme") {
		t.Fatal("GuestSetup does not install /usr/local/bin/cage-apply-theme")
	}
	if !strings.Contains(DefaultConfig().StartScript(), "/usr/local/bin/cage-apply-theme") {
		t.Fatal("StartScript does not invoke /usr/local/bin/cage-apply-theme")
	}
}
