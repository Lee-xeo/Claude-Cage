package instance

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	hostagentevents "github.com/lima-vm/lima/v2/pkg/hostagent/events"
	"github.com/lima-vm/lima/v2/pkg/limatype"
)

func TestWithIdleDeadlineExtendsOnProgress(t *testing.T) {
	ctx, touch, expired := withIdleDeadline(context.Background(), 80*time.Millisecond)
	for range 5 {
		time.Sleep(40 * time.Millisecond)
		touch(true)
	}
	if ctx.Err() != nil || expired() {
		t.Fatal("progress did not postpone the idle deadline")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("idle deadline never fired")
	}
	if !expired() {
		t.Fatal("idle expiry not reported")
	}
	touch(false)
}

func TestWithIdleDeadlineKeepsParentAndStop(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx, touch, expired := withIdleDeadline(parent, time.Hour)
	cancel()
	<-ctx.Done()
	if expired() {
		t.Fatal("parent cancellation reported as idle")
	}
	touch(false)
	ctx, touch, expired = withIdleDeadline(context.Background(), 0)
	touch(true)
	if ctx.Err() != nil || expired() {
		t.Fatal("zero idle must disable the deadline")
	}
}

func TestWatchHostAgentTimeoutsUseSeparateKeys(t *testing.T) {
	ctx := WithWatchHostAgentTimeout(context.Background(), 2*time.Hour)
	ctx = WithWatchHostAgentIdleTimeout(ctx, 10*time.Minute)
	if got := watchHostAgentTimeout(ctx); got != 2*time.Hour {
		t.Fatalf("overall timeout = %v, want 2h: the idle timeout replaced it", got)
	}
	if got := watchHostAgentIdleTimeout(ctx); got != 10*time.Minute {
		t.Fatalf("idle timeout = %v, want 10m", got)
	}
	ctx = WithWatchHostAgentIdleTimeout(WithWatchHostAgentTimeout(context.Background(), time.Hour), time.Minute)
	ctx = WithWatchHostAgentTimeout(ctx, 3*time.Hour)
	if watchHostAgentIdleTimeout(ctx) != time.Minute || watchHostAgentTimeout(ctx) != 3*time.Hour {
		t.Fatal("setting the overall timeout after the idle one lost a value")
	}
}

// Port forwarding and vsock events repeat the last boot status every few
// seconds; only a change of that status, such as a new cloud-init line, is
// progress that postpones the idle deadline (#632).
func TestBootProgressIgnoresRepeatedStatus(t *testing.T) {
	line := func(text string) *hostagentevents.CloudInitProgress {
		return &hostagentevents.CloudInitProgress{Active: true, LogLine: text}
	}
	running := hostagentevents.Status{Running: true, SSHLocalPort: 60022, CloudInitProgress: line("Setting up git ...")}
	heartbeat := running
	heartbeat.PortForward = &hostagentevents.PortForwardEvent{}
	heartbeat.Vsock = &hostagentevents.VsockEvent{}
	if bootProgress(heartbeat) != bootProgress(running) {
		t.Fatal("a port forwarding heartbeat counted as boot progress")
	}
	next := running
	next.CloudInitProgress = line("Setting up curl ...")
	completed := running
	completed.CloudInitProgress = &hostagentevents.CloudInitProgress{Completed: true, LogLine: "Setting up git ..."}
	for name, status := range map[string]hostagentevents.Status{
		"new cloud-init line":  next,
		"cloud-init completed": completed,
		"degraded":             {Running: true, Degraded: true, SSHLocalPort: 60022, CloudInitProgress: running.CloudInitProgress},
		"exiting":              {Exiting: true, SSHLocalPort: 60022, CloudInitProgress: running.CloudInitProgress},
		"error":                {Running: true, SSHLocalPort: 60022, CloudInitProgress: running.CloudInitProgress, Errors: []string{"failed"}},
		"ssh port":             {SSHLocalPort: 60022},
	} {
		if bootProgress(status) == bootProgress(running) {
			t.Errorf("%s did not count as progress", name)
		}
	}
}

// writeHostAgentEvent appends one event as the hostagent writes ha.stdout.log.
func writeHostAgentEvent(t *testing.T, path string, status hostagentevents.Status) {
	t.Helper()
	data, err := json.Marshal(hostagentevents.Event{Time: time.Now(), Status: status})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
}

