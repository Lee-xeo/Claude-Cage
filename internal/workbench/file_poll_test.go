package workbench

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/paths"
)

func TestDesktopFileRequestsReportTransportFailureAsUnavailable(t *testing.T) {
	if _, err := exec.LookPath(sshExecutable()); err != nil {
		t.Skip("SSH unavailable")
	}
	s := testServer(t)
	s.ready = true
	// This isolated profile has no SSH config. The shipped SSH entry fails
	// locally, without connecting to any running VM or host service.
	w := request(s, "GET", "/api/input/files/desktop/requests", "", "", true)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("transport failure became bad user input: %d %s", w.Code, w.Body.String())
	}
}

func TestDesktopFilePollDistinguishesUnopenedAndBrokenDesktop(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	fixture := `systemctl() {
  printf '%s\n' "$DESKTOP_STATE"
  [ "$DESKTOP_STATE" = active ]
}
`
	for _, state := range []string{"inactive", "activating", "active", "failed", ""} {
		t.Run(state, func(t *testing.T) {
			// The final command stands for contacting the broker. It must only
			// run for an active service; a failed service cannot become empty success.
			script := "DESKTOP_STATE='" + state + "'\n" + fixture + desktopFilesPoll + "printf broker-contacted"
			cmd := exec.Command(bash, "--noprofile", "--norc", "-s")
			cmd.Stdin = strings.NewReader(script)
			out, err := cmd.CombinedOutput()
			switch state {
			case "inactive", "activating":
				var result map[string]any
				if err != nil || json.Unmarshal(out, &result) != nil || result["available"] != false {
					t.Fatalf("unopened desktop became error: %s %v", out, err)
				}
			case "active":
				if err != nil || string(out) != "broker-contacted" {
					t.Fatalf("live broker not polled: %s %v", out, err)
				}
			default:
				if err == nil || strings.Contains(string(out), "broker-contacted") {
					t.Fatalf("broken service hidden: %s %v", out, err)
				}
			}
		})
	}
}

func TestDesktopFilesPollAllMergesDefaultAndAccountDesktops(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX Unix-socket poll script runs in Linux guest")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	// Keep Unix domain socket paths well below macOS's 104-byte sockaddr_un limit.
	runRoot, err := os.MkdirTemp("/tmp", "cp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runRoot) })
	accountID := strings.Repeat("a", 24)
	defaultDir := filepath.Join(runRoot, "cage-desktop")
	accountDir := filepath.Join(runRoot, "desktop-"+accountID)
	if err := os.MkdirAll(defaultDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(accountDir, 0700); err != nil {
		t.Fatal(err)
	}
	// Create real Unix socket files so `[ -S "$sock" ]` passes.
	l1, err := net.Listen("unix", filepath.Join(defaultDir, "files.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l1.Close()
	l2, err := net.Listen("unix", filepath.Join(accountDir, "files.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()

	const defaultUploadID = "11111111-1111-4111-8111-111111111111"
	const accountUploadID = "22222222-2222-4222-8222-222222222222"
	const accountOpenID = "33333333-3333-4333-8333-333333333333"

	fixture := `systemctl() {
  case "$2" in
    cage-claude-desktop.service) printf '%s\n' "$DEFAULT_STATE"; [ "$DEFAULT_STATE" = active ] ;;
    desktop-` + accountID + `.service) printf '%s\n' "$ACCOUNT_STATE"; [ "$ACCOUNT_STATE" = active ] ;;
    *) printf 'inactive\n'; return 3 ;;
  esac
}
curl() {
  sock=""
  while [ "$#" -gt 0 ]; do
    if [ "$1" = "--unix-socket" ]; then sock="$2"; shift 2; continue; fi
    shift
  done
  case "$sock" in
    */cage-desktop/files.sock)
      printf '{"available":true,"uploads":[{"id":"` + defaultUploadID + `","target":"desktop"}],"downloads":[],"opens":[]}\nCAGE_FILE_HTTP:200' ;;
    */desktop-` + accountID + `/files.sock)
      printf '{"available":true,"uploads":[{"id":"` + accountUploadID + `","target":"desktop","directory":true}],"downloads":[],"opens":[{"id":"` + accountOpenID + `"}]}\nCAGE_FILE_HTTP:200' ;;
    *)
      return 7 ;;
  esac
}
`
	script := strings.ReplaceAll(desktopFilesPollAllScript(""), "/run/", runRoot+"/")

	// Case 1: default inactive, account desktop active -> account uploads/opens still polled!
	cmd := exec.Command(bash, "--noprofile", "--norc", "-s")
	cmd.Stdin = strings.NewReader("DEFAULT_STATE=inactive\nACCOUNT_STATE=active\n" + fixture + script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("account-only poll failed: %v\n%s", err, out)
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out)
	}
	s := testServer(t)
	merged := s.mergeDesktopFilePoll(raw)
	if merged["available"] != true {
		t.Fatalf("expected available=true, got %#v", merged)
	}
	if got := s.desktopFileOwner(accountUploadID); got != accountID {
		t.Fatalf("expected owner %q, got %q", accountID, got)
	}
	if got := s.desktopFileOwner(accountOpenID); got != accountID {
		t.Fatalf("expected open owner %q, got %q", accountID, got)
	}

	// Case 2: both active -> merged deterministically with owners tracked per socket.
	cmd = exec.Command(bash, "--noprofile", "--norc", "-s")
	cmd.Stdin = strings.NewReader("DEFAULT_STATE=active\nACCOUNT_STATE=active\n" + fixture + script)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dual poll failed: %v\n%s", err, out)
	}
	raw = nil
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("invalid dual json: %v\n%s", err, out)
	}
	merged = s.mergeDesktopFilePoll(raw)
	uploads := merged["uploads"].([]any)
	if len(uploads) != 2 {
		t.Fatalf("expected 2 merged uploads, got %#v", uploads)
	}
	if uploads[0].(map[string]any)["desktop"] != "default" || uploads[1].(map[string]any)["desktop"] != accountID {
		t.Fatalf("unexpected desktop tags: %#v", uploads)
	}
	if got := s.desktopFileOwner(defaultUploadID); got != "" {
		t.Fatalf("expected default owner empty, got %q", got)
	}

	// Case 3: a failed default Desktop does not stop an account Desktop's
	// transfers; with no other source the failure is reported.
	cmd = exec.Command(bash, "--noprofile", "--norc", "-s")
	cmd.Stdin = strings.NewReader("DEFAULT_STATE=failed\nACCOUNT_STATE=active\n" + fixture + script)
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), accountID) || strings.Contains(string(out), `"default"`) {
		t.Fatalf("expected the account Desktop to keep working beside a failed default, got: %s (%v)", out, err)
	}
	cmd = exec.Command(bash, "--noprofile", "--norc", "-s")
	cmd.Stdin = strings.NewReader("DEFAULT_STATE=failed\nACCOUNT_STATE=inactive\n" + fixture + script)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("expected a failed default service alone to error, got: %s", out)
	}
}

