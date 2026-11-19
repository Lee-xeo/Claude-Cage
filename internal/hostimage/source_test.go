package hostimage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceRejectsUntrustedURLsAndUnsupportedSystems(t *testing.T) {
	base := Plan{Host: Identity{System: "debian-13", Version: "13", Arch: "arm64"}, Version: "13", Format: "qcow2", Digest: "sha256:" + strings.Repeat("a", 64), URL: "https://cloud.debian.org/system.qcow2"}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{"http://cloud.debian.org/a", "https://cloud.debian.org.evil.test/a", "https://user@cloud.debian.org/a", "https://cloud.debian.org:443/a"} {
		plan := base
		plan.URL = url
		if err := plan.Validate(); err == nil {
			t.Errorf("accepted %q", url)
		}
	}
	base.Host.System = "arch-linux"
	if err := base.Validate(); err == nil {
		t.Fatal("accepted unsupported source")
	}
}

// Plans saved for guests that are no longer installed must stay readable, so
// their installation records still load, but nothing may act on them.
func TestAbandonedSystemPlansStayReadableButCannotBePrepared(t *testing.T) {
	dir := t.TempDir()
	for _, plan := range []Plan{
		{Host: Identity{System: "macos", Version: "26.5", Build: "25F71", Arch: "arm64"}, Version: "26.5", Build: "25F71", Format: "ipsw", Path: filepath.Join(dir, "system.ipsw")},
		{Host: Identity{System: "windows-11", Version: "25H2", Build: "26200.9", Edition: "Professional", Language: "en-US", Arch: "amd64"}, Version: "25H2", Build: "26200.8", Format: "esd",
			URL: "http://dl.delivery.mp.microsoft.com/example.esd", Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 10},
		{Host: Identity{System: "ubuntu-22.04", Version: "22.04", Arch: "arm64"}, Version: "22.04", Format: "qcow2",
			URL: "https://cloud-images.ubuntu.com/releases/jammy/example.img", Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 10},
		{Host: Identity{System: "ubuntu-24.04", Version: "24.04", Arch: "amd64"}, Version: "24.04", Format: "qcow2", Path: filepath.Join(dir, "system.ipsw")},
	} {
		if err := plan.Validate(); err != nil {
			t.Fatalf("%s record became unreadable: %v", plan.Host.System, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "system.ipsw"), []byte("retained media"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Prepare(t.Context(), filepath.Join(dir, "cache"), plan, io.Discard, nil); err == nil {
			t.Fatalf("%s media was prepared", plan.Host.System)
		}
		if err := WriteLinuxTemplate(filepath.Join(dir, "state"), plan); err == nil {
			t.Fatalf("%s was bound to a new environment", plan.Host.System)
		}
		if data, err := os.ReadFile(filepath.Join(dir, "system.ipsw")); err != nil || string(data) != "retained media" {
			t.Fatal("retained media was changed", err)
		}
	}
}

func TestSourceKeyAndSpaceAllowCompletedConcurrentDownload(t *testing.T) {
	cache := t.TempDir()
	plan := Plan{Host: Identity{System: "debian-13", Version: "13", Arch: "amd64"}, Version: "13", Format: "qcow2", URL: "https://cloud.debian.org/example.qcow2", Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 10}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan.CachedPath(cache)+".partial", []byte("123456"), 0600); err != nil {
		t.Fatal(err)
	}
	if remaining := plan.RemainingBytes(cache); remaining != 4 {
		t.Fatalf("remaining=%d", remaining)
	}
	ready := plan
	ready.Path = plan.CachedPath(cache)
	ready.Modified = 123
	if ready.Key() != plan.Key() {
		t.Fatal("a completed cache invalidated the selected image")
	}
	if remaining := ready.RemainingBytes(cache); remaining != 0 {
		t.Fatalf("ready remaining=%d", remaining)
	}
	plan.Digest = ""
	if err := plan.Validate(); err == nil {
		t.Fatal("accepted downloadable media without its catalog digest")
	}
}

func TestDownloadResumesAndRejectsWrongRange(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(fmt.Sprint(wrong), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Range") != "bytes=3-" {
					t.Error("did not request resume")
				}
				start := 3
				if wrong {
					start = 2
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-5/6", start))
				w.WriteHeader(http.StatusPartialContent)
				fmt.Fprint(w, "def")
			}))
			defer server.Close()
			file, err := os.CreateTemp(t.TempDir(), "partial")
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			file.WriteString("abc")
			var progress bytes.Buffer
			err = download(t.Context(), Plan{Host: Identity{System: "debian-13"}, URL: server.URL, Bytes: 6}, file, 3, &progress, nil)
			if wrong {
				if err == nil {
					t.Fatal("accepted wrong range")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(file.Name())
			if string(data) != "abcdef" {
				t.Fatalf("got %q", data)
			}
			if !strings.Contains(progress.String(), "CAGE_GUEST_BYTES debian-13 download 6 6") {
				t.Fatalf("missing actual resumed byte progress: %s", progress.String())
			}
		})
	}
}

