package workbench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sylvinus/agent-vm/internal/desktop"
	"github.com/sylvinus/agent-vm/internal/hostbridge"
)

func approvalsAnswering(scope desktop.Scope, answered bool, asked *atomic.Int32, shown *string) *hostApprovals {
	approvals := newHostApprovals()
	approvals.ask = func(_ context.Context, _, msg string) (desktop.Scope, bool) {
		asked.Add(1)
		if shown != nil {
			*shown = msg
		}
		return scope, answered
	}
	return approvals
}

func TestHostApprovalOnceIsAskedEveryTime(t *testing.T) {
	var asked atomic.Int32
	var shown string
	approvals := approvalsAnswering(desktop.Once, true, &asked, &shown)
	request := hostbridge.CommandRequest{Command: "make test", Dir: "/p/app/sub", Project: "/p/app"}
	for range 2 {
		if err := approvals.approve(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	if asked.Load() != 2 {
		t.Fatalf("asked %d times", asked.Load())
	}
	for _, want := range []string{"make test", "/p/app/sub", "「app」"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("the dialog does not show %q: %s", want, shown)
		}
	}
}

func TestHostApprovalAlwaysCoversOnlyThatProject(t *testing.T) {
	var asked atomic.Int32
	approvals := approvalsAnswering(desktop.Always, true, &asked, nil)
	first := hostbridge.CommandRequest{Command: "ls", Dir: "/p/a", Project: "/p/a"}
	for range 3 {
		if err := approvals.approve(context.Background(), first); err != nil {
			t.Fatal(err)
		}
	}
	if asked.Load() != 1 {
		t.Fatalf("asked %d times for one project", asked.Load())
	}
	if err := approvals.approve(context.Background(), hostbridge.CommandRequest{Command: "ls", Dir: "/p/b", Project: "/p/b"}); err != nil {
		t.Fatal(err)
	}
	if asked.Load() != 2 {
		t.Fatalf("another project was not asked about: %d", asked.Load())
	}
	if newHostApprovals().alwaysAllowed(hostGrantProject+"/p/a", nil) {
		t.Fatal("an approval was remembered with nowhere to remember it")
	}
}

// "Always" is written down: after a restart the plugin is not asked about
// again, until the user takes the grant back or the plugin declares more.
func TestHostGrantIsRememberedAndTakenBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host-grants.json")
	var asked atomic.Int32
	start := func(scope desktop.Scope, permissions ...string) *hostApprovals {
		approvals := approvalsAnswering(scope, true, &asked, nil)
		approvals.grants = &hostGrants{path: path}
		approvals.permissions = map[string][]string{"clock": permissions}
		return approvals
	}
	call := hostbridge.ToolRequest{Plugin: "clock", Tool: "current_time", Arguments: "{}"}
	if err := start(desktop.Always, "network").approveTool(context.Background(), call); err != nil || asked.Load() != 1 {
		t.Fatalf("asked %d: %v", asked.Load(), err)
	}
	// Started again, answering no: the grant is what lets the call through.
	if err := start(desktop.Deny, "network").approveTool(context.Background(), call); err != nil || asked.Load() != 1 {
		t.Fatalf("asked again after a restart (%d): %v", asked.Load(), err)
	}
	if err := start(desktop.Deny, "network").approveTool(context.Background(), hostbridge.ToolRequest{Plugin: "other", Tool: "x"}); err == nil {
		t.Fatal("the grant covered another plugin")
	}
	if err := start(desktop.Deny, "network", "files").approveTool(context.Background(), call); err == nil {
		t.Fatal("the grant covered a permission declared later")
	}
	// Windows has no permission bits; the profile folder's access list protects the file.
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Fatalf("grants file: %v %v", info, err)
	}
	grants := &hostGrants{path: path}
	if err := grants.revoke(hostGrantPlugin + "clock"); err != nil {
		t.Fatal(err)
	}
	if err := start(desktop.Deny, "network").approveTool(context.Background(), call); err == nil {
		t.Fatal("a grant taken back still let the call through")
	}
	// Commands are remembered for each project; all can be taken back at once.
	command := start(desktop.Always)
	for _, project := range []string{"/p/a", "/p/b"} {
		if err := command.approve(context.Background(), hostbridge.CommandRequest{Command: "ls", Dir: project, Project: project}); err != nil {
			t.Fatal(err)
		}
	}
	grants = &hostGrants{path: path}
	if projects := grants.projects(); len(projects) != 2 || projects[0] != "/p/a" {
		t.Fatalf("projects %v", projects)
	}
	if err := grants.revokeProjects(); err != nil || len(grants.projects()) != 0 {
		t.Fatalf("projects %v: %v", grants.projects(), err)
	}
}

