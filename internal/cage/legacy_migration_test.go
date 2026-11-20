//go:build !windows

package cage

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func python3(t *testing.T) string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	return python
}

func legacySettingsCopy(t *testing.T) string {
	t.Helper()
	const begin, end = "legacy_copy=$(cat <<'LEGACY_COPY'\n", "\nLEGACY_COPY\n"
	start := strings.Index(guestMigrate, begin)
	stop := strings.Index(guestMigrate, end)
	if start < 0 || stop < start {
		t.Fatal("settings copy not found in the migration")
	}
	return guestMigrate[start+len(begin) : stop]
}

func TestLegacySettingsAreCopiedOnceAndKept(t *testing.T) {
	python := python3(t)
	program := legacySettingsCopy(t)
	root := t.TempDir()
	former, current := filepath.Join(root, "claude-cage"), filepath.Join(root, "workbench")
	files := map[string]struct {
		data string
		mode os.FileMode
	}{
		"browser-control.json": {`{"token":"t"}`, 0o640},
		"browser-password":     {"secret\n", 0o600},
		"code-server.yaml":     {"password: p\n", 0o600},
		"display-scale":        {"2", 0o644},
		"components.json":      {`{"revision":"old"}`, 0o644},
	}
	if err := os.Mkdir(former, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, file := range files {
		path := filepath.Join(former, name)
		if err := os.WriteFile(path, []byte(file.data), file.mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, file.mode); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(former, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(former, "folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	// A setting the current version already wrote is never replaced.
	if err := os.Mkdir(current, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(current, "display-scale"), []byte("1.5"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Left behind by an attempt that was stopped before the copy took its name.
	if err := os.WriteFile(filepath.Join(current, ".browser-password.copy"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	formerBefore := snapshotFiles(t, former)
	run := func() {
		t.Helper()
		if output, err := exec.Command(python, "-I", "-c", program, former, current, "700").CombinedOutput(); err != nil {
			t.Fatalf("copy: %v\n%s", err, output)
		}
	}
	run()
	for name, file := range files {
		data, err := os.ReadFile(filepath.Join(current, name))
		switch name {
		case "components.json":
			if !os.IsNotExist(err) {
				t.Fatalf("the component manifest was copied: %v", err)
			}
			continue
		case "display-scale":
			if string(data) != "1.5" {
				t.Fatalf("an existing setting was replaced: %q", data)
			}
			continue
		}
		if err != nil || string(data) != file.data {
			t.Fatalf("%s: %q %v", name, data, err)
		}
		info, _ := os.Stat(filepath.Join(current, name))
		if info.Mode().Perm() != file.mode {
			t.Fatalf("%s mode %v, want %v", name, info.Mode().Perm(), file.mode)
		}
	}
	for _, name := range []string{"linked", "folder", ".browser-password.copy"} {
		if _, err := os.Lstat(filepath.Join(current, name)); !os.IsNotExist(err) {
			t.Fatalf("%s present in the current directory: %v", name, err)
		}
	}
	if !bytes.Equal(snapshotFiles(t, former), formerBefore) {
		t.Fatal("the former directory changed")
	}
	// Later changes in the current directory are never undone from the
	// former one.
	if err := os.Remove(filepath.Join(current, "browser-password")); err != nil {
		t.Fatal(err)
	}
	run()
	if _, err := os.Stat(filepath.Join(current, "browser-password")); !os.IsNotExist(err) {
		t.Fatalf("a removed setting came back: %v", err)
	}
}

func TestLegacySettingsCopySurvivesMidMigrationInterruptionAndVerifyFailure(t *testing.T) {
	python := python3(t)
	program := legacySettingsCopy(t)
	root := t.TempDir()
	former, current := filepath.Join(root, "claude-cage"), filepath.Join(root, "workbench")
	if err := os.MkdirAll(former, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(former, "alpha"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(former, "beta"), []byte("two\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	formerBefore := snapshotFiles(t, former)

	// Simulate an interrupted run where alpha was copied, beta left a partial
	// staged file, and the completion marker was not yet written.
	if err := os.MkdirAll(current, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(current, "alpha"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(current, ".beta.copy"), []byte("truncated"), 0o640); err != nil {
		t.Fatal(err)
	}

	// Verification failure aborts before replacing beta, removes staged copy,
	// leaves former intact, and does not write .settings-copied.
	failing := strings.Replace(program, "filecmp.cmp(source, staged, shallow=False)", "False", 1)
	if out, err := exec.Command(python, "-I", "-c", failing, former, current, "700").CombinedOutput(); err == nil {
		t.Fatalf("expected verification failure, succeeded: %s", out)
	}
	if _, err := os.Lstat(filepath.Join(current, ".beta.copy")); !os.IsNotExist(err) {
		t.Fatalf("failed staged copy was not cleaned up: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(current, ".settings-copied")); !os.IsNotExist(err) {
		t.Fatalf("marker written despite verification failure: %v", err)
	}
	if !bytes.Equal(snapshotFiles(t, former), formerBefore) {
		t.Fatal("former directory changed on failed verification")
	}

	// Re-entry completes beta byte-exact and records completion.
	if out, err := exec.Command(python, "-I", "-c", program, former, current, "700").CombinedOutput(); err != nil {
		t.Fatalf("retry copy: %v\n%s", err, out)
	}
	for name, want := range map[string]string{"alpha": "one\n", "beta": "two\n"} {
		got, err := os.ReadFile(filepath.Join(current, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(current, ".settings-copied")); err != nil {
		t.Fatalf("completion marker missing after retry: %v", err)
	}
	if !bytes.Equal(snapshotFiles(t, former), formerBefore) {
		t.Fatal("former directory changed after retry")
	}
}

func TestLegacyBashrcPromptRewriteIsAtomicAndIdempotent(t *testing.T) {
	python := python3(t)
	const begin, end = "python3 - <<'LEGACY_PROMPT'\n", "\nLEGACY_PROMPT\n"
	start := strings.Index(guestMigrate, begin)
	stop := strings.Index(guestMigrate, end)
	if start < 0 || stop < start {
		t.Fatal("LEGACY_PROMPT not found in guest-migrate.sh")
	}
	program := guestMigrate[start+len(begin) : stop]
	home := t.TempDir()
	bashrc := filepath.Join(home, ".bashrc")
	original := "# user config\nexport FOO=bar\n# CAGE_WORKBENCH_PROMPT\nif [ \"${CAGE_WORKBENCH:-}\" = 1 ]; then\n  PS1='x'\nfi\n"
	if err := os.WriteFile(bashrc, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bashrc, 0o640); err != nil {
		t.Fatal(err)
	}
	run := func() {
		t.Helper()
		cmd := exec.Command(python, "-I", "-c", program)
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("prompt rewrite: %v\n%s", err, out)
		}
	}
	run()
	after, err := os.ReadFile(bashrc)
	if err != nil {
		t.Fatal(err)
	}
	want := "# user config\nexport FOO=bar\n# WORKBENCH_TERMINAL_PROMPT\nif [ \"${WORKBENCH_TERMINAL:-}\" = 1 ]; then\n  PS1='x'\nfi\n"
	if string(after) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", after, want)
	}
	info, err := os.Stat(bashrc)
	if err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("mode %v %v, want 0640", info.Mode().Perm(), err)
	}
	run()
	afterAgain, _ := os.ReadFile(bashrc)
	if string(afterAgain) != want {
		t.Fatalf("not idempotent:\n%s", afterAgain)
	}
}

func snapshotFiles(t *testing.T, root string) []byte {
	t.Helper()
	var out bytes.Buffer
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		out.WriteString(path + " " + info.Mode().String() + " " + info.ModTime().String() + "\n")
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out.Write(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
