// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package hostagent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
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
