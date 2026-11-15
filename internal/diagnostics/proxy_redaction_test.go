package diagnostics

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const proxyFailure = `time="2026-11-14T10:20:30+08:00" level=warning msg="[TCP] dial CAGE (match Match/) 192.0.2.8:63839 --> api.github.com:443 error: static.example-provider.io:1337 connect error: dns resolve failed: context deadline exceeded"`

func TestRedactProxyAddresses(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"static.example-provider.io:1337 api.github.com", "[host] [host]"},
		{"STATIC.EXAMPLE-PROVIDER.IO:1337 xn--bcher-kva.example.", "[host] [host]"},
		{"edge.xn--p1ai:443 _gateway.example:1337", "[host] [host]"},
		{"192.0.2.8:63839", "[ip]"},
		{"[ip]:63839 --> static.example-provider.io:1337", "[ip] --> [host]"},
		{"[2001:db8::1]:443 [fe80::1%en0]:53", "[ip] [ip]"},
		{"2001:db8:1234:5678:90ab:cdef:1234:5678 2001:db8::1 ::1 ::", "[ip] [ip] [ip] [ip]"},
		{"[::ffff:192.0.2.8]:443 ::ffff:192.0.2.8", "[ip] [ip]"},
		{"proxy 0123456789abcdef01234567 uuid=12345678-1234-1234-1234-123456789abc", "proxy [redacted id] uuid=[redacted id]"},
		{"https://sub.example-provider.io/sub?token=private-subscription token=private-token", "[url] token=[redacted]"},
		{"2026-11-14T10:20:30+08:00 v0.1.7 HTTP 502 dns resolve failed: context deadline exceeded", "2026-11-14T10:20:30+08:00 v0.1.7 HTTP 502 dns resolve failed: context deadline exceeded"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got := Redact(tc.input)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if Redact(got) != got {
				t.Fatal("repeated redaction changed the output")
			}
		})
	}
}

func assertProxyFailureRedacted(t *testing.T, text string) {
	t.Helper()
	for _, secret := range []string{"192.0.2.8", "63839", "api.github.com", "static.example-provider.io", "1337"} {
		if strings.Contains(text, secret) {
			t.Errorf("leaked %q: %s", secret, text)
		}
	}
	for _, evidence := range []string{"2026-11-14T10:20:30+08:00", "warning", "[TCP]", "dns resolve failed: context deadline exceeded"} {
		if !strings.Contains(text, evidence) {
			t.Errorf("lost diagnostic evidence %q: %s", evidence, text)
		}
	}
}

func TestProxyStreamsRedactBeforeStorageAndSink(t *testing.T) {
	for _, source := range []string{"proxy", "proxy-validation"} {
		t.Run(source, func(t *testing.T) {
			store := New(t.TempDir())
			var sink strings.Builder
			stream := NewStream(store, source, "error", func(line string) { sink.WriteString(line) })
			// Producers can split hostnames and credentials between writes.
			for _, b := range []byte(proxyFailure + "\n") {
				if _, err := stream.Write([]byte{b}); err != nil {
					t.Fatal(err)
				}
			}
			if err := stream.Close(); err != nil {
				t.Fatal(err)
			}
			assertProxyFailureRedacted(t, sink.String())
			events := readEvents(t, store)
			if len(events) != 1 || events[0].Source != source || events[0].Level != "warn" {
				t.Fatalf("unexpected records: %+v", events)
			}
			assertProxyFailureRedacted(t, events[0].Message)
		})
	}
}

func TestStreamConsumerKeepsStoredRecordsRedacted(t *testing.T) {
	store := New(t.TempDir())
	var sink, consumed strings.Builder
	stream := NewStreamWithConsumer(store, "task/setup", "info", func(line string) { sink.WriteString(line) }, func(line string) bool {
		consumed.WriteString(line)
		return true
	})
	if _, err := stream.Write([]byte(proxyFailure + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	if consumed.String() != proxyFailure || sink.Len() != 0 {
		t.Fatal("internal consumer did not receive the original complete line or failed to suppress display")
	}
	events := readEvents(t, store)
	if len(events) != 1 {
		t.Fatalf("consumed line was lost from diagnostic storage: %+v", events)
	}
	assertProxyFailureRedacted(t, events[0].Message)
}

func TestExportRedactsLegacyProxyMessages(t *testing.T) {
	store := New(t.TempDir())
	if err := store.Clean(); err != nil {
		t.Fatal(err)
	}
	var legacy bytes.Buffer
	for _, source := range []string{"proxy", "proxy-validation"} {
		message := strings.Replace(proxyFailure, "192.0.2.8", "[ip]", 1)
		if err := json.NewEncoder(&legacy).Encode(Event{Time: time.Now().UTC().Format(time.RFC3339Nano), Level: "warn", Source: source, Message: message}); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate a log retained from a version with incomplete redaction.
	if err := os.WriteFile(filepath.Join(store.dir, "events.jsonl"), legacy.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	var bundle bytes.Buffer
	if err := store.Export(&bundle, "test", []byte(proxyFailure), "collected"); err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(bundle.Bytes()), int64(bundle.Len()))
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, file := range archive.File {
		if file.Name != "logs/events.jsonl" && file.Name != "guest/services.log" {
			continue
		}
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		assertProxyFailureRedacted(t, string(data))
		checked++
	}
	if checked != 2 {
		t.Fatal("export omitted diagnostic logs")
	}
}

func TestGuestRunnerRedactsProxyAddresses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("guest runner executes on Linux; POSIX shell fixture")
	}
	if _, err := exec.LookPath("uv"); err != nil {
		t.Skip("uv unavailable")
	}
	t.Setenv("UV_CACHE_DIR", t.TempDir())
	script := filepath.Join(t.TempDir(), "runner.py")
	if err := os.WriteFile(script, []byte(GuestRunner()), 0600); err != nil {
		t.Fatal(err)
	}
	input := proxyFailure + "\n[::ffff:192.0.2.8]:443 fe80::1%en0 0123456789abcdef01234567 token=private-token"
	cmd := exec.Command("uv", "run", "--no-project", "python", script, "/bin/sh", "-c", `printf '%s\n' "$1"`, "sh", input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("guest runner failed: %s: %v", stderr.String(), err)
	}
	assertProxyFailureRedacted(t, string(out))
	if want := Redact(input) + "\n"; string(out) != want {
		t.Fatalf("host and guest redaction differ: got %q, want %q", out, want)
	}
}
