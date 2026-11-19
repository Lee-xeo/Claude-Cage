package workbench

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sylvinus/agent-vm/internal/hostbridge"
)

func TestHostToolDirsListsTheCurrentProjectFirst(t *testing.T) {
	s := &Server{projectShares: map[string]*projectShare{"/p/b": {}, "/p/a": {}, "/p/c": {}}}
	s.preferences.Workspace = "/p/b"
	if got, want := s.hostToolDirs(), []string{"/p/b", "/p/a", "/p/c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	s.preferences.Workspace = "/p/unshared"
	if got, want := s.hostToolDirs(), []string{"/p/a", "/p/b", "/p/c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestHostToolsTokenIsKeptAcrossStarts(t *testing.T) {
	s := &Server{profile: t.TempDir()}
	first, err := s.hostToolsToken()
	if err != nil || len(first) < 32 {
		t.Fatalf("token %q: %v", first, err)
	}
	second, err := s.hostToolsToken()
	if err != nil || second != first {
		t.Fatalf("second token %q, first %q: %v", second, first, err)
	}
	if info, err := os.Stat(filepath.Join(s.profile, "host-tools.token")); err != nil || info.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("token file: %v %v", info, err)
	}
}

// The script receives the entry on stdin and writes it into the profile of
// whichever home it runs in.
func TestHostToolsConfigInputWritesTheEntry(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	home := t.TempDir()
	cmd := exec.Command(python, "-", "computer", "add")
	// Python finds the home folder through USERPROFILE on Windows.
	cmd.Env = append(os.Environ(), "HOME="+home, "USERPROFILE="+home)
	cmd.Stdin = strings.NewReader(hostToolsConfigInput([]byte(`{"type":"http","url":"http://127.0.0.1:18962/","headers":{"Authorization":"Bearer \"q\\"}}`)))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	written, err := os.ReadFile(filepath.Join(home, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"computer"`, `"http://127.0.0.1:18962/"`, `Bearer \"q\\`} {
		if !strings.Contains(string(written), want) {
			t.Fatalf("%s is missing from %s", want, written)
		}
	}
}

// The relay is what the desktop app starts: messages written to its stdin
// must reach the tools and the answers must come back on its stdout.
func TestHostToolsRelayCarriesAToolCall(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	dir := t.TempDir()
	token, err := hostbridge.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := hostbridge.Start(token, hostbridge.Command{Dirs: func() []string { return []string{dir} }})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	relay := filepath.Join(dir, "relay.py")
	endpoint := filepath.Join(dir, "endpoint.json")
	settings, _ := json.Marshal(map[string]any{"url": "http://" + bridge.Addr() + "/", "headers": map[string]string{"Authorization": "Bearer " + token}})
	if err = os.WriteFile(relay, []byte(hostToolsRelayScript), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(endpoint, settings, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, relay, endpoint)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close(); _ = cmd.Wait() }()
	timer := time.AfterFunc(30*time.Second, func() { _ = cmd.Process.Kill() })
	defer timer.Stop()
	replies := bufio.NewReader(stdout)
	exchange := func(message string, id float64) map[string]any {
		t.Helper()
		if _, err := stdin.Write([]byte(message + "\n")); err != nil {
			t.Fatal(err)
		}
		for {
			line, err := replies.ReadBytes('\n')
			if err != nil {
				t.Fatalf("no reply to %s: %v", message, err)
			}
			var reply map[string]any
			if json.Unmarshal(line, &reply) == nil && reply["id"] == id {
				return reply
			}
		}
	}
	initialized := exchange(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`, 1)
	if initialized["result"] == nil {
		t.Fatalf("initialize: %v", initialized)
	}
	if _, err = stdin.Write([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	listed, _ := json.Marshal(exchange(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, 2))
	if !strings.Contains(string(listed), `"run_command"`) {
		t.Fatalf("tools: %s", listed)
	}
	called, _ := json.Marshal(exchange(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"run_command","arguments":{"command":"echo relayed"}}}`, 3))
	if !strings.Contains(string(called), "relayed") {
		t.Fatalf("call: %s", called)
	}
}

// With the server gone the client gets an error for its request rather
// than silence.
func TestHostToolsRelayAnswersWhenTheServerIsGone(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	dir := t.TempDir()
	relay := filepath.Join(dir, "relay.py")
	endpoint := filepath.Join(dir, "endpoint.json")
	_ = os.WriteFile(relay, []byte(hostToolsRelayScript), 0o600)
	_ = os.WriteFile(endpoint, []byte(`{"url":"http://127.0.0.1:1/"}`), 0o600)
	cmd := exec.Command(python, relay, endpoint)
	cmd.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/list"}` + "\n")
	output, err := cmd.Output()
	if err != nil || !strings.Contains(string(output), `"id":7`) || !strings.Contains(string(output), `"error"`) {
		t.Fatalf("output %q: %v", output, err)
	}
}

func TestHostToolsViewListsPluginFolders(t *testing.T) {
	s := &Server{profile: t.TempDir()}
	write := func(name, manifest string) {
		dir := filepath.Join(s.hostPluginDir(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if manifest != "" {
			if err := os.WriteFile(filepath.Join(dir, hostbridge.ManifestFile), []byte(manifest), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	write("clock", `{"name":"clock","version":"1.0.0","description":"Tells the time","permissions":[],"command":["python3","server.py"]}`)
	write("broken", `{"name":"broken"}`)
	write("renamed", `{"name":"other","version":"1","permissions":["files"],"command":["x"]}`)
	write("notes", "")
	view := s.hostToolsView()
	if view.Enabled || view.Running || !view.Available || view.Folders == nil {
		t.Fatalf("view %+v", view)
	}
	var names []string
	byName := map[string]hostPluginView{}
	for _, plugin := range view.Plugins {
		names = append(names, plugin.Name)
		byName[plugin.Name] = plugin
	}
	if want := []string{"commands", "broken", "clock", "renamed"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("plugins %v, want %v", names, want)
	}
	if clock := byName["clock"]; clock.Loaded || !clock.Supported || clock.Problem != "" || clock.Version != "1.0.0" || clock.Permissions == nil {
		t.Fatalf("clock %+v", clock)
	}
	if !byName["clock"].Enabled || !byName["commands"].Enabled {
		t.Fatalf("plugins start turned on: %+v", view.Plugins)
	}
	if err := s.setHostPluginOn("clock", false); err != nil {
		t.Fatal(err)
	}
	if err := s.setHostPluginOn("commands", false); err != nil {
		t.Fatal(err)
	}
	if s.setHostPluginOn("missing", false) == nil || s.setHostPluginOn("../clock", false) == nil {
		t.Fatal("turned off a plugin that is not there")
	}
	saved, _ := os.ReadFile(filepath.Join(s.profile, "workbench.json"))
	if !strings.Contains(string(saved), `"host_plugins_off"`) || s.preferences.HostPluginsOff != "clock,commands" {
		t.Fatalf("saved %s, off %v", saved, s.preferences.HostPluginsOff)
	}
	for _, plugin := range s.hostToolsView().Plugins {
		if (plugin.Name == "clock" || plugin.Name == "commands") && plugin.Enabled {
			t.Fatalf("%s still shown as on", plugin.Name)
		}
	}
	if err := s.setHostPluginOn("clock", true); err != nil || s.preferences.HostPluginsOff != "commands" {
		t.Fatalf("off %v: %v", s.preferences.HostPluginsOff, err)
	}
	if view.Catalog == nil || len(view.Catalog) != 0 {
		t.Fatalf("catalog %+v", view.Catalog)
	}
	if err := s.removeHostPlugin("clock"); err != nil {
		t.Fatal(err)
	}
	if aside, _ := filepath.Glob(filepath.Join(s.profile, "plugins-removed", "clock-*", hostbridge.ManifestFile)); len(aside) != 1 {
		t.Fatalf("removed plugin was not kept: %v", aside)
	}
	if s.removeHostPlugin("../x") == nil || s.removeHostPlugin("clock") == nil {
		t.Fatal("removed something that is not an installed plugin")
	}
	if byName["broken"].Problem == "" || byName["renamed"].Problem == "" || !byName["commands"].BuiltIn {
		t.Fatalf("plugins %+v", view.Plugins)
	}
}

// A newer version replaces the installed one, which is kept aside; when the
// new one cannot be installed the old one is put back.
func TestInstallFromMarketReplacesAndRestores(t *testing.T) {
	manifest := func(version string) string {
		return `{"name":"notes","version":"` + version + `","permissions":[],"command":["python3","server.py"]}`
	}
	pack := func(version string) []byte {
		var packed bytes.Buffer
		compressed := gzip.NewWriter(&packed)
		archive := tar.NewWriter(compressed)
		body := manifest(version)
		_ = archive.WriteHeader(&tar.Header{Name: "notes/plugin.json", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
		_, _ = archive.Write([]byte(body))
		_ = archive.Close()
		_ = compressed.Close()
		return packed.Bytes()
	}
	good := pack("2.0.0")
	digest := sha256.Sum256(good)
	served := good
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(served) }))
	defer server.Close()
	previous := hostbridge.CommunityMarket
	hostbridge.CommunityMarket = hostbridge.Market{IndexURL: server.URL + "/index.json", ArchivePrefix: server.URL + "/dist/"}
	defer func() { hostbridge.CommunityMarket = previous }()

	s := &Server{profile: t.TempDir()}
	s.hostMarket.fetched = time.Now()
	s.hostMarket.entries = []hostbridge.MarketEntry{{Name: "notes", Version: "2.0.0", Permissions: []string{},
		Archive: server.URL + "/dist/notes-2.0.0.tar.gz", SHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(good))}}
	installed := filepath.Join(s.hostPluginDir(), "notes")
	if err := os.MkdirAll(installed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, hostbridge.ManifestFile), []byte(manifest("1.0.0")), 0o644); err != nil {
		t.Fatal(err)
	}
	version := func() string {
		m, err := hostbridge.LoadManifest(installed)
		if err != nil {
			t.Fatal(err)
		}
		return m.Version
	}
	if err := s.installFromMarket(context.Background(), "notes", false); err == nil || version() != "1.0.0" {
		t.Fatalf("replaced without being asked: %v", err)
	}
	served = pack("3.0.0") // not what the list's digest is for
	if err := s.installFromMarket(context.Background(), "notes", true); err == nil || version() != "1.0.0" {
		t.Fatalf("a failed update did not put the old version back: %v", err)
	}
	if kept, _ := filepath.Glob(filepath.Join(s.profile, "plugins-removed", "*")); len(kept) != 0 {
		t.Fatalf("old version left aside after being restored: %v", kept)
	}
	served = good
	if err := s.installFromMarket(context.Background(), "notes", true); err != nil || version() != "2.0.0" {
		t.Fatalf("update: %v", err)
	}
	if kept, _ := filepath.Glob(filepath.Join(s.profile, "plugins-removed", "notes-*", hostbridge.ManifestFile)); len(kept) != 1 {
		t.Fatalf("old version was not kept: %v", kept)
	}
	if err := s.installFromMarket(context.Background(), "missing", false); err == nil {
		t.Fatal("installed a plugin the list does not have")
	}
}
