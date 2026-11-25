// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package hostagent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"gotest.tools/v3/assert"

	"github.com/lima-vm/lima/v2/pkg/hostagent/events"
)

func TestEmitCloudInitProgressEventStripsControlChars(t *testing.T) {
	var buf bytes.Buffer
	a := &HostAgent{eventEnc: json.NewEncoder(&buf)}

	// A compromised guest can put arbitrary bytes in its cloud-init log, including
	// an ANSI escape sequence that would rewrite the operator's terminal.
	const payload = "starting\x1b[2Kspoofed\x07 line"
	a.emitCloudInitProgressEvent(t.Context(), &events.CloudInitProgress{LogLine: payload})

	var ev events.Event
	assert.NilError(t, json.Unmarshal(buf.Bytes(), &ev))
	assert.Assert(t, ev.Status.CloudInitProgress != nil)
	got := ev.Status.CloudInitProgress.LogLine
	for _, r := range got {
		assert.Assert(t, unicode.IsPrint(r), "control char %#q survived in %#q", r, got)
	}
	assert.Equal(t, got, "starting[2Kspoofed line")
	assert.Assert(t, !strings.ContainsRune(got, '\x1b'))
}

type emittedLine struct {
	Line     string
	Finished bool
}

func collectLines(out *[]emittedLine) func(string, bool) {
	return func(line string, finished bool) {
		*out = append(*out, emittedLine{line, finished})
	}
}

// A log stream that ends before cloud-init's completion line, as when sshd
// restarts during provisioning, does not mean cloud-init finished (#828).
func TestFollowCloudInitLogEOFWithoutMarkerIsNotFinished(t *testing.T) {
	var got []emittedLine
	lines, finished := followCloudInitLog(strings.NewReader("Setting up git ...\n\nSetting up curl ...\n"), 0, collectLines(&got))
	assert.Assert(t, !finished)
	assert.Equal(t, lines, 2)
	assert.DeepEqual(t, got, []emittedLine{{"Setting up git ...", false}, {"Setting up curl ...", false}})
}

func TestFollowCloudInitLogStopsAtMarker(t *testing.T) {
	for name, log := range map[string]string{
		"cloud-init-output.log": "Setting up git ...\nCloud-init v. 24.1 finished at Mon, 01 Jan 2024 00:00:00 +0000. Up 42 seconds\nnot read\n",
		"journal": "Started cloud-init-main.service - Cloud-init: Single Process.\n" +
			"Cloud-init v. 24.1 finished at Mon\n" +
			"cloud-init-main.service: Deactivated successfully.\n" +
			"cloud-init-main.service: Consumed 3.2s CPU time.\nnot read\n",
		"journal-oneshot": "Started cloud-init-main.service - Cloud-init: Single Process.\n" +
			"LIMA 2026-11-24T00:00:00Z Exiting with code 0\n" +
			"Cloud-init v. 25.1 finished at Tue\n" +
			"Finished cloud-init-main.service - Cloud-init: Single Process.\nnot read\n",
	} {
		t.Run(name, func(t *testing.T) {
			var got []emittedLine
			_, finished := followCloudInitLog(strings.NewReader(log), 0, collectLines(&got))
			assert.Assert(t, finished)
			last := got[len(got)-1]
			assert.Assert(t, last.Finished)
			assert.Assert(t, !strings.Contains(last.Line, "not read"))
			for _, l := range got[:len(got)-1] {
				assert.Assert(t, !l.Finished, "%q reported as finished", l.Line)
			}
		})
	}
}

func TestFollowCloudInitLogSkipsReplayedLines(t *testing.T) {
	var got []emittedLine
	lines, finished := followCloudInitLog(strings.NewReader("a\nb\nc\n"), 2, collectLines(&got))
	assert.Assert(t, !finished)
	assert.Equal(t, lines, 3)
	assert.DeepEqual(t, got, []emittedLine{{"c", false}})

	// The completion line is reported even within the replayed part.
	got = nil
	_, finished = followCloudInitLog(strings.NewReader("a\nCloud-init v. 1 finished\n"), 5, collectLines(&got))
	assert.Assert(t, finished)
	assert.DeepEqual(t, got, []emittedLine{{"Cloud-init v. 1 finished", true}})
}

