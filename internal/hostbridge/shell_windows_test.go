//go:build windows

package hostbridge

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestResolveHostCommandSkipsWindowsAppsStubForPython3(t *testing.T) {
	base := t.TempDir()
	appsDir := filepath.Join(base, "Microsoft", "WindowsApps")
	realDir := filepath.Join(base, "Python313")
	for _, dir := range []string{appsDir, realDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub := filepath.Join(appsDir, "python3.exe")
	realPython := filepath.Join(realDir, "python.exe")
	for _, file := range []string{stub, realPython} {
		if err := os.WriteFile(file, []byte{}, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	orig := verifyWindowsPython
	t.Cleanup(func() { verifyWindowsPython = orig })
	var probed []string
	verifyWindowsPython = func(_ context.Context, program string, prefix []string, _ []string, requirePython3 bool) bool {
		probed = append(probed, program)
		if !requirePython3 {
			t.Errorf("expected requirePython3=true")
		}
		return strings.EqualFold(program, realPython) && len(prefix) == 0
	}

	env := []string{"PATH=" + appsDir + ";" + realDir, "WINDIR=" + filepath.Join(base, "empty-win")}
	got, prefix, err := resolveHostCommand(context.Background(), "python3", env)
	if err != nil {
		t.Fatalf("resolveHostCommand failed: %v", err)
	}
	if !strings.EqualFold(got, realPython) || len(prefix) != 0 {
		t.Fatalf("got (%q, %v), want (%q, nil)", got, prefix, realPython)
	}
	if len(probed) == 0 || !strings.EqualFold(probed[0], realPython) {
		t.Fatalf("expected non-WindowsApps python.exe to be probed before WindowsApps stub, probed=%v", probed)
	}
}

func TestResolveHostCommandFallsBackToPyLauncher(t *testing.T) {
	base := t.TempDir()
	appsDir := filepath.Join(base, "Microsoft", "WindowsApps")
	winDir := filepath.Join(base, "Windows")
	for _, dir := range []string{appsDir, winDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(appsDir, "python3.exe"), []byte{}, 0o755); err != nil {
		t.Fatal(err)
	}
	pyLauncher := filepath.Join(winDir, "py.exe")
	if err := os.WriteFile(pyLauncher, []byte{}, 0o755); err != nil {
		t.Fatal(err)
	}

	orig := verifyWindowsPython
	t.Cleanup(func() { verifyWindowsPython = orig })
	verifyWindowsPython = func(_ context.Context, program string, prefix []string, _ []string, requirePython3 bool) bool {
		return requirePython3 && strings.EqualFold(program, pyLauncher) && slices.Equal(prefix, []string{"-3"})
	}

	env := []string{"PATH=" + appsDir, "WINDIR=" + winDir}
	got, prefix, err := resolveHostCommand(context.Background(), "python3", env)
	if err != nil {
		t.Fatalf("resolveHostCommand failed: %v", err)
	}
	if !strings.EqualFold(got, pyLauncher) || !slices.Equal(prefix, []string{"-3"}) {
		t.Fatalf("got (%q, %v), want (%q, [-3])", got, prefix, pyLauncher)
	}
}

func TestResolveHostCommandReturnsNotFoundWhenOnlyBrokenStubExists(t *testing.T) {
	base := t.TempDir()
	appsDir := filepath.Join(base, "Microsoft", "WindowsApps")
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appsDir, "python3.exe"), []byte{}, 0o755); err != nil {
		t.Fatal(err)
	}

	orig := verifyWindowsPython
	t.Cleanup(func() { verifyWindowsPython = orig })
	verifyWindowsPython = func(context.Context, string, []string, []string, bool) bool { return false }

	env := []string{"PATH=" + appsDir, "WINDIR=" + filepath.Join(base, "empty-win")}
	_, _, err := resolveHostCommand(context.Background(), "python3", env)
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("got err=%v, want exec.ErrNotFound", err)
	}
}

func TestResolveHostCommandAcceptsWorkingStorePython(t *testing.T) {
	base := t.TempDir()
	appsDir := filepath.Join(base, "Microsoft", "WindowsApps")
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	storePython := filepath.Join(appsDir, "python3.exe")
	if err := os.WriteFile(storePython, []byte{}, 0o755); err != nil {
		t.Fatal(err)
	}

	orig := verifyWindowsPython
	t.Cleanup(func() { verifyWindowsPython = orig })
	verifyWindowsPython = func(_ context.Context, program string, prefix []string, _ []string, _ bool) bool {
		return strings.EqualFold(program, storePython) && len(prefix) == 0
	}

	env := []string{"PATH=" + appsDir, "WINDIR=" + filepath.Join(base, "empty-win")}
	got, prefix, err := resolveHostCommand(context.Background(), "python3", env)
	if err != nil {
		t.Fatalf("resolveHostCommand failed: %v", err)
	}
	if !strings.EqualFold(got, storePython) || len(prefix) != 0 {
		t.Fatalf("got (%q, %v), want (%q, nil)", got, prefix, storePython)
	}
}

func TestExternalUseCageStartsWhenWindowsAppsPython3StubPrecedesRealPython(t *testing.T) {
	// Only run this live probe when a real python.exe exists outside WindowsApps.
	var realDir string
	for _, dir := range windowsPathDirs(os.Environ()) {
		if isWindowsAppsDir(dir) {
			continue
		}
		candidate := filepath.Join(dir, "python.exe")
		if windowsExecutableExists(candidate) && runWindowsPythonProbe(context.Background(), candidate, nil, os.Environ(), true) {
			realDir = dir
			break
		}
	}
	if realDir == "" {
		t.Skip("no real non-WindowsApps python.exe on PATH")
	}

	// Deploy the actual preinstalled use-cage plugin into a temp directory.
	pluginDir := filepath.Join(t.TempDir(), "use-cage")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := defaultPluginFiles.ReadDir("defaults/use-cage")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := defaultPluginFiles.ReadFile("defaults/use-cage/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pluginDir, entry.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := LoadManifest(pluginDir)
	if err != nil {
		t.Fatal(err)
	}
	appsDir := filepath.Join(t.TempDir(), "Microsoft", "WindowsApps")
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Put a broken stub python3.exe in WindowsApps ahead of real python.exe on PATH.
	if err := os.WriteFile(filepath.Join(appsDir, "python3.exe"), []byte("not-a-valid-pe"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", appsDir+";"+realDir)

	ext := &External{Dir: pluginDir, Manifest: manifest}
	if err := ext.Start(context.Background()); err != nil {
		t.Fatalf("use-cage External.Start failed with WindowsApps python3 stub ahead of real python.exe: %v", err)
	}
	t.Cleanup(ext.Close)
	if len(ext.Tools()) == 0 {
		t.Fatal("use-cage started with zero tools")
	}
}
