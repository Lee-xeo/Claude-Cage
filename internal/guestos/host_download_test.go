package guestos

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestMacMediaRejectsUnknownArchitectureBeforeDownloads(t *testing.T) {
	for _, script := range []string{"prepare-macos-media.sh", "prepare-macos-cli-kit.sh"} {
		t.Run(script, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "media")
			cmd := exec.CommandContext(t.Context(), "bash", filepath.Join("..", "..", "scripts", "guest-os", script), output)
			cmd.Env = append(os.Environ(), "CAGE_MACOS_GUEST_ARCH=unsupported-architecture")
			data, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(data), "Unsupported macOS guest architecture") {
				t.Fatalf("unknown architecture was not rejected: %v\n%s", err, data)
			}
			if _, err := os.Lstat(output); !os.IsNotExist(err) {
				t.Fatal("unsupported architecture created media output")
			}
		})
	}
}

// Run both real shell entry points, including concurrent CLI downloads and the
// GitHub asset fallback. A host helper path with spaces must remain one command.
func TestMacMediaUsesHostDownloaderForEveryPackage(t *testing.T) {
	for _, arch := range []string{"arm64", "x86_64"} {
		t.Run(arch, func(t *testing.T) { testMacMediaUsesHostDownloader(t, arch) })
	}
}

func testMacMediaUsesHostDownloader(t *testing.T, arch string) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	dir := t.TempDir()
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("fixture")))
	for _, name := range []string{"prepare-macos-media.sh", "prepare-macos-cli-kit.sh", "macos-guest-arch.sh"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "scripts", "guest-os", name))
		if err != nil {
			t.Fatal(err)
		}
		data = regexp.MustCompile(`[a-f0-9]{64}`).ReplaceAll(data, []byte(digest))
		if err := os.WriteFile(filepath.Join(dir, name), data, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"macos-cli.py", "install-macos-cli.sh", "claude-account.py", "cli_channel.py", "cli_channel.resume.md", "claude_account_resume.md", "client_runtime.py"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	helper := filepath.Join(dir, "Cage helper")
	body := `#!/bin/sh
set -eu
test "$1" = host-download
case "$4" in chrome.dmg|macos-cli-latest|macos-cli-manifest.json) test "$#" = 5;; *) test "$6" = "$FIXTURE_MEDIA/.downloads"; test "$7" = "$FIXTURE_DIGEST";; esac
printf '%s %s %s\n' "$2" "$4" "${5:-}" >> "$FIXTURE_DOWNLOADS"
case "$4" in macos-cli-git.tar.gz) test ! -f "$FIXTURE_MEDIA/fail-git" || exit 1;; esac
case "$2" in https://github.com/*) exit 1;; esac
case "$2" in https://api.github.com/*) test "$5" = application/octet-stream;; esac
case "$4" in
  macos-cli-latest) printf 2.1.322 > "$3";;
  macos-cli-manifest.json) printf '{"version":"2.1.322","platforms":{"darwin-arm64":{"binary":"claude","checksum":"%s"}}}' "$FIXTURE_DIGEST" > "$3";;
  *) printf fixture > "$3";;
esac
printf 'CAGE_HOST_BYTES %s 7 7\n' "$4"
`
	if err := os.WriteFile(helper, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	environment := filepath.Join(dir, "environment.sh")
	if err := os.WriteFile(environment, []byte(`codesign() { :; }
plutil() {
  case "$2" in version) printf 2.1.322;; platforms.darwin-*.binary) printf claude;; platforms.darwin-*.checksum) printf '%s' "$FIXTURE_DIGEST";; *) return 1;; esac
}
`), 0600); err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(dir, "media")
	if err := os.Mkdir(media, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(media, "fail-git")
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	run := func() ([]byte, error) {
		cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(dir, "prepare-macos-media.sh"), media)
		cmd.Env = append(os.Environ(), "BASH_ENV="+environment, "CAGE_MACOS_GUEST_ARCH="+arch, "CAGE_HOST_DOWNLOADER="+helper, "CAGE_REFRESH_TOOLS=1", "FIXTURE_MEDIA="+media, "FIXTURE_DIGEST="+digest, "FIXTURE_DOWNLOADS="+filepath.Join(dir, "requests"))
		return cmd.CombinedOutput()
	}
	output, err := run()
	if err == nil {
		t.Fatal("incomplete CLI kit was published")
	}
	if err = os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	retry, err := run()
	if err != nil {
		t.Fatalf("%v\n%s", err, retry)
	}
	output = append(output, retry...)
	requests, _ := os.ReadFile(filepath.Join(dir, "requests"))
	packageArch, nodeArch, rustArch := "arm64", "arm64", "aarch64"
	if arch == "x86_64" {
		packageArch, nodeArch, rustArch = "amd64", "x64", "x86_64"
	}
	for _, target := range []string{"macos-" + packageArch + ".tar.gz", "darwin-" + nodeArch + ".tar.gz", "/darwin-" + nodeArch + "/claude", "uv-" + rustArch + "-apple-darwin", "macOS_" + packageArch + ".zip"} {
		if !strings.Contains(string(requests), target) {
			t.Fatalf("wrong native package selection for %s: missing %s\n%s", arch, target, requests)
		}
	}
	if strings.Count(string(requests), " macos-cli-claude ") != 1 {
		t.Fatal("completed CLI package was fetched again on retry")
	}
	for _, name := range []string{"editor.tgz", "node.tgz", "chrome.dmg", "macos-cli-claude", "macos-cli-uv.tar.gz", "macos-cli-python.tar.gz", "macos-cli-git.tar.gz"} {
		if !strings.Contains(string(requests), " "+name+" ") || !strings.Contains(string(output), "CAGE_HOST_BYTES "+name+" 7 7") {
			t.Fatalf("component bypassed host download helper: %s\n%s", name, requests)
		}
	}
	for _, name := range []string{"claude", "uv.tar.gz", "python.tar.gz", "git.tar.gz"} {
		data, err := os.ReadFile(filepath.Join(media, "cli", name))
		if err != nil || string(data) != "fixture" {
			t.Fatalf("CLI kit not published: %s %v", name, err)
		}
	}
}