func TestFollowCloudInitLogCutsLongLines(t *testing.T) {
	long := strings.Repeat("#", 3*cloudInitMaxLine)
	var got []emittedLine
	lines, _ := followCloudInitLog(strings.NewReader(long+"\nnext\n"), 0, collectLines(&got))
	assert.Equal(t, lines, 2)
	assert.Equal(t, len(got[0].Line), cloudInitMaxLine)
	assert.Equal(t, got[1].Line, "next")
}

// fakeCloudInitStreams serves one log per call; once they run out, every
// stream ends at once with nothing to read.
func fakeCloudInitStreams(logs ...string) (cloudInitStream, *int) {
	calls := 0
	return func(_ context.Context, read func(io.Reader)) error {
		calls++
		if calls <= len(logs) {
			read(strings.NewReader(logs[calls-1]))
		} else {
			read(strings.NewReader(""))
		}
		return io.ErrUnexpectedEOF
	}, &calls
}

func noRetryDelay(int) time.Duration { return 0 }

// A stream that ends early is reopened; the new stream replays the log from
// the start of the boot, and only its new lines are reported.
func TestFollowCloudInitReopensStreamUntilFinished(t *testing.T) {
	stream, calls := fakeCloudInitStreams(
		"one\ntwo\n",
		"",
		"one\ntwo\nthree\nCloud-init v. 1 finished\n",
	)
	var got []emittedLine
	assert.Assert(t, followCloudInit(t.Context(), stream, collectLines(&got), noRetryDelay))
	assert.Equal(t, *calls, 3)
	assert.DeepEqual(t, got, []emittedLine{
		{"one", false}, {"two", false}, {"three", false}, {"Cloud-init v. 1 finished", true},
	})
}

func TestFollowCloudInitGivesUpWhenContextEnds(t *testing.T) {
	stream, calls := fakeCloudInitStreams("one\n")
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	var got []emittedLine
	assert.Assert(t, !followCloudInit(ctx, stream, collectLines(&got), func(int) time.Duration { return 10 * time.Millisecond }))
	assert.Assert(t, *calls > 1, "the stream was not reopened")
	assert.DeepEqual(t, got, []emittedLine{{"one", false}})
}

func TestCloudInitRetryDelayBacksOff(t *testing.T) {
	assert.Equal(t, cloudInitRetryDelay(0), time.Second)
	assert.Equal(t, cloudInitRetryDelay(3), 8*time.Second)
	assert.Equal(t, cloudInitRetryDelay(100), 30*time.Second)
}

func decodeEvents(t *testing.T, buf *bytes.Buffer) []events.Event {
	t.Helper()
	var evs []events.Event
	dec := json.NewDecoder(buf)
	for dec.More() {
		var ev events.Event
		assert.NilError(t, dec.Decode(&ev))
		evs = append(evs, ev)
	}
	return evs
}

// The last event of a watch that never saw cloud-init finish must not claim
// completion; otherwise limactl start takes the VM as provisioned (#828).
func TestWatchCloudInitReportsCompletionOnlyWhenSeen(t *testing.T) {
	var buf bytes.Buffer
	a := &HostAgent{eventEnc: json.NewEncoder(&buf)}
	stream, _ := fakeCloudInitStreams("Setting up git ...\n")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	a.watchCloudInit(ctx, stream)
	evs := decodeEvents(t, &buf)
	for _, ev := range evs {
		assert.Assert(t, !ev.Status.CloudInitProgress.Completed, "reported completed: %+v", ev.Status.CloudInitProgress)
	}
	last := evs[len(evs)-1].Status.CloudInitProgress
	assert.Assert(t, !last.Active)
	assert.Assert(t, strings.Contains(last.LogLine, "before cloud-init reported completion"), last.LogLine)

	buf.Reset()
	stream, _ = fakeCloudInitStreams("Setting up git ...\nCloud-init v. 1 finished\n")
	a.watchCloudInit(t.Context(), stream)
	evs = decodeEvents(t, &buf)
	assert.Assert(t, evs[len(evs)-1].Status.CloudInitProgress.Completed)
	assert.Assert(t, evs[len(evs)-2].Status.CloudInitProgress.Completed)
}

