package managedproxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/host"
	"go.yaml.in/yaml/v3"
)

func TestCumulativeImportsPreserveReadableStoreOnOverflow(t *testing.T) {
	profile := t.TempDir()
	s, err := Load(profile)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("proxies:\n  - name: synthetic\n    type: socks5\n    server: example.com\n    port: 1080\n    password: " + strings.Repeat("x", 7<<20) + "\n")
	for i := 0; i < 4; i++ {
		if err := s.Import(fmt.Sprintf("source-%d", i), "", data, "", 0, ""); err != nil {
			t.Fatal(err)
		}
		if err := s.Save(profile); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(Dir(profile), "sources.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Import("overflow", "", data, "", 0, ""); err != nil {
		t.Fatal("individual import should remain within its input limit", err)
	}
	if err := s.Save(profile); err == nil {
		t.Fatal("cumulative imports saved an unreadable store")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed save changed the previous file", err)
	}
	loaded, err := Load(profile)
	if err != nil || len(loaded.Sources) != 4 || loaded.Auth != s.Auth {
		t.Fatal("previous sources could not be recovered after rejected import", err)
	}
	// Normal source removal must still work after the rejected import.
	loaded.Sources = loaded.Sources[:3]
	if err := loaded.Save(profile); err != nil {
		t.Fatal(err)
	}
	if loaded, err := Load(profile); err != nil || len(loaded.Sources) != 3 {
		t.Fatal("source removal failed after rejected import", err)
	}
}

func TestStoreLimitCoversSerializedBytesAndAllowsExactLimit(t *testing.T) {
	profile := t.TempDir()
	s, _ := Load(profile)
	if err := s.Import("source", "", []byte(testNodes), "source", 0, ""); err != nil {
		t.Fatal(err)
	}
	s.Sources[0].Nodes[0].Config["password"] = ""
	if err := s.Save(profile); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(Dir(profile), "sources.json")
	base, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	padding := strings.Repeat("x", maxStoreSize-int(base.Size()))
	s.Sources[0].Nodes[0].Config["password"] = padding
	if err := s.Save(profile); err != nil {
		t.Fatal("exact limit was rejected", err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != maxStoreSize {
		t.Fatal("fixture did not reach the exact serialized limit", err)
	}
	if _, err := Load(profile); err != nil {
		t.Fatal("exact-size saved store was not readable", err)
	}
	// The raw string has the same byte length, but JSON escapes '<' into six
	// bytes. Limits must cover the persisted representation, not input lengths.
	s.Sources[0].Nodes[0].Config["password"] = padding[:len(padding)-1] + "<"
	if err := s.Save(profile); err == nil {
		t.Fatal("JSON expansion bypassed the persisted-size limit")
	}
	loaded, err := Load(profile)
	if err != nil || loaded.Sources[0].Nodes[0].Config["password"] != padding {
		t.Fatal("overflow replaced the exact-size previous store", err)
	}
}

const testNodes = `mixed-port: 7890
external-controller: 0.0.0.0:9090
tun: {enable: true}
rules: [MATCH,DIRECT]
proxies:
  - {name: edge, type: socks5, server: 203.0.113.10, port: 1080, password: private-secret}
  - {name: chained, type: http, server: 203.0.113.10, port: 8080, dialer-proxy: edge}
`

func TestImportCompilesOnlyOwnedNetworkBoundary(t *testing.T) {
	s, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Import("test", "", []byte(testNodes), "source", 0, ""); err != nil {
		t.Fatal(err)
	}
	s.Selected = s.Sources[0].Nodes[1].ID
	b, err := s.Runtime("/tmp/qa")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if yaml.Unmarshal(b, &config) != nil {
		t.Fatal("invalid runtime")
	}
	if config["mixed-port"] != 17896 || config["allow-lan"] != false || config["ipv6"] != false || config["external-controller"] != nil {
		t.Fatalf("import changed boundary: %v", config)
	}
	if !strings.Contains(string(b), "IN-USER,cage,CAGE") || !strings.Contains(string(b), "MATCH,REJECT") || strings.Contains(string(b), "DIRECT") || strings.Contains(string(b), "enable: true\nproxies:") {
		t.Fatal("unsafe imported routing")
	}
	if config["tun"].(map[string]any)["enable"] != false {
		t.Fatal("TUN enabled")
	}
	if config["unified-delay"] != true {
		t.Fatal("node latency includes connection setup instead of an established-connection request")
	}
	if s.Sources[0].Nodes[1].Config["dialer-proxy"] != s.Sources[0].Nodes[0].ID {
		t.Fatal("chain not namespaced")
	}
	profile := t.TempDir()
	if err = s.Save(profile); err != nil {
		t.Fatal(err)
	}
	if err := host.CheckPrivateFile(filepath.Join(Dir(profile), "sources.json")); err != nil {
		t.Fatalf("source credentials not private: %v", err)
	}
	loaded, err := Load(profile)
	if err != nil || loaded.Endpoint() != s.Endpoint() {
		t.Fatal("credentials not stable")
	}
}
func TestImportRejectsFilesDirectAndCycles(t *testing.T) {
	for _, nodes := range []string{
		`proxies: [{name: test, type: http, server: example.com, port: 80, interface-name: en0}]`,
		`proxies: [{name: test, type: socks5, server: example.com, port: 80, dialer-proxy: DIRECT}]`,
		`proxies: [{name: test, type: socks5, server: example.com, port: 80, dialer-proxy: test}]`,
		`proxies: [{name: test, type: http, server: example.com, port: 80, ws-opts: {private-key: /etc/passwd}}]`,
		`proxies: [{name: test, type: mieru, server: example.com, port: 443, transport: TCP, username: test-user, password: test-password, interface-name: en0}]`,
		`proxies: [{name: test, type: mieru, server: example.com, port: 443, transport: TCP, username: test-user, password: test-password, tls-opts: {ca: /etc/passwd}}]`,
		`proxies: [{name: duplicate, type: http, server: a, port: 80}, {name: duplicate, type: http, server: b, port: 80}]`,
	} {
		if _, err := Parse([]byte(nodes), "test"); err == nil {
			t.Fatal("unsafe import accepted")
		}
	}
}

func TestAnyTLSIdleSessionOptionsRemainCompatible(t *testing.T) {
	_, err := Parse([]byte(`proxies: [{name: qa, type: anytls, server: example.com, port: 443, password: secret, idle-session-check-interval: 30, idle-session-timeout: 30, min-idle-session: 0}]`), "source")
	if err != nil {
		t.Fatal(err)
	}
}

func TestMieruMixedSubscription(t *testing.T) {
	// Match the reported subscription's mix without using account credentials.
	var data strings.Builder
	data.WriteString("proxies:\n")
	for i := 0; i < 113; i++ {
		fmt.Fprintf(&data, "  - {name: vless-%d, type: vless, server: example.com, port: 443, uuid: 00000000-0000-4000-8000-000000000001}\n", i)
	}
	for i := 0; i < 19; i++ {
		fmt.Fprintf(&data, "  - {name: mieru-%d, type: mieru, server: example.com, port: 443, username: test-user, password: test-password, transport: TCP, udp: true}\n", i)
	}
	profile := t.TempDir()
	s, err := Load(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Import("mixed", "", []byte(data.String()), "source", 24, ""); err != nil {
		t.Fatal(err)
	}
	if s.Count() != 132 {
		t.Fatalf("imported %d nodes, want 132", s.Count())
	}
	s.Selected = s.Sources[0].Nodes[113].ID
	if err := s.Save(profile); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(profile)
	if err != nil {
		t.Fatal(err)
	}
	node, ok := loaded.Find(s.Selected)
	if !ok || node.Type != "mieru" || node.Config["transport"] != "TCP" || node.Config["username"] != "test-user" || node.Config["password"] != "test-password" || node.Config["udp"] != false {
		t.Fatal("Mieru options or Cage UDP boundary lost during persistence")
	}
	other, err := Parse([]byte(data.String()), "source")
	if err != nil || other[113].ID != node.ID {
		t.Fatal("refresh changed Mieru node identity", err)
	}
	checkMieruRuntime(t, profile, loaded)
}

func TestMieruTransportOptions(t *testing.T) {
	for _, root := range []string{"proxies", "payload"} {
		t.Run(root, func(t *testing.T) {
			s, err := Load(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			data := root + `: [{name: mieru-udp, type: mieru, server: example.com, port-range: 8000-8010, username: test-user, password: test-password, transport: UDP, multiplexing: MULTIPLEXING_LOW, handshake-mode: HANDSHAKE_STANDARD}]`
			if err := s.Import("mieru", "", []byte(data), "source", 0, ""); err != nil {
				t.Fatal(err)
			}
			s.Selected = s.Sources[0].Nodes[0].ID
			checkMieruRuntime(t, t.TempDir(), s)
		})
	}
}

func checkMieruRuntime(t *testing.T, profile string, s Store) {
	t.Helper()
	b, err := s.Runtime(profile)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(b, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Proxies) != s.Count() {
		t.Fatal("runtime lost imported nodes")
	}
	for _, node := range config.Proxies {
		if node["type"] == "mieru" && (node["transport"] == nil || node["udp"] != false) {
			t.Fatal("runtime lost Mieru transport or enabled guest UDP")
		}
	}
	// Run the compiled test binary beside the bundled mihomo to exercise the
	// same candidate-config validation used by subscription import and refresh.
	if os.Getenv("CAGE_PROXY_INTEGRATION") == "1" {
		if err := Validate(context.Background(), profile, s); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRefreshIdentityAndInvalidUpdatePreserveNodes(t *testing.T) {
	s, _ := Load(t.TempDir())
	if err := s.Import("test", "https://example.com/sub?secret=x", []byte(testNodes), "source", 24, ""); err != nil {
		t.Fatal(err)
	}
	id := s.Sources[0].Nodes[0].ID
	if err := s.Import("test", "", []byte("not: a subscription"), "source", 24, ""); err == nil {
		t.Fatal("bad source accepted")
	}
	if s.Sources[0].Nodes[0].ID != id {
		t.Fatal("failed update lost nodes")
	}
	if err := s.Import("test", "", []byte(testNodes), "source", 24, ""); err != nil || s.Sources[0].Nodes[0].ID != id {
		t.Fatal("refresh changed node identity")
	}
	other, _ := Parse([]byte(testNodes), "other")
	if other[0].ID == id {
		t.Fatal("sources collide")
	}
}
func TestSubscriptionFetchAndMetadata(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.UserAgent(); ua != subscriptionUserAgent || strings.Contains(strings.ToLower(ua), "cage") {
			t.Errorf("incorrect subscription user agent: %q", ua)
		}
		w.Header().Set("Subscription-Userinfo", "upload=2; download=3; total=100; expire=123; secret=credential")
		io.WriteString(w, testNodes)
	}))
	defer server.Close()
	data, usage, err := Fetch(context.Background(), server.Client(), server.URL)
	if err != nil || string(data) != testNodes || usage != "upload=2; download=3; total=100; expire=123" {
		t.Fatalf("fetch: %s %v", usage, err)
	}
	if _, _, err = Fetch(context.Background(), server.Client(), "http://example.com"); err == nil {
		t.Fatal("insecure subscription accepted")
	}
}
func TestBootstrapDoesNotAllowPrivateDestinations(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "198.18.0.1", "::1"} {
		if publicIP(netip.MustParseAddr(ip)) {
			t.Fatal("infrastructure private destination allowed")
		}
	}
}

func TestRuntimeUsesConfiguredResolvers(t *testing.T) {
	s, _ := Load(t.TempDir())
	if err := s.Import("r", "", []byte(`proxies: [{name: a, type: socks5, server: node.example.com, port: 1080}]`), "r", 0, ""); err != nil {
		t.Fatal(err)
	}
	s.Selected = s.Sources[0].Nodes[0].ID
	runtime := func() map[string]any {
		t.Helper()
		b, err := s.Runtime(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			DNS map[string]any `yaml:"dns"`
		}
		if err := yaml.Unmarshal(b, &config); err != nil {
			t.Fatal(err)
		}
		return config.DNS
	}
	dns := runtime()
	if fmt.Sprint(dns["nameserver"]) != "[https://1.1.1.1/dns-query#"+Group+"]" || fmt.Sprint(dns["proxy-server-nameserver"]) != fmt.Sprint(BootstrapDNSURLs()) {
		t.Fatalf("default resolvers changed: %v", dns)
	}
	s.GuestDNS = []string{"https://dns.example/dns-query", "https://8.8.8.8/dns-query"}
	s.BootstrapDNS = []string{"https://223.5.5.5/dns-query"}
	dns = runtime()
	if fmt.Sprint(dns["nameserver"]) != "[https://dns.example/dns-query#"+Group+" https://8.8.8.8/dns-query#"+Group+"]" {
		t.Fatalf("core nameserver ignores guest DNS settings: %v", dns["nameserver"])
	}
	if fmt.Sprint(dns["default-nameserver"]) != "[https://223.5.5.5/dns-query]" || fmt.Sprint(dns["proxy-server-nameserver"]) != "[https://223.5.5.5/dns-query]" {
		t.Fatalf("node resolvers ignore settings: %v", dns)
	}
}

func TestRuntimeEnablesTopLevelIPv6OnlyForIPv6LiteralNodes(t *testing.T) {
	cases := []struct {
		name     string
		server   string
		wantIPv6 bool
	}{
		{name: "domain", server: "node.example.com", wantIPv6: false},
		{name: "ipv4", server: "203.0.113.10", wantIPv6: false},
		{name: "ipv4-mapped-ipv6", server: "::ffff:203.0.113.10", wantIPv6: false},
		{name: "ipv6-literal", server: "2001:db8::10", wantIPv6: true},
		{name: "ipv6-bracketed", server: "[2001:db8::10]", wantIPv6: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := Load(t.TempDir())
			raw := fmt.Sprintf("proxies: [{name: a, type: socks5, server: %q, port: 1080}]", tc.server)
			if err := s.Import("r", "", []byte(raw), "r", 0, ""); err != nil {
				t.Fatal(err)
			}
			s.Selected = s.Sources[0].Nodes[0].ID
			b, err := s.Runtime(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			var cfg map[string]any
			if err := yaml.Unmarshal(b, &cfg); err != nil {
				t.Fatal(err)
			}
			if got, _ := cfg["ipv6"].(bool); got != tc.wantIPv6 {
				t.Fatalf("top-level ipv6=%v, want %v", got, tc.wantIPv6)
			}
			dnsCfg, _ := cfg["dns"].(map[string]any)
			if dnsIPv6, _ := dnsCfg["ipv6"].(bool); dnsIPv6 {
				t.Fatalf("dns.ipv6 must always stay false, got %v", dnsCfg["ipv6"])
			}
		})
	}
}

// Top-level ipv6 is global to the core (domain-named nodes then resolve AAAA
// and may dial over the host's IPv6). Any loaded IPv6-literal node needs it to
// be dialable for latency tests and account routes; unless the selected route
// has an IPv6-literal hop, domain-named servers stay A-only via ip-version.
func TestRuntimeIPv6LiteralNodesStayDialableWithoutWideningDomains(t *testing.T) {
	runtime := func(t *testing.T, s Store) (bool, map[string]any) {
		t.Helper()
		b, err := s.Runtime(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		var cfg map[string]any
		if err := yaml.Unmarshal(b, &cfg); err != nil {
			t.Fatal(err)
		}
		versions := map[string]any{}
		for _, p := range cfg["proxies"].([]any) {
			p := p.(map[string]any)
			versions[p["name"].(string)] = p["ip-version"]
		}
		got, _ := cfg["ipv6"].(bool)
		return got, versions
	}
	s, _ := Load(t.TempDir())
	raw := `proxies: [{name: v4, type: socks5, server: node.example.com, port: 1080, ip-version: dual}, {name: v6, type: socks5, server: "2001:db8::10", port: 1080}, {name: via, type: socks5, server: 203.0.113.10, port: 1080, dialer-proxy: v6}, {name: hop, type: socks5, server: hop.example.com, port: 1080, dialer-proxy: v4}]`
	if err := s.Import("r", "", []byte(raw), "r", 0, ""); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, n := range s.Sources[0].Nodes {
		ids[n.Name] = n.ID
	}
	s.Selected = ids["v4"]
	top, versions := runtime(t, s)
	if !top {
		t.Fatal("an unselected IPv6-literal node must stay dialable for latency tests and account routes")
	}
	for _, name := range []string{"v4", "hop"} {
		if versions[ids[name]] != "ipv4" {
			t.Fatalf("domain-named node %s widened to IPv6 by an unselected IPv6-literal node: %v", name, versions)
		}
	}
	if versions[ids["v6"]] != nil || versions[ids["via"]] != nil {
		t.Fatalf("literal servers must not be pinned: %v", versions)
	}
	for _, selected := range []string{"v6", "via"} {
		s.Selected = ids[selected]
		top, versions = runtime(t, s)
		if !top {
			t.Fatalf("selected route through %s needs top-level ipv6", selected)
		}
		if versions[ids["v4"]] != "dual" || versions[ids["hop"]] != nil {
			t.Fatalf("an IPv6-literal selected route keeps imported ip-version unchanged: %v", versions)
		}
	}
	s2, _ := Load(t.TempDir())
	if err := s2.Import("r", "", []byte(`proxies: [{name: a, type: socks5, server: node.example.com, port: 1080}, {name: b, type: socks5, server: 203.0.113.10, port: 1080}]`), "r", 0, ""); err != nil {
		t.Fatal(err)
	}
	s2.Selected, s2.Chain = s2.Sources[0].Nodes[1].ID, []string{s2.Sources[0].Nodes[0].ID, s2.Sources[0].Nodes[1].ID}
	if top, versions = runtime(t, s2); top || versions[s2.Sources[0].Nodes[0].ID] != nil {
		t.Fatalf("without IPv6 literals ipv6 stays off and nothing is pinned: %v %v", top, versions)
	}
}