// A VM whose cloud-init progress stopped reaching the host keeps sending
// port forwarding events. The wait must still end after the idle timeout
// with an error, instead of lasting the overall timeout or reporting success.
func TestWatchHostAgentEventsStallsDespiteHeartbeats(t *testing.T) {
	dir := t.TempDir()
	stdout, stderr := filepath.Join(dir, "ha.stdout.log"), filepath.Join(dir, "ha.stderr.log")
	begin := time.Now().Add(-time.Second)
	progress := &hostagentevents.CloudInitProgress{Active: true}
	writeHostAgentEvent(t, stdout, hostagentevents.Status{SSHLocalPort: 60022})
	writeHostAgentEvent(t, stdout, hostagentevents.Status{SSHLocalPort: 60022, CloudInitProgress: progress})
	writeHostAgentEvent(t, stdout, hostagentevents.Status{Running: true, SSHLocalPort: 60022, CloudInitProgress: progress})
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(50 * time.Millisecond):
				writeHostAgentEvent(t, stdout, hostagentevents.Status{Running: true, SSHLocalPort: 60022, CloudInitProgress: progress, PortForward: &hostagentevents.PortForwardEvent{}})
			}
		}
	}()
	ctx := WithWatchHostAgentIdleTimeout(WithWatchHostAgentTimeout(context.Background(), 30*time.Second), 400*time.Millisecond)
	inst := &limatype.Instance{Config: &limatype.LimaYAML{}}
	started := time.Now()
	err := watchHostAgentEvents(ctx, inst, stdout, stderr, begin, true)
	if err == nil || !strings.Contains(err.Error(), "no boot progress") {
		t.Fatalf("stalled cloud-init progress ended with %v", err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("heartbeats kept the wait alive for %v", elapsed)
	}
}

// Port forwarding and vsock events repeat the last cloud-init progress. Each
// cloud-init line, and the completion, must be printed once (#828).
func TestWatchHostAgentEventsPrintsCloudInitProgressOnce(t *testing.T) {
	var logs bytes.Buffer
	out, level := logrus.StandardLogger().Out, logrus.GetLevel()
	logrus.SetOutput(&logs)
	logrus.SetLevel(logrus.InfoLevel)
	t.Cleanup(func() {
		logrus.SetOutput(out)
		logrus.SetLevel(level)
	})

	dir := t.TempDir()
	stdout, stderr := filepath.Join(dir, "ha.stdout.log"), filepath.Join(dir, "ha.stderr.log")
	begin := time.Now().Add(-time.Second)
	heartbeat := &hostagentevents.PortForwardEvent{}
	started := &hostagentevents.CloudInitProgress{Active: true}
	line := &hostagentevents.CloudInitProgress{Active: true, LogLine: "Setting up git ..."}
	done := &hostagentevents.CloudInitProgress{Completed: true, LogLine: "Cloud-init v. 1 finished"}
	monitorDone := &hostagentevents.CloudInitProgress{Completed: true, LogLine: "Cloud-init monitoring completed successfully"}
	for _, status := range []hostagentevents.Status{
		{SSHLocalPort: 60022, CloudInitProgress: started},
		{SSHLocalPort: 60022, CloudInitProgress: started, PortForward: heartbeat},
		{SSHLocalPort: 60022, CloudInitProgress: line},
		{SSHLocalPort: 60022, CloudInitProgress: line, PortForward: heartbeat},
		{SSHLocalPort: 60022, CloudInitProgress: line, Vsock: &hostagentevents.VsockEvent{}},
		{SSHLocalPort: 60022, CloudInitProgress: done},
		{SSHLocalPort: 60022, CloudInitProgress: done, PortForward: heartbeat},
		{SSHLocalPort: 60022, CloudInitProgress: monitorDone},
		{SSHLocalPort: 60022, CloudInitProgress: monitorDone, PortForward: heartbeat},
		{Running: true, SSHLocalPort: 60022, CloudInitProgress: monitorDone},
	} {
		writeHostAgentEvent(t, stdout, status)
	}
	ctx := WithWatchHostAgentIdleTimeout(WithWatchHostAgentTimeout(context.Background(), 30*time.Second), 10*time.Second)
	plain := true
	inst := &limatype.Instance{Name: "test", Dir: dir, Config: &limatype.LimaYAML{Plain: &plain}}
	if err := watchHostAgentEvents(ctx, inst, stdout, stderr, begin, true); err != nil {
		t.Fatal(err)
	}
	got := logs.String()
	for text, want := range map[string]int{
		"Cloud-init provisioning started...":                        1,
		"[cloud-init] Setting up git ...":                           1,
		"[cloud-init] Cloud-init v. 1 finished":                     1,
		"[cloud-init] Cloud-init monitoring completed successfully": 1,
		"Cloud-init progress monitoring done.":                      1,
	} {
		if n := strings.Count(got, text); n != want {
			t.Errorf("%q printed %d times, want %d:\n%s", text, n, want, got)
		}
	}
}
