package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

func TestBootstrapDNSReplacementPreflightsCandidateCoreAndPreservesIsolation(t *testing.T) {
	setupManaged := func(t *testing.T, s *Server) (cage.Config, managedproxy.Store) {
		t.Helper()
		store, err := managedproxy.Load(s.profile)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Import("src", "", []byte(`proxies: [{name: node-a, type: socks5, server: node.example.com, port: 1080}]`), "src", 0, ""); err != nil {
			t.Fatal(err)
		}
		store.Selected = store.Sources[0].Nodes[0].ID
		store.Revision = managedproxy.Token()
		store.BootstrapDNS = []string{"https://1.1.1.1/dns-query"}
		store.GuestDNS = []string{"https://1.1.1.1/dns-query"}
		if err := store.Save(s.profile); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadConfig(s.configPath)
		if err != nil {
			t.Fatal(err)
		}
		cfg.ProxyURL = store.Endpoint()
		cfg.ProxyRevision = store.Revision
		cfg.DNS = cage.NewDNSOptions(nil, []string{"https://1.1.1.1/dns-query"}, false)
		cfg.ExitPolicy = cage.ExitPolicy{Mode: "dynamic", Scope: "country", CountryCode: "US"}
		if err := writePrivate(s.configPath, cfg); err != nil {
			t.Fatal(err)
		}
		if err := cage.SaveDNSOptions(s.configPath, cfg.DNS); err != nil {
			t.Fatal(err)
		}
		if err := cfg.RequireManagedProxy(s.profile); err != nil {
			t.Fatalf("managed proxy fixture invalid: %v", err)
		}
		return cfg, store
	}

	const candidateEndpoint = "socks5://cage:onetime-secret@127.0.0.1:19876"

	t.Run("working candidate bootstrap DNS recovers when active proxy fails and reloads running core", func(t *testing.T) {
		s := testServer(t)
		baseCfg, _ := setupManaged(t, s)

		oldPreflight, oldProbe, oldRunning := startPreflightProxy, probeEnvironmentLocation, proxyCoreRunning
		oldValidate, oldStart := validateProxyCore, startProxyCore
		t.Cleanup(func() {
			startPreflightProxy, probeEnvironmentLocation, proxyCoreRunning = oldPreflight, oldProbe, oldRunning
			validateProxyCore, startProxyCore = oldValidate, oldStart
		})

		var preflightStarts, preflightCloses, coreRestarts atomic.Int32
		var preflightBootstrap []string
		var reloadedBootstrap []string
		startPreflightProxy = func(_ context.Context, _ string, candidate managedproxy.Store) (*managedproxy.PreflightProxy, error) {
			preflightStarts.Add(1)
			preflightBootstrap = append([]string(nil), candidate.BootstrapDNS...)
			// Simulate PreflightProxy via exported type cannot set unexported cleanup directly;
			// instead track probe endpoint and close via wrapper test below.
			return &managedproxy.PreflightProxy{Endpoint: candidateEndpoint}, nil
		}
		probeEnvironmentLocation = func(_ context.Context, c cage.Config, _ Preferences) (Location, error) {
			if c.ProxyURL == baseCfg.ProxyURL {
				return Location{}, errors.New("old bootstrap DNS failed TLS handshake")
			}
			if c.ProxyURL != candidateEndpoint {
				t.Fatalf("unexpected probe ProxyURL %q", c.ProxyURL)
			}
			preflightCloses.Add(1)
			return Location{IP: "8.8.8.8", CountryCode: "US", Country: "United States", Timezone: "America/Los_Angeles", CheckedAt: time.Now()}, nil
		}
		proxyCoreRunning = func(context.Context, string, managedproxy.Store) bool { return true }
		validateProxyCore = func(context.Context, string, managedproxy.Store) error { return nil }
		startProxyCore = func(_ context.Context, _ string, p managedproxy.Store) error {
			coreRestarts.Add(1)
			reloadedBootstrap = append([]string(nil), p.BootstrapDNS...)
			return nil
		}

		next := baseCfg
		next.DNS = cage.NewDNSOptions(nil, []string{"https://223.6.6.6/dns-query"}, false)
		if err := s.applyWithOptions(savedProfile{Config: next, Preferences: s.preferences}, true, false); err != nil {
			t.Fatalf("applyWithOptions failed to recover with candidate bootstrap DNS: %v", err)
		}
		if preflightStarts.Load() != 1 || preflightCloses.Load() != 1 {
			t.Fatalf("expected 1 preflight start & probe, got starts=%d probes=%d", preflightStarts.Load(), preflightCloses.Load())
		}
		if !slices.Equal(preflightBootstrap, []string{"https://223.6.6.6/dns-query"}) {
			t.Fatalf("preflight received wrong bootstrap DNS: %v", preflightBootstrap)
		}
		saved, err := loadConfig(s.configPath)
		if err != nil || !slices.Equal(saved.BootstrapEndpoints(), []string{"https://223.6.6.6/dns-query"}) {
			t.Fatalf("candidate bootstrap DNS not persisted: %v %v", saved.BootstrapEndpoints(), err)
		}
		if coreRestarts.Load() != 1 || !slices.Equal(reloadedBootstrap, []string{"https://223.6.6.6/dns-query"}) {
			t.Fatalf("running core not reloaded with saved bootstrap DNS: restarts=%d dns=%v", coreRestarts.Load(), reloadedBootstrap)
		}
	})

	t.Run("unreachable candidate and disallowed country reject save without disturbing active gate or location", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			loc      Location
			probeErr error
		}{
			{"unreachable candidate", Location{}, errors.New("candidate bootstrap timeout")},
			{"disallowed mainland exit", Location{IP: "223.5.5.5", CountryCode: "CN", Country: "China", CheckedAt: time.Now()}, nil},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s, gate := grantedToolUpdateServer(t)
				baseCfg, err := loadConfig(s.configPath)
				if err != nil {
					t.Fatal(err)
				}
				store, err := managedproxy.Load(s.profile)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Import("src", "", []byte(`proxies: [{name: node-a, type: socks5, server: node.example.com, port: 1080}]`), "src", 0, ""); err != nil {
					t.Fatal(err)
				}
				store.Selected = store.Sources[0].Nodes[0].ID
				store.Revision = managedproxy.Token()
				store.BootstrapDNS = []string{"https://1.1.1.1/dns-query"}
				if err := store.Save(s.profile); err != nil {
					t.Fatal(err)
				}
				baseCfg.ProxyURL = store.Endpoint()
				baseCfg.ProxyRevision = store.Revision
				baseCfg.DNS = cage.NewDNSOptions(nil, []string{"https://1.1.1.1/dns-query"}, false)
				baseCfg.ExitPolicy = cage.ExitPolicy{Mode: "dynamic", Scope: "country", CountryCode: "US"}
				if err := writePrivate(s.configPath, baseCfg); err != nil {
					t.Fatal(err)
				}
				if err := cage.SaveDNSOptions(s.configPath, baseCfg.DNS); err != nil {
					t.Fatal(err)
				}
				existingLoc := Location{IP: "8.8.8.8", CountryCode: "US", CheckedAt: time.Now()}
				s.location = &existingLoc
				s.locations = map[string]Location{"8.8.8.8": existingLoc}

				oldPreflight, oldProbe := startPreflightProxy, probeEnvironmentLocation
				t.Cleanup(func() { startPreflightProxy, probeEnvironmentLocation = oldPreflight, oldProbe })
				startPreflightProxy = func(_ context.Context, _ string, _ managedproxy.Store) (*managedproxy.PreflightProxy, error) {
					return &managedproxy.PreflightProxy{Endpoint: candidateEndpoint}, nil
				}
				probeEnvironmentLocation = func(_ context.Context, c cage.Config, _ Preferences) (Location, error) {
					if c.ProxyURL != candidateEndpoint {
						t.Fatalf("probe used active proxy %q instead of preflight", c.ProxyURL)
					}
					return tc.loc, tc.probeErr
				}

				next := baseCfg
				next.DNS = cage.NewDNSOptions(nil, []string{"https://9.9.9.9/dns-query"}, false)
				if err := s.applyWithOptions(savedProfile{Config: next, Preferences: s.preferences}, true, false); err == nil {
					t.Fatal("expected failed candidate preflight to reject save")
				}
				saved, err := loadConfig(s.configPath)
				if err != nil || !slices.Equal(saved.BootstrapEndpoints(), []string{"https://1.1.1.1/dns-query"}) {
					t.Fatalf("failed preflight mutated saved bootstrap DNS: %v %v", saved.BootstrapEndpoints(), err)
				}
				if gate.Status().Mode != cage.GateOpen {
					t.Fatalf("failed candidate preflight closed active guest gate: %s", gate.Status().Mode)
				}
				if s.location == nil || s.location.IP != "8.8.8.8" {
					t.Fatalf("failed candidate preflight mutated active location: %+v", s.location)
				}
			})
		}
	})

	t.Run("settings probe uses candidate bootstrap DNS and ordinary save skips preflight core", func(t *testing.T) {
		s := testServer(t)
		baseCfg, _ := setupManaged(t, s)

		oldPreflight, oldProbe := startPreflightProxy, probeEnvironmentLocation
		t.Cleanup(func() { startPreflightProxy, probeEnvironmentLocation = oldPreflight, oldProbe })

		var preflightCalls atomic.Int32
		startPreflightProxy = func(_ context.Context, _ string, _ managedproxy.Store) (*managedproxy.PreflightProxy, error) {
			preflightCalls.Add(1)
			return &managedproxy.PreflightProxy{Endpoint: candidateEndpoint}, nil
		}
		probeEnvironmentLocation = func(_ context.Context, c cage.Config, _ Preferences) (Location, error) {
			if c.ProxyURL == candidateEndpoint {
				return Location{IP: "8.8.8.8", CountryCode: "US", Country: "United States", Timezone: "America/Los_Angeles", CheckedAt: time.Now()}, nil
			}
			return Location{IP: "1.1.1.1", CountryCode: "US", Country: "United States", Timezone: "America/Los_Angeles", CheckedAt: time.Now()}, nil
		}

		// POST /api/settings/probe with candidate dns_bootstrap_urls uses preflight core.
		view := settingsView(baseCfg, s.preferences)
		view.DNSBootstrapURLs = []string{"https://223.6.6.6/dns-query"}
		body, _ := json.Marshal(view)
		w := request(s, "POST", "/api/settings/probe", s.origin, string(body), true)
		if w.Code != 200 {
			t.Fatalf("probe with candidate bootstrap DNS failed: %d %s", w.Code, w.Body.String())
		}
		var probed Location
		if err := json.Unmarshal(w.Body.Bytes(), &probed); err != nil || probed.IP != "8.8.8.8" {
			t.Fatalf("probe did not use candidate core: %+v %v", probed, err)
		}
		if preflightCalls.Load() != 1 {
			t.Fatalf("expected 1 preflight call for probe, got %d", preflightCalls.Load())
		}

		// Ordinary settings save without changing bootstrap DNS does not launch preflight core.
		ordinary := baseCfg
		ordinary.Timezone = "America/New_York"
		if err := s.applyWithOptions(savedProfile{Config: ordinary, Preferences: s.preferences}, true, false); err != nil {
			t.Fatalf("ordinary save failed: %v", err)
		}
		if preflightCalls.Load() != 1 {
			t.Fatalf("ordinary save unexpectedly started preflight core: calls=%d", preflightCalls.Load())
		}
		saved, err := loadConfig(s.configPath)
		if err != nil || !reflect.DeepEqual(saved.BootstrapEndpoints(), []string{"https://1.1.1.1/dns-query"}) || saved.Timezone != "America/New_York" {
			t.Fatalf("ordinary save wrong result: %+v %v", saved, err)
		}
	})
}
