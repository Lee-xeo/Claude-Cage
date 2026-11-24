package workbench

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/managedproxy"
)

func TestDNSSettingsPersistBesideConfigAndSurviveLegacyClients(t *testing.T) {
	s := testServer(t)
	if !strings.HasPrefix(s.configPath, os.TempDir()) {
		t.Fatalf("config %q outside the test temp dir", s.configPath)
	}
	cfg, err := cage.Load(s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	view := settingsView(cfg, s.preferences)
	if view.DNSFallbackURLs == nil || view.DNSBootstrapURLs == nil || view.DNSPublicFallback {
		t.Fatalf("defaults must be empty lists with the public fallback off: %+v", view)
	}
	view.DNSFallbackURLs = []string{"https://dns.example/dns-query"}
	view.DNSBootstrapURLs = []string{"https://223.5.5.5/dns-query"}
	view.DNSPublicFallback = true
	next, prefs, err := proposedSettings(view, cfg, s.profile)
	if err != nil {
		t.Fatal(err)
	}
	s.ready = true
	if err := s.apply(savedProfile{Config: next, Preferences: prefs}, false); err != nil {
		t.Fatal(err)
	}
	loaded, err := cage.Load(s.configPath)
	if err != nil || loaded.DNS != next.DNS {
		t.Fatalf("options not saved: %+v %v", loaded.DNS, err)
	}
	if got := loaded.DNSEndpoints(); len(got) != 3 || got[2] != cage.PublicDNSFallback {
		t.Fatalf("endpoints: %v", got)
	}
	raw, _ := os.ReadFile(s.configPath)
	if strings.Contains(string(raw), "dns.example") {
		t.Fatal("options written into the strict configuration")
	}
	// A client that predates these fields leaves them as saved.
	body, _ := json.Marshal(settingsView(loaded, s.preferences))
	var legacy map[string]any
	_ = json.Unmarshal(body, &legacy)
	delete(legacy, "dns_fallback_urls")
	delete(legacy, "dns_public_fallback")
	delete(legacy, "dns_bootstrap_urls")
	body, _ = json.Marshal(legacy)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/settings", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	v, ok := s.proposed(w, r)
	if !ok || v.Config.DNS != next.DNS {
		t.Fatalf("legacy client erased DNS options: ok=%v %+v %s", ok, v.Config.DNS, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(s.profile, cage.DNSOptionsFile)); err != nil {
		t.Fatal(err)
	}
}

func TestSettingsDNSCheckRouteAndAgentCapabilityAreReadOnly(t *testing.T) {
	s := testServer(t)
	store, err := managedproxy.Load(s.profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Import("src", "", []byte(`proxies: [{name: node-a, type: socks5, server: 203.0.113.10, port: 1080}]`), "src", 0, ""); err != nil {
		t.Fatal(err)
	}
	store.Selected = store.Sources[0].Nodes[0].ID
	store.Revision = managedproxy.Token()
	if err := store.Save(s.profile); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ProxyURL = store.Endpoint()
	cfg.ProxyRevision = store.Revision
	if err := writePrivate(s.configPath, cfg); err != nil {
		t.Fatal(err)
	}

	oldCheck := checkDNSFunc
	t.Cleanup(func() { checkDNSFunc = oldCheck })
	var probedEndpoints []string
	checkDNSFunc = func(_ context.Context, c cage.Config) (cage.DNSCheckResult, error) {
		probedEndpoints = append([]string(nil), c.DNSEndpoints()...)
		return cage.DNSCheckResult{
			Target: cage.DefaultDNSCheckTarget,
			OK:     true,
			Endpoints: []cage.DNSEndpointCheck{
				{Host: "dns.quad9.net", Role: "primary", Status: cage.DNSCheckOK, Addresses: []string{"151.101.2.132"}},
				{Host: "8.8.8.8", Role: "public_fallback", Status: cage.DNSCheckConnection, Detail: "tls"},
			},
		}, nil
	}

	draft := settingsView(cfg, s.preferences)
	draft.DNSURL = "https://dns.quad9.net/dns-query"
	draft.DNSFallbackURLs = []string{"https://dns.adguard-dns.com/dns-query"}
	draft.DNSPublicFallback = true
	body, _ := json.Marshal(draft)

	w := request(s, "POST", "/api/settings/dns-check", s.origin, string(body), true)
	if w.Code != 200 {
		t.Fatalf("POST /api/settings/dns-check status=%d body=%s", w.Code, w.Body.String())
	}
	var got cage.DNSCheckResult
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || got.Target != cage.DefaultDNSCheckTarget || len(got.Endpoints) != 2 {
		t.Fatalf("unexpected DNS check response: %+v", got)
	}
	wantEndpoints := []string{"https://dns.quad9.net/dns-query", "https://dns.adguard-dns.com/dns-query", cage.PublicDNSFallback}
	if !slices.Equal(probedEndpoints, wantEndpoints) {
		t.Fatalf("probed endpoints=%v want=%v", probedEndpoints, wantEndpoints)
	}
	// Read-only check must never modify persisted config.json or dns.json.
	after, err := loadConfig(s.configPath)
	if err != nil || after.DNSURL != cfg.DNSURL || after.DNS != cfg.DNS {
		t.Fatalf("dns-check mutated saved settings: %+v %v", after, err)
	}

	// Invalid DoH URL in draft is rejected with 400 before invoking checkDNSFunc.
	bad := draft
	bad.DNSURL = "http://insecure.example/dns-query"
	badBody, _ := json.Marshal(bad)
	if w := request(s, "POST", "/api/settings/dns-check", s.origin, string(badBody), true); w.Code != 400 {
		t.Fatalf("invalid DoH URL not rejected: %d %s", w.Code, w.Body.String())
	}

	// Verify settings.dns_check is registered in agent operations.
	var found bool
	for _, op := range AgentOperations() {
		if op.Name == "settings.dns_check" && op.path == "/api/settings/dns-check" && op.method == "POST" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("settings.dns_check missing from AgentOperations")
	}
}
