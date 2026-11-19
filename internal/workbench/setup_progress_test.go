package workbench

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSetupProgressParsesComponentNamesBeforeLogRedaction(t *testing.T) {
	var m jobManager
	var op sync.Mutex
	_, err := m.submit(&op, "setup", 0, nil, nil, func() error {
		e := m.execution()
		e.setSetupProgress(&SetupProgress{Phase: "image"})
		for _, part := range []string{"CAGE_TOOL_BYTES chromium-", "common.deb 5 10\n"} {
			_, _ = e.Write([]byte(part))
		}
		p := m.snapshot().SetupProgress
		if p.Phase != "packages" || p.Received == nil || *p.Received != 5 || p.Total != 10 {
			t.Errorf("tool component progress lost: %+v", p)
		}
		_, _ = e.Write([]byte("connect error: static.example-provider.io:1337\n"))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitJobIdle(t, &m)
	log := m.snapshot().Log
	if strings.Contains(log, "static.example-provider.io") || strings.Contains(log, "1337") || strings.Contains(log, "CAGE_") || !strings.Contains(log, "connect error:") {
		t.Fatalf("job log leaked raw output or lost error evidence: %q", log)
	}
}

func TestRemovedGuestInstallationProgressIsIgnored(t *testing.T) {
	for _, line := range []string{
		"CAGE_GUEST_STAGE windows-11 oobe", "CAGE_GUEST_STAGE macos restore", "CAGE_GUEST_STAGE debian-13 tools",
		"CAGE_GUEST_BYTES windows-11 download 1 10", "CAGE_GUEST_BYTES macos download 1 10",
		"CAGE_HOST_BYTES chrome.dmg 25 100", "CAGE_HOST_RETRY editor.tgz 2", "下载 editor.tgz",
	} {
		if parseSetupProgress(line) != nil {
			t.Fatalf("removed installation progress accepted: %s", line)
		}
	}
}

// Child pipes may split a progress line across writes. A fallback mirror may
// restart from zero, and an absent Content-Length must stay indeterminate.
func TestSetupProgressFromChildOutput(t *testing.T) {
	var m jobManager
	var op sync.Mutex
	var points []*SetupProgress
	_, err := m.submit(&op, "start", 0, nil, nil, func() error {
		e := m.execution()
		e.setSetupProgress(&SetupProgress{Phase: "image"})
		for _, part := range []string{"CAGE_IMAGE_BYTES 25", " 100\n", "CAGE_IMAGE_BYTES 0 -1\n", "CAGE_IMAGE_BYTES 123 5\n", "CAGE_IMAGE_READY\n", "Installing Claude Code...\n"} {
			_, _ = e.Write([]byte(part))
			points = append(points, m.snapshot().SetupProgress)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitJobIdle(t, &m)
	if len(points) != 6 || points[0].Received != nil || points[1].Received == nil || *points[1].Received != 25 || points[1].Total != 100 || points[2].Received == nil || *points[2].Received != 0 || points[2].Total != 0 || points[3] != points[2] || points[4].Received != nil || points[5].Phase != "tools" {
		t.Fatalf("bad setup progress sequence: %+v", points)
	}
}

func TestImageProgressKeepsSystemAndSeparatesVerification(t *testing.T) {
	var m jobManager
	var op sync.Mutex
	_, err := m.submit(&op, "guest-install", 0, nil, nil, func() error {
		e := m.execution()
		e.setSetupProgress(&SetupProgress{Phase: "image", System: "debian-13"})
		_, _ = e.Write([]byte("CAGE_GUEST_BYTES debian-13 download 25"))
		if m.snapshot().SetupProgress.Received != nil {
			t.Error("partial line became progress")
		}
		_, _ = e.Write([]byte(" 100\n"))
		m.mu.Lock()
		e.progressUpdated = time.Now().Add(-2 * time.Second)
		m.mu.Unlock()
		_, _ = e.Write([]byte("CAGE_GUEST_BYTES debian-13 download 75 100\n"))
		p := m.snapshot().SetupProgress
		if p.BytesPerSecond < 24 || p.BytesPerSecond > 26 || *p.Received != 75 {
			t.Errorf("wrong measured rate: %+v", p)
		}
		_, _ = e.Write([]byte("CAGE_GUEST_BYTES debian-13 verify 0 100\n"))
		p = m.snapshot().SetupProgress
		if p.System != "debian-13" || p.Operation != "verify" || p.BytesPerSecond != 0 || *p.Received != 0 {
			t.Errorf("download leaked into verification: %+v", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitJobIdle(t, &m)
	for _, line := range []string{"CAGE_GUEST_BYTES unknown download 2 10", "CAGE_GUEST_BYTES debian-13 download 11 10", "CAGE_GUEST_BYTES debian-13 verify -1 10", "CAGE_GUEST_BYTES debian-13 anything 1 10"} {
		if p := parseSetupProgress(line); p != nil {
			t.Errorf("invalid progress accepted: %s", line)
		}
	}
	for _, system := range []string{"debian-12", "debian-13"} {
		p := parseSetupProgress("CAGE_GUEST_BYTES " + system + " download 1 10")
		if p == nil || p.Total != 10 || p.System != system {
			t.Errorf("missing shared progress for %s", system)
		}
	}
}

func TestToolProgressUsesOnlyKnownPackageNames(t *testing.T) {
	p := parseSetupProgress("CAGE_TOOL_BYTES claude-code-linux-x64.tgz 4980736")
	if p == nil || p.Phase != "tools" || p.Received == nil || *p.Received != 4980736 || p.Total != 0 {
		t.Fatalf("expected current bytes without a guessed total, got %+v", p)
	}
	if p := parseSetupProgress("CAGE_TOOL_BYTES untrusted 12"); p != nil {
		t.Fatalf("unexpected package name entered UI: %+v", p)
	}
	if p := parseSetupProgress("CAGE_TOOL_BYTES chromium-common.deb 5 10"); p == nil || p.Total != 10 || *p.Received != 5 {
		t.Fatalf("missing Chromium progress from signed index: %+v", p)
	}
	if p := parseSetupProgress("CAGE_TOOL_BYTES chromium.deb 12 10"); p != nil {
		t.Fatalf("excess bytes entered UI: %+v", p)
	}
}
