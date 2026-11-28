package workbench

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	// A GitHub CLI download after Chromium must replace the Chromium label,
	// or its failure is reported as a Chromium installation failure.
	if p := parseSetupProgress("CAGE_TOOL_BYTES gh.deb 0"); p == nil || p.Label != "正在下载 GitHub CLI" {
		t.Fatalf("missing GitHub CLI download progress: %+v", p)
	}
	if p := parseSetupProgress("Installing GitHub CLI..."); p == nil || p.Label != "正在安装 GitHub CLI" {
		t.Fatalf("missing GitHub CLI stage: %+v", p)
	}
	if p := parseSetupProgress("CAGE_TOOL_BYTES chromium.deb 12 10"); p != nil {
		t.Fatalf("excess bytes entered UI: %+v", p)
	}
}

// The base system's first boot runs APT inside cloud-init; Lima relays each
// output line through logrus. Those lines replace the generic boot label so a
// slow index download is visible. APT gives no reliable total, so the card
// stays an activity indicator.
func TestBaseCloudInitAPTProgress(t *testing.T) {
	const index, deps = "正在下载 Debian 软件索引", "正在安装基础依赖"
	relay := func(s string) string {
		return `time="2026-11-26T10:00:00+08:00" level=info msg=` + strconv.Quote("[cloud-init] "+s)
	}
	for _, tc := range []struct{ line, label string }{
		{relay("+ apt-get update"), index},
		{relay("Hit:1 http://deb.debian.org/debian trixie InRelease"), index},
		{relay("Get:2 http://deb.debian.org/debian trixie-updates InRelease [47.3 kB]"), index},
		{relay("Get:3 http://deb.debian.org/debian trixie/main Sources [10.5 MB]"), index},
		{relay("Get:4 http://deb.debian.org/debian trixie/main amd64 Packages [9670 kB]"), index},
		{relay("Get:5 http://deb.debian.org/debian trixie/main Translation-en [6484 kB]"), index},
		{relay("Get:6 http://deb.debian.org/debian-security trixie-security/main amd64 Packages.diff/Index [63.3 kB]"), index},
		{relay("Ign:7 https://mirrors.ustc.edu.cn/debian trixie InRelease"), index},
		{"[cloud-init] Get:3 http://deb.debian.org/debian trixie/main Sources [10.5 MB]", index},
		{relay("+ apt-get install -y --no-upgrade --no-install-recommends -q rsync"), deps},
		{relay("Need to get 445 kB of archives."), deps},
		{relay("Get:1 http://deb.debian.org/debian trixie/main amd64 rsync amd64 3.4.1+ds1-5 [445 kB]"), deps},
		{relay("Selecting previously unselected package rsync."), deps},
		{relay("Preparing to unpack .../rsync_3.4.1+ds1-5_amd64.deb ..."), deps},
		{relay("Unpacking rsync (3.4.1+ds1-5) ..."), deps},
		{relay("Setting up rsync (3.4.1+ds1-5) ..."), deps},
		{relay("Processing triggers for man-db (2.13.1-1) ..."), deps},
	} {
		p := parseSetupProgress(tc.line)
		if p == nil || p.Phase != "image" || p.Label != tc.label || p.Received != nil || p.Total != 0 {
			t.Errorf("%s: got %+v, want image phase %q without bytes", tc.line, p, tc.label)
		}
	}
	for _, line := range []string{
		// Shared by both steps, or not APT: keep the current label.
		relay("Reading package lists..."),
		relay("Fetched 10.6 MB in 3min 2s (58.1 kB/s)"),
		relay("Cloud-init v. 25.1.4 running 'modules:final' at Wed, 26 Nov 2026 02:00:00 +0000."),
		relay("Err:3 http://deb.debian.org/debian trixie/main Sources"),
		// Cage's own setup script installs later, under its own labels.
		"Get:3 http://deb.debian.org/debian trixie/main Sources [10.5 MB]",
		"Setting up rsync (3.4.1+ds1-5) ...",
		`time="2026-11-26T10:00:00+08:00" level=info msg="SSH Local Port: 60022"`,
	} {
		if p := parseSetupProgress(line); p != nil {
			t.Errorf("%s: unexpected progress %+v", line, p)
		}
	}
}