// When cloud-init-main.service deactivates without emitting a Consumed line
// (e.g. CPU accounting disabled), the live journalctl -f stream blocks after
// "Exiting with code 0". Once bootDone confirms /run/lima-boot-done for this
// boot and the guest probe confirms cloud-init is done, watchCloudInit must
// report completion instead of stalling (#782).
func TestWatchCloudInitPostBootProbeCompletesMissingMarker(t *testing.T) {
	var buf bytes.Buffer
	a := &HostAgent{eventEnc: json.NewEncoder(&buf)}
	stream := func(ctx context.Context, read func(io.Reader)) error {
		pr, pw := io.Pipe()
		go func() {
			defer pw.Close()
			_, _ = io.WriteString(pw, "Started cloud-init-main.service - Cloud-init: Single Process.\n"+
				"LIMA Exiting with code 0\n"+
				"cloud-init-main.service: Deactivated successfully.\n")
			<-ctx.Done()
		}()
		read(pr)
		return ctx.Err()
	}
	bootDone := make(chan struct{})
	var probes atomic.Int32
	probe := func(context.Context) bool {
		return probes.Add(1) >= 2
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.watchCloudInitWithProbe(t.Context(), stream, bootDone, probe, 10*time.Millisecond, 10*time.Millisecond)
	}()
	time.Sleep(30 * time.Millisecond)
	assert.Equal(t, probes.Load(), int32(0), "probe ran before bootDone was signaled")
	close(bootDone)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchCloudInitWithProbe stalled after bootDone and probe completion")
	}
	evs := decodeEvents(t, &buf)
	assert.Assert(t, len(evs) >= 2)
	assert.Assert(t, evs[len(evs)-1].Status.CloudInitProgress.Completed)
	assert.Assert(t, evs[len(evs)-2].Status.CloudInitProgress.Completed)
	assert.Assert(t, strings.Contains(evs[len(evs)-2].Status.CloudInitProgress.LogLine, "verified via guest status"))
}

func TestWatchCloudInitDoesNotProbeWhenLogStreamFinishesNaturally(t *testing.T) {
	var buf bytes.Buffer
	a := &HostAgent{eventEnc: json.NewEncoder(&buf)}
	stream, _ := fakeCloudInitStreams("Setting up git ...\nCloud-init v. 25.1 finished at Tue\n")
	bootDone := make(chan struct{})
	close(bootDone)
	var probes atomic.Int32
	a.watchCloudInitWithProbe(t.Context(), stream, bootDone, func(context.Context) bool {
		probes.Add(1)
		return true
	}, 200*time.Millisecond, 50*time.Millisecond)
	assert.Equal(t, probes.Load(), int32(0), "probe should not run when log stream finishes within grace window")
	evs := decodeEvents(t, &buf)
	for _, ev := range evs {
		assert.Assert(t, !strings.Contains(ev.Status.CloudInitProgress.LogLine, "verified via guest status"))
	}
	assert.Assert(t, evs[len(evs)-1].Status.CloudInitProgress.Completed)
}

