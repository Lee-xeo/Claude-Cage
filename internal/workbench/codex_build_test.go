package workbench

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"al.essio.dev/pkg/shellescape"
)

// Exercise the actual app-server shell environment as well as its filesystem
// profile. A version-only probe misses module caches and compiler subprocesses.
func TestLocalCodexMacBuildTools(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Getenv("CAGE_TEST_LOCAL_CODEX") != "1" {
		t.Skip("requires local signed-in Codex and the macOS build toolchains")
	}
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "private-test-data")
	if err = os.WriteFile(outside, []byte("test marker"), 0600); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":        "module local.test/build\n\ngo 1.23\n",
		"build_test.go": "package build\nimport \"testing\"\nfunc TestBuild(t *testing.T) {}\n",
		"package.json":  `{"scripts":{"test":"node -e \"console.log(123)\""}}`,
		"main.swift":    "import Foundation\nprint(843)\n",
		"check.sh": `set -eu
printf "%s\n" "tmp=$TMPDIR" "go=$(command -v go)" "node=$(command -v node)" "mktemp=$(command -v mktemp)" > build-env.txt
out=$(mktemp -d)
go test ./...
test "$(npm test --silent)" = 123
xcrun swiftc main.swift -o "$out/check"
test "$("$out/check")" = 843
if /bin/cat ` + shellescape.Quote(outside) + ` >/dev/null 2>&1; then exit 7; fi
printf OK > "$TMPDIR/build-proof"
`,
	}
	for name, body := range files {
		if err = os.WriteFile(filepath.Join(project, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	session, err := startCodexConversation(ctx, localCodex(), project)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	response, err := session.ask("In the assigned project, run /bin/sh check.sh once. Do not edit any files or substitute commands. Report the exit status and the first error if it fails.")
	if err != nil {
		t.Fatal(err)
	}
	proof, err := os.ReadFile(filepath.Join(session.dir, "build", "tmp", "build-proof"))
	if err != nil || string(proof) != "OK" {
		env, _ := os.ReadFile(filepath.Join(project, "build-env.txt"))
		t.Fatalf("build script did not complete: %s\n%s", response, env)
	}
}