func TestHostApprovalRefusals(t *testing.T) {
	request := hostbridge.CommandRequest{Command: "ls", Dir: "/p/a", Project: "/p/a"}
	var asked atomic.Int32
	if err := approvalsAnswering(desktop.Deny, true, &asked, nil).approve(context.Background(), request); err == nil {
		t.Fatal("a denied command was approved")
	}
	if err := approvalsAnswering(desktop.Deny, false, &asked, nil).approve(context.Background(), request); err == nil || !strings.Contains(err.Error(), "no approval dialog") {
		t.Fatalf("without a dialog: %v", err)
	}
	asked.Store(0)
	approvals := approvalsAnswering(desktop.Always, true, &asked, nil)
	for _, command := range []string{"echo a\x1b[2Kb", "echo a‮b", "echo ​hidden", strings.Repeat("x", hostApprovalCommandMax+1)} {
		if err := approvals.approve(context.Background(), hostbridge.CommandRequest{Command: command, Dir: "/p/a", Project: "/p/a"}); err == nil {
			t.Fatalf("%q was approved", command)
		}
	}
	if asked.Load() != 0 {
		t.Fatal("the user was asked about a command that cannot be shown")
	}
	if err := approvals.approve(context.Background(), hostbridge.CommandRequest{Command: "printf 'a\\n'\n\tls", Dir: "/p/a", Project: "/p/a"}); err != nil {
		t.Fatalf("an ordinary multi-line command was refused: %v", err)
	}
}

func TestHostApprovalShowsOneDialogAtATime(t *testing.T) {
	approvals := newHostApprovals()
	var open, most, asked atomic.Int32
	release := make(chan struct{})
	approvals.ask = func(context.Context, string, string) (desktop.Scope, bool) {
		asked.Add(1)
		if now := open.Add(1); now > most.Load() {
			most.Store(now)
		}
		<-release
		open.Add(-1)
		return desktop.Always, true
	}
	var wait sync.WaitGroup
	for range 5 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_ = approvals.approve(context.Background(), hostbridge.CommandRequest{Command: "ls", Dir: "/p/a", Project: "/p/a"})
		}()
	}
	close(release)
	wait.Wait()
	if most.Load() != 1 || asked.Load() != 1 {
		t.Fatalf("%d dialogs at once, %d in all", most.Load(), asked.Load())
	}
}

func TestHostApprovalForAPluginIsSeparateFromProjects(t *testing.T) {
	var asked atomic.Int32
	var shown string
	approvals := approvalsAnswering(desktop.Always, true, &asked, &shown)
	call := hostbridge.ToolRequest{Plugin: "screen-reader", Tool: "read_window", Arguments: `{"app":"Notes"}`}
	for range 2 {
		if err := approvals.approveTool(context.Background(), call); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"screen-reader", "read_window", `{"app":"Notes"}`} {
		if !strings.Contains(shown, want) {
			t.Fatalf("the dialog does not show %q: %s", want, shown)
		}
	}
	if err := approvals.approve(context.Background(), hostbridge.CommandRequest{Command: "ls", Dir: "/p/screen-reader", Project: "screen-reader"}); err != nil {
		t.Fatal(err)
	}
	if asked.Load() != 2 {
		t.Fatalf("asked %d times; a plugin's approval must not cover a project", asked.Load())
	}
	if err := approvals.approveTool(context.Background(), hostbridge.ToolRequest{Plugin: "other", Tool: "t", Arguments: "{\"a\":\"\u202e\"}"}); err == nil {
		t.Fatal("arguments with invisible characters were approved")
	}
}