// The post-boot probe must never bypass #828: if cloud-init is still running or
// failed (probe returns false) or bootDone never arrives, watchCloudInit must
// not report Completed.
func TestWatchCloudInitPostBootProbeDoesNotBypassUnfinishedBoot(t *testing.T) {
	for _, closeBootDone := range []bool{false, true} {
		name := "without-boot-done"
		if closeBootDone {
			name = "probe-still-running"
		}
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			a := &HostAgent{eventEnc: json.NewEncoder(&buf)}
			stream := func(ctx context.Context, read func(io.Reader)) error {
				pr, pw := io.Pipe()
				go func() {
					defer pw.Close()
					_, _ = io.WriteString(pw, "Setting up git ...\n")
					<-ctx.Done()
				}()
				read(pr)
				return ctx.Err()
			}
			bootDone := make(chan struct{})
			if closeBootDone {
				close(bootDone)
			}
			var probes atomic.Int32
			ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
			defer cancel()
			a.watchCloudInitWithProbe(ctx, stream, bootDone, func(context.Context) bool {
				probes.Add(1)
				return false
			}, 10*time.Millisecond, 10*time.Millisecond)
			if !closeBootDone {
				assert.Equal(t, probes.Load(), int32(0))
			} else {
				assert.Assert(t, probes.Load() >= 1)
			}
			evs := decodeEvents(t, &buf)
			for _, ev := range evs {
				assert.Assert(t, !ev.Status.CloudInitProgress.Completed, "reported completed: %+v", ev.Status.CloudInitProgress)
			}
		})
	}
}

func TestCloudInitLogStreamAwkKeepsCurrentBootCompletion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cloud-init log stream script runs inside Linux guests")
	}
	logFile := filepath.Join(t.TempDir(), "cloud-init-output.log")
	binDir := t.TempDir()
	// Fake systemctl reports cloud-init-main.service disabled; fake sudo runs directly;
	// fake tail prints the selected lines and exits without -f blocking.
	writeTool := func(name, body string) {
		t.Helper()
		assert.NilError(t, os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755))
	}
	writeTool("systemctl", "exit 1")
	writeTool("sudo", `exec "$@"`)
	writeTool("tail", `
n=1
file=""
while [ $# -gt 0 ]; do
  case "$1" in
    -n) n="$2"; shift 2 ;;
    -f) shift ;;
    *) file="$1"; shift ;;
  esac
done
exec /usr/bin/tail -n "$n" "$file"`)
	script := strings.ReplaceAll(cloudInitLogStreamScript, "/var/log/cloud-init-output.log", logFile)

	cases := []struct {
		name      string
		content   string
		wantFirst string
	}{
		{
			name: "single boot with trailing line after finished",
			content: "Cloud-init v. 25.1 running 'init-local'\n" +
				"LIMA Exiting with code 0\n" +
				"Cloud-init v. 25.1 finished at Tue\n" +
				"trailing output\n",
			wantFirst: "Cloud-init v. 25.1 running 'init-local'",
		},
		{
			name: "second boot skips previous boot but keeps current finished",
			content: "Cloud-init v. 25.1 running 'init-local' boot1\n" +
				"Cloud-init v. 25.1 finished at Mon\n" +
				"Cloud-init v. 25.1 running 'init-local' boot2\n" +
				"LIMA Exiting with code 0\n" +
				"Cloud-init v. 25.1 finished at Tue\n" +
				"\n",
			wantFirst: "Cloud-init v. 25.1 running 'init-local' boot2",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.NilError(t, os.WriteFile(logFile, []byte(tc.content), 0o600))
			cmd := exec.Command("/bin/sh", "-s")
			cmd.Env = []string{"PATH=" + binDir + ":/usr/bin:/bin"}
			cmd.Stdin = strings.NewReader(script)
			out, err := cmd.CombinedOutput()
			assert.NilError(t, err, string(out))
			var got []emittedLine
			_, finished := followCloudInitLog(bytes.NewReader(out), 0, collectLines(&got))
			assert.Assert(t, finished, "output skipped completion marker:\n%s", string(out))
			assert.Equal(t, got[0].Line, tc.wantFirst)
		})
	}
}

func TestCloudInitCompletedProbeScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cloud-init probe script runs inside Linux guests")
	}
	const iid = "iid-test1234"
	cases := []struct {
		name      string
		bootDone  string
		systemctl string
		cloudInit string
		wantOK    bool
	}{
		{
			name:      "missing boot-done fails even when cloud-init says done",
			bootDone:  "",
			systemctl: "exit 1",
			cloudInit: "echo 'status: done'; exit 0",
			wantOK:    false,
		},
		{
			name:      "stale boot-done fails",
			bootDone:  "iid-oldboot",
			systemctl: "exit 1",
			cloudInit: "echo 'status: done'; exit 0",
			wantOK:    false,
		},
		{
			name:     "cloud-init-main inactive success passes",
			bootDone: iid,
			systemctl: `
if [ "$1" = "is-enabled" ]; then exit 0; fi
case "$*" in
  *ActiveState*) echo inactive ;;
  *SubState*) echo dead ;;
  *Result*) echo success ;;
esac`,
			cloudInit: "echo 'status: running'; exit 0",
			wantOK:    true,
		},
		{
			name:     "cloud-init-main oneshot active exited success passes",
			bootDone: iid,
			systemctl: `
if [ "$1" = "is-enabled" ]; then exit 0; fi
case "$*" in
  *ActiveState*) echo active ;;
  *SubState*) echo exited ;;
  *Result*) echo success ;;
esac`,
			cloudInit: "echo 'status: running'; exit 0",
			wantOK:    true,
		},
		{
			name:     "cloud-init-main failed does not pass",
			bootDone: iid,
			systemctl: `
if [ "$1" = "is-enabled" ]; then exit 0; fi
case "$*" in
  *ActiveState*) echo failed ;;
  *SubState*) echo failed ;;
  *Result*) echo exit-code ;;
esac`,
			cloudInit: "echo 'status: error'; exit 1",
			wantOK:    false,
		},
		{
			name:      "classic cloud-init degraded done exit 2 passes",
			bootDone:  iid,
			systemctl: "exit 1",
			cloudInit: "echo 'status: degraded done'; exit 2",
			wantOK:    true,
		},
		{
			name:      "classic cloud-init still running fails",
			bootDone:  iid,
			systemctl: "exit 1",
			cloudInit: "echo 'status: running'; exit 0",
			wantOK:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bootFile := filepath.Join(dir, "lima-boot-done")
			if tc.bootDone != "" {
				assert.NilError(t, os.WriteFile(bootFile, []byte(tc.bootDone+"\n"), 0o600))
			}
			writeTool := func(name, body string) {
				t.Helper()
				assert.NilError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755))
			}
			writeTool("systemctl", tc.systemctl)
			writeTool("cloud-init", tc.cloudInit)
			writeTool("sudo", `shift; exec "$@"`)

			script := strings.ReplaceAll(cloudInitCompletedProbeScript(iid), "/run/lima-boot-done", bootFile)
			cmd := exec.Command("/bin/sh", "-s")
			cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin"}
			cmd.Stdin = strings.NewReader(script)
			err := cmd.Run()
			assert.Equal(t, err == nil, tc.wantOK)
		})
	}
}

func TestEmitStatusUpdatePreservesCloudInitProgressAndRunning(t *testing.T) {
	var buf bytes.Buffer
	a := &HostAgent{eventEnc: json.NewEncoder(&buf), sshLocalPort: 60022}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		a.emitCloudInitProgressEvent(t.Context(), &events.CloudInitProgress{Completed: true, LogLine: "Cloud-init v. 25.1 finished"})
	}()
	go func() {
		defer wg.Done()
		a.emitStatusUpdate(t.Context(), func(st *events.Status) {
			st.SSHLocalPort = a.sshLocalPort
			st.Running = true
		})
	}()
	wg.Wait()
	evs := decodeEvents(t, &buf)
	assert.Equal(t, len(evs), 2)
	last := evs[len(evs)-1].Status
	assert.Assert(t, last.Running, "last status lost Running=true: %+v", last)
	assert.Assert(t, last.CloudInitProgress != nil && last.CloudInitProgress.Completed, "last status lost CloudInitProgress.Completed: %+v", last)
}
