package managedproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

// TestHelperPreflightCore acts as a cross-platform stand-in for mihomo during
// StartPreflight unit tests when CAGE_TEST_PREFLIGHT_CORE=1.
func TestHelperPreflightCore(t *testing.T) {
	if os.Getenv("CAGE_TEST_PREFLIGHT_CORE") != "1" {
		return
	}
	args := os.Args[1:]
	var validate bool
	var configPath string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-t":
			validate = true
		case "-f":
			if i+1 < len(args) {
				configPath = args[i+1]
				i++
			}
		}
	}
	if configPath == "" {
		os.Exit(2)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		os.Exit(3)
	}
	var cfg struct {
		MixedPort    int            `yaml:"mixed-port"`
		Interface    string         `yaml:"interface-name"`
		UnifiedDelay bool           `yaml:"unified-delay"`
		Socket       string         `yaml:"external-controller-unix"`
		Auth         []string       `yaml:"authentication"`
		DNS          map[string]any `yaml:"dns"`
		ProxyGroups  []struct {
			Name    string   `yaml:"name"`
			Proxies []string `yaml:"proxies"`
		} `yaml:"proxy-groups"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		os.Exit(4)
	}
	if record := os.Getenv("CAGE_TEST_PREFLIGHT_RECORD"); record != "" {
		_ = os.WriteFile(record, raw, 0600)
	}
	mode := os.Getenv("CAGE_TEST_PREFLIGHT_MODE")
	if validate {
		if mode == "fail-validate" {
			os.Exit(5)
		}
		os.Exit(0)
	}
	if mode == "fail-start" {
		os.Exit(6)
	}
	ln, err := net.Listen("unix", cfg.Socket)
	if err != nil {
		os.Exit(7)
	}
	defer ln.Close()
	selected := ""
	for _, g := range cfg.ProxyGroups {
		if g.Name == Group && len(g.Proxies) > 0 {
			selected = g.Proxies[0]
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/configs", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"mixed-port":     cfg.MixedPort,
			"interface-name": cfg.Interface,
			"unified-delay":  cfg.UnifiedDelay,
		})
	})
	mux.HandleFunc("/proxies/"+Group, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"now": selected})
	})
	_ = http.Serve(ln, mux)
	os.Exit(0)
}

func TestStartPreflightUsesCandidateDNSAndIsolatesFromActiveProxy(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("helper re-exec uses POSIX test binary flags; Windows cross-compile verified separately")
	}
	profile := t.TempDir()
	s, err := Load(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Import("src", "", []byte(`proxies: [{name: node-a, type: socks5, server: node.example.com, port: 1080}]`), "src", 0, ""); err != nil {
		t.Fatal(err)
	}
	s.Selected = s.Sources[0].Nodes[0].ID
	s.BootstrapDNS = []string{"https://1.1.1.1/dns-query"}
	s.GuestDNS = []string{"https://1.1.1.1/dns-query"}
	if err := s.Save(profile); err != nil {
		t.Fatal(err)
	}
	activeConfigPath := filepath.Join(Dir(profile), "config.yaml")
	const activeSentinel = "sentinel: active-config\n"
	if err := Write(activeConfigPath, []byte(activeSentinel)); err != nil {
		t.Fatal(err)
	}

	// Wrapper script invokes our compiled test binary in TestHelperPreflightCore mode.
	wrapper := filepath.Join(t.TempDir(), "fake-mihomo")
	recordFile := filepath.Join(t.TempDir(), "captured.yaml")
	modeFile := filepath.Join(t.TempDir(), "mode.txt")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf("#!/bin/sh\nMODE=$(cat %q 2>/dev/null || true)\nexec env CAGE_TEST_PREFLIGHT_CORE=1 CAGE_TEST_PREFLIGHT_RECORD=%q CAGE_TEST_PREFLIGHT_MODE=\"$MODE\" %q -test.run=^TestHelperPreflightCore$ -- \"$@\"\n", modeFile, recordFile, exe)
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

	oldIface, oldCore := preflightPhysicalInterface, preflightCorePath
	t.Cleanup(func() {
		preflightPhysicalInterface, preflightCorePath = oldIface, oldCore
	})
	preflightPhysicalInterface = func(context.Context) (string, error) { return "en0", nil }
	preflightCorePath = func() (string, error) { return wrapper, nil }

	candidate := s
	candidate.BootstrapDNS = []string{"https://223.6.6.6/dns-query", "https://1.12.12.12/dns-query"}
	candidate.GuestDNS = []string{"https://dns.quad9.net/dns-query"}

	preflight, err := StartPreflight(t.Context(), profile, candidate)
	if err != nil {
		t.Fatalf("StartPreflight failed: %v", err)
	}
	if preflight.Endpoint == s.Endpoint() {
		t.Fatalf("preflight reused active endpoint %q", preflight.Endpoint)
	}
	u, err := url.Parse(preflight.Endpoint)
	if err != nil || u.Scheme != "socks5" || u.Hostname() != "127.0.0.1" {
		t.Fatalf("unexpected preflight endpoint: %q (%v)", preflight.Endpoint, err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port == s.Port || port < 1 {
		t.Fatalf("preflight port not ephemeral: %d (active %d)", port, s.Port)
	}
	pass, _ := u.User.Password()
	if u.User.Username() != "cage" || pass == "" || pass == s.Auth {
		t.Fatalf("preflight did not use one-time credentials")
	}

	// Inspect the generated candidate config passed to the core.
	capturedRaw, err := os.ReadFile(recordFile)
	if err != nil {
		t.Fatal(err)
	}
	var captured struct {
		MixedPort int    `yaml:"mixed-port"`
		Interface string `yaml:"interface-name"`
		Socket    string `yaml:"external-controller-unix"`
		DNS       struct {
			Default     []string `yaml:"default-nameserver"`
			ProxyServer []string `yaml:"proxy-server-nameserver"`
			Nameserver  []string `yaml:"nameserver"`
		} `yaml:"dns"`
	}
	if err := yaml.Unmarshal(capturedRaw, &captured); err != nil {
		t.Fatal(err)
	}
	if captured.MixedPort != port {
		t.Fatalf("mixed-port=%d want=%d", captured.MixedPort, port)
	}
	if captured.Interface != "en0" {
		t.Fatalf("interface-name=%q want=en0", captured.Interface)
	}
	if captured.Socket == "" || captured.Socket == Socket(profile) {
		t.Fatalf("preflight socket collided with active socket: %q", captured.Socket)
	}
	if !slices.Equal(captured.DNS.Default, candidate.BootstrapDNS) || !slices.Equal(captured.DNS.ProxyServer, candidate.BootstrapDNS) {
		t.Fatalf("candidate bootstrap DNS not applied: %+v", captured.DNS)
	}
	if !slices.Equal(captured.DNS.Nameserver, []string{"https://dns.quad9.net/dns-query#" + Group}) {
		t.Fatalf("candidate guest DNS not applied: %+v", captured.DNS.Nameserver)
	}

	// Active profile's config.yaml and sources.json remain untouched.
	gotActive, err := os.ReadFile(activeConfigPath)
	if err != nil || string(gotActive) != activeSentinel {
		t.Fatalf("active config.yaml mutated: %q %v", string(gotActive), err)
	}
	reloaded, err := Load(profile)
	if err != nil || !slices.Equal(reloaded.BootstrapDNS, []string{"https://1.1.1.1/dns-query"}) {
		t.Fatalf("saved sources.json mutated: %+v %v", reloaded.BootstrapDNS, err)
	}

	// Closing stops the helper process and removes the temporary socket directory.
	sockDir := filepath.Dir(captured.Socket)
	preflight.Close()
	preflight.Close() // idempotent
	if _, err := os.Stat(captured.Socket); !os.IsNotExist(err) {
		t.Fatalf("preflight socket left behind after Close: %v", err)
	}
	if _, err := os.Stat(sockDir); !os.IsNotExist(err) {
		t.Fatalf("preflight socket dir left behind after Close: %v", err)
	}

	// Validation failure cleans up and returns the standard validation error.
	if err := os.WriteFile(modeFile, []byte("fail-validate"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := StartPreflight(t.Context(), profile, candidate); err == nil || !strings.Contains(err.Error(), "未通过配置校验") {
		t.Fatalf("expected validation error, got %v", err)
	}

	// Early process exit fails promptly (does not wait 15s) and cleans up.
	if err := os.WriteFile(modeFile, []byte("fail-start"), 0600); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := StartPreflight(t.Context(), profile, candidate); err == nil {
		t.Fatal("expected early exit error")
	} else if time.Since(started) > 5*time.Second {
		t.Fatalf("early exit waited too long: %v", time.Since(started))
	}
}

func TestStartPreflightWithRealBundledMihomo(t *testing.T) {
	corePath := os.Getenv("CAGE_TEST_REAL_MIHOMO")
	if corePath == "" {
		if _, err := CorePath(); err != nil {
			t.Skip("set CAGE_TEST_REAL_MIHOMO or run beside bundled mihomo to exercise real core startup")
		}
	}
	profile := t.TempDir()
	s, err := Load(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Import("src", "", []byte(`proxies: [{name: node-a, type: socks5, server: 203.0.113.10, port: 1080}]`), "src", 0, ""); err != nil {
		t.Fatal(err)
	}
	s.Selected = s.Sources[0].Nodes[0].ID
	s.BootstrapDNS = []string{"https://223.6.6.6/dns-query"}
	s.GuestDNS = []string{"https://dns.quad9.net/dns-query"}

	if corePath != "" {
		oldCore := preflightCorePath
		t.Cleanup(func() { preflightCorePath = oldCore })
		preflightCorePath = func() (string, error) { return corePath, nil }
	}
	oldIface := preflightPhysicalInterface
	t.Cleanup(func() { preflightPhysicalInterface = oldIface })
	iface, err := PhysicalInterface(t.Context())
	if err != nil {
		// Fall back to loopback in headless environments without a physical NIC.
		iface = "lo0"
		if runtime.GOOS == "linux" {
			iface = "lo"
		}
	}
	preflightPhysicalInterface = func(context.Context) (string, error) { return iface, nil }

	preflight, err := StartPreflight(t.Context(), profile, s)
	if err != nil {
		t.Fatalf("real mihomo preflight failed: %v", err)
	}
	defer preflight.Close()

	u, err := url.Parse(preflight.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp4", u.Host, 2*time.Second)
	if err != nil {
		t.Fatalf("preflight mihomo not listening on %s: %v", u.Host, err)
	}
	_ = conn.Close()

	preflight.Close()
	if conn, err := net.DialTimeout("tcp4", u.Host, 300*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Fatalf("preflight mihomo still listening after Close on %s", u.Host)
	}
}
