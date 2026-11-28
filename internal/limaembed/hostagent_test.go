package limaembed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/diagnostics"
)

// The copy of limactl's hostagent command differs from it only as its
// header says: after a Lima upgrade, copy it again.
func TestHostagentCopy(t *testing.T) {
	upstream, err := os.ReadFile("../../third_party/lima/cmd/limactl/hostagent.go")
	if err != nil {
		t.Fatal(err)
	}
	ours, err := os.ReadFile("hostagent.go")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(string(upstream), "package main", "package limaembed", 1)
	want = strings.Replace(want, "WrapArgsError(cobra.ExactArgs(1))", "cobra.ExactArgs(1)", 1)
	want = strings.Replace(want, "signalCh := make(chan os.Signal, 1)", "signalCh := hostagentSignals", 1)
	want = strings.Replace(want, "signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM)", "signal.Notify(signalCh, os.Interrupt, syscall.SIGTERM)\n\tstopNotification, err := watchHostagentStop(socket)\n\tif err != nil {\n\t\treturn err\n\t}\n\tdefer stopNotification()", 1)
	// Our header: the comment block after the license lines.
	got := string(ours)
	start := strings.Index(got, "// Copied from")
	end := strings.Index(got, "package limaembed")
	if start < 0 || end < start {
		t.Fatal("hostagent.go lost its header")
	}
	got = got[:start] + got[end:]
	if got != want {
		t.Error("hostagent.go differs from third_party/lima/cmd/limactl/hostagent.go beyond its header: copy it again")
	}
}

func TestHostagentMemoryFailureReachesStartupLog(t *testing.T) {
	profile := t.TempDir()
	var stderr strings.Builder
	log := hostagentDiagnostics(diagnostics.New(profile), &stderr)
	for _, line := range []string{
		`{"level":"info","msg":"starting QEMU"}`,
		`{"level":"debug","msg":"qemu[stderr]: WHPX: initializing partition"}`,
		`{"level":"debug","msg":"qemu[stderr]: qemu-system-x86_64: cannot set up guest memory 'pc.ram': token=private-token"}`,
	} {
		if _, err := log.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	if got := stderr.String(); !strings.Contains(got, "qemu[stderr]: cannot set up guest memory") || strings.Count(got, "\n") != 1 || strings.Contains(got, "private-token") {
		t.Fatalf("incorrect startup log: %q", got)
	}
	events, err := os.ReadFile(filepath.Join(profile, "logs", "events.jsonl"))
	if err != nil || !strings.Contains(string(events), "WHPX: initializing partition") || strings.Contains(string(events), "private-token") {
		t.Fatalf("incorrect diagnostics: %s, %v", events, err)
	}
}