func TestMergeDesktopFilePollAndSocketValidation(t *testing.T) {
	accountID := strings.Repeat("a", 24)
	const defaultUploadID = "11111111-1111-4111-8111-111111111111"
	const accountUploadID = "22222222-2222-4222-8222-222222222222"
	const accountOpenID = "33333333-3333-4333-8333-333333333333"

	s := testServer(t)
	raw := map[string]any{
		"sources": map[string]any{
			accountID: map[string]any{
				"available": true,
				"uploads":   []any{map[string]any{"id": accountUploadID, "target": "desktop", "directory": true}},
				"downloads": []any{},
				"opens":     []any{map[string]any{"id": accountOpenID}},
			},
			"default": map[string]any{
				"available": true,
				"uploads":   []any{map[string]any{"id": defaultUploadID, "target": "desktop"}},
				"downloads": []any{},
				"opens":     []any{},
			},
		},
	}
	merged := s.mergeDesktopFilePoll(raw)
	if merged["available"] != true {
		t.Fatalf("expected available=true, got %#v", merged)
	}
	uploads := merged["uploads"].([]any)
	if len(uploads) != 2 {
		t.Fatalf("expected 2 merged uploads, got %#v", uploads)
	}
	if uploads[0].(map[string]any)["desktop"] != "default" || uploads[1].(map[string]any)["desktop"] != accountID {
		t.Fatalf("unexpected desktop tags: %#v", uploads)
	}
	if got := s.desktopFileOwner(defaultUploadID); got != "" {
		t.Fatalf("expected default owner empty, got %q", got)
	}
	if got := s.desktopFileOwner(accountUploadID); got != accountID {
		t.Fatalf("expected owner %q, got %q", accountID, got)
	}
	if got := s.desktopFileOwner(accountOpenID); got != accountID {
		t.Fatalf("expected open owner %q, got %q", accountID, got)
	}

	// Verify desktopFileSocket validation.
	if sock, err := desktopFileSocket(""); err != nil || sock != "/run/cage-desktop/files.sock" {
		t.Fatalf("default socket: %q %v", sock, err)
	}
	if sock, err := desktopFileSocket(accountID); err != nil || sock != "/run/desktop-"+accountID+"/files.sock" {
		t.Fatalf("account socket: %q %v", sock, err)
	}
	if _, err := desktopFileSocket("../escape"); err == nil {
		t.Fatal("invalid desktop id accepted")
	}
}

func TestOpenDownloadedFileAcceptsCaseInsensitiveParentOnNoCaseFS(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Downloads")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "report.txt")
	if err := os.WriteFile(file, []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	// Resolve any /var -> /private/var symlink on macOS first so only case differs.
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	f, err := openDownloadedFile(filepath.Join(realDir, "report.txt"))
	if err != nil {
		t.Fatalf("openDownloadedFile failed on real path: %v", err)
	}
	f.Close()

	// On case-insensitive filesystems (macOS/Windows), differing case in parent path must still succeed.
	if paths.NoCase() {
		altDir := filepath.Join(filepath.Dir(realDir), strings.ToLower(filepath.Base(realDir)))
		f, err := openDownloadedFile(filepath.Join(altDir, "report.txt"))
		if err != nil {
			t.Fatalf("openDownloadedFile rejected case-different parent on NoCase FS: %v", err)
		}
		f.Close()
	}
}