func TestBaseCloudInitAPTProgressFollowsBoot(t *testing.T) {
	var m jobManager
	var op sync.Mutex
	var labels []string
	_, err := m.submit(&op, "setup", 0, nil, nil, func() error {
		e := m.execution()
		e.setSetupProgress(&SetupProgress{Phase: "image", System: "debian-13"})
		for _, line := range []string{
			"CAGE_IMAGE_READY",
			`time="2026-11-26T10:00:00+08:00" level=info msg="[cloud-init] Get:3 http://deb.debian.org/debian trixie/main Sources [10.5 MB]"`,
			`time="2026-11-26T10:03:00+08:00" level=info msg="[cloud-init] Fetched 10.6 MB in 3min 2s (58.1 kB/s)"`,
			`time="2026-11-26T10:03:05+08:00" level=info msg="[cloud-init] Unpacking rsync (3.4.1+ds1-5) ..."`,
			"Installing packages inside VM...",
		} {
			_, _ = e.Write([]byte(line + "\n"))
			p := m.snapshot().SetupProgress
			labels = append(labels, p.Phase+"/"+p.Label+"/"+p.System)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitJobIdle(t, &m)
	want := []string{
		"image/正在启动 Linux 基础环境/debian-13",
		"image/正在下载 Debian 软件索引/debian-13",
		"image/正在下载 Debian 软件索引/debian-13",
		"image/正在安装基础依赖/debian-13",
		"packages/正在安装 Linux 基础软件包/debian-13",
	}
	if strings.Join(labels, "\n") != strings.Join(want, "\n") {
		t.Fatalf("labels:\n%s\nwant:\n%s", strings.Join(labels, "\n"), strings.Join(want, "\n"))
	}
	if log := m.snapshot().Log; !strings.Contains(log, "Sources") {
		t.Fatalf("cloud-init line left the operation log: %q", log)
	}
}

// Two progress lines from one pipe read are processed back to back; the job
// must keep a finite rate so its status stays encodable.
func TestImageProgressRateIgnoresBackToBackLines(t *testing.T) {
	var m jobManager
	var op sync.Mutex
	_, err := m.submit(&op, "start", 0, nil, nil, func() error {
		e := m.execution()
		e.setSetupProgress(&SetupProgress{Phase: "image"})
		_, _ = e.Write([]byte("CAGE_IMAGE_BYTES 0 100\n"))
		m.mu.Lock()
		e.progressUpdated = time.Now().Add(-2 * time.Second)
		m.mu.Unlock()
		_, _ = e.Write([]byte("CAGE_IMAGE_BYTES 50 100\nCAGE_IMAGE_BYTES 60 100\nCAGE_IMAGE_BYTES 60 100\n"))
		job := m.snapshot()
		if p := job.SetupProgress; *p.Received != 60 || p.BytesPerSecond < 24 || p.BytesPerSecond > 26 {
			t.Errorf("rate not kept across back-to-back lines: %+v", p)
		}
		if _, err := json.Marshal(job); err != nil {
			t.Errorf("job status not encodable: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitJobIdle(t, &m)
}

func TestJSONReplyReportsUnencodableValues(t *testing.T) {
	w := httptest.NewRecorder()
	jsonReply(w, map[string]float64{"rate": math.Inf(1)})
	if w.Code != http.StatusInternalServerError || json.Valid(w.Body.Bytes()) {
		t.Fatalf("got %d %q, want an error response", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	jsonReply(w, map[string]int{"ok": 1})
	if w.Code != http.StatusOK || w.Body.String() != "{\"ok\":1}\n" || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("got %d %q", w.Code, w.Body.String())
	}
}