func TestPrepareRejectsModifiedSourceAndPreservesIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "system.qcow2")
	os.WriteFile(path, []byte("original"), 0600)
	info, _ := os.Stat(path)
	digest := sha256.Sum256([]byte("original"))
	plan := Plan{Host: Identity{System: "debian-13", Version: "13", Arch: "amd64"}, Version: "13", Format: "qcow2", Path: path, Bytes: 8, Modified: info.ModTime().UnixNano(), Digest: "sha256:" + hex.EncodeToString(digest[:])}
	os.WriteFile(path, []byte("tampered"), 0600)
	if _, err := Prepare(t.Context(), filepath.Join(dir, "cache"), plan, io.Discard, nil); err == nil {
		t.Fatal("accepted changed source")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "tampered" {
		t.Fatal("changed user's source")
	}
}

func TestPrepareRecoversOversizedPartialWithoutDiscardingIt(t *testing.T) {
	cache := t.TempDir()
	content := []byte("valid image")
	digest := sha256.Sum256(content)
	plan := Plan{
		Host:    Identity{System: "debian-13", Version: "13", Arch: "amd64"},
		Version: "13", Format: "qcow2", Bytes: int64(len(content)),
		URL: "https://cloud.debian.org/fixture.qcow2", Digest: "sha256:" + hex.EncodeToString(digest[:]),
	}
	partial := plan.CachedPath(cache) + ".partial"
	bad := []byte("oversized incomplete response")
	if err := os.WriteFile(partial, bad, 0600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Range") != "" {
			t.Error("retried using the oversized prefix")
		}
		_, _ = w.Write(content)
	}))
	defer server.Close()
	// The pinned vendor address is served by the local fixture and its certificate.
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = "example.com"
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return new(net.Dialer).DialContext(ctx, network, server.Listener.Addr().String())
	}
	defer transport.CloseIdleConnections()
	for attempt := 0; attempt < 2; attempt++ {
		ready, err := Prepare(t.Context(), cache, plan, io.Discard, transport)
		if err != nil || ready.Digest != plan.Digest || ready.Path != plan.CachedPath(cache) {
			t.Fatalf("retry did not prepare the original image: %+v %v", ready, err)
		}
	}
	retained, err := filepath.Glob(partial + ".invalid-*")
	if err != nil || len(retained) != 1 || requests != 1 {
		t.Fatalf("retained=%v requests=%d error=%v", retained, requests, err)
	}
	data, err := os.ReadFile(retained[0])
	if err != nil || !bytes.Equal(data, bad) {
		t.Fatal("discarded the failed response", err)
	}
}

// An environment created from Ubuntu by an earlier version is recognised from
// its saved image choice, refused, and left untouched.
func TestUbuntuEnvironmentIsReportedAndNeverUsedAsDebian(t *testing.T) {
	for _, system := range []string{"ubuntu-22.04", "ubuntu-24.04"} {
		state := t.TempDir()
		media := filepath.Join(state, "guest-media")
		version := strings.TrimPrefix(system, "ubuntu-")
		plan := Plan{Host: Identity{System: system, Version: version, Arch: "amd64"}, Version: version, Format: "qcow2",
			Path: filepath.Join(media, system, "image.qcow2"), Digest: "sha256:" + strings.Repeat("a", 64)}
		if AbandonedEnvironment(state) != "" {
			t.Fatal("empty state reported an abandoned environment")
		}
		if err := Save(media, plan); err != nil {
			t.Fatal(err)
		}
		template, err := linuxTemplateData(plan)
		if err != nil {
			t.Fatal(err)
		}
		files := map[string][]byte{}
		for _, name := range []string{"host-system.yaml", sourceRecord} {
			path := filepath.Join(media, name)
			if name == "host-system.yaml" {
				if err := os.WriteFile(path, template, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if files[path], err = os.ReadFile(path); err != nil {
				t.Fatal(err)
			}
		}
		if got := AbandonedEnvironment(state); got != system {
			t.Fatalf("%s environment reported as %q", system, got)
		}
		var abandoned *AbandonedSystemError
		if path, err := LinuxTemplate(state); !errors.As(err, &abandoned) || abandoned.System != system || path != "" {
			t.Fatalf("%s template was offered: %q %v", system, path, err)
		}
		if got, err := LinuxSystem(state); err == nil {
			t.Fatalf("%s was reported as a usable system %q", system, got)
		}
		// A missing template must not turn the environment into Debian either.
		if err := os.Rename(filepath.Join(media, "host-system.yaml"), filepath.Join(media, "moved")); err != nil {
			t.Fatal(err)
		}
		if path, err := LinuxTemplate(state); !errors.As(err, &abandoned) || path != "" {
			t.Fatalf("%s without its template fell back: %q %v", system, path, err)
		}
		if err := os.Rename(filepath.Join(media, "moved"), filepath.Join(media, "host-system.yaml")); err != nil {
			t.Fatal(err)
		}
		for path, before := range files {
			if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, before) {
				t.Fatal("reporting changed", path, err)
			}
		}
	}
	debian := t.TempDir()
	plan := Plan{Host: Identity{System: "debian-12", Version: "12", Arch: "amd64"}, Version: "12", Format: "qcow2",
		Path: filepath.Join(debian, "image.qcow2"), Digest: "sha256:" + strings.Repeat("a", 64)}
	if err := WriteLinuxTemplate(debian, plan); err != nil {
		t.Fatal(err)
	}
	if got := AbandonedEnvironment(debian); got != "" {
		t.Fatalf("Debian environment reported as abandoned %q", got)
	}
	if system, err := LinuxSystem(debian); err != nil || system != "debian-12" {
		t.Fatalf("existing Debian 12 environment no longer starts: %q %v", system, err)
	}
}
