package managedproxy

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func sourceDNSInput(host string, servers ...string) string {
	data := fmt.Sprintf("proxies: [{name: node, type: http, server: %q, port: 8080}]\n", host)
	if len(servers) > 0 {
		data += "dns:\n  proxy-server-nameserver:\n"
		for _, server := range servers {
			data += fmt.Sprintf("    - %q\n", server)
		}
	}
	return data
}

func TestSubscriptionNodeDNSPersistsAndRefreshes(t *testing.T) {
	profile := t.TempDir()
	s, _ := Load(profile)
	importSource := func(input string) {
		t.Helper()
		if err := s.Import("source", "", []byte(input), "source", 0, ""); err != nil {
			t.Fatal(err)
		}
	}
	input := sourceDNSInput("node.example.test", "https://resolver.example.test:44443/dns-query?token=private-dns-id")
	input += "  nameserver: [https://unwanted.example/dns-query]\n  nameserver-policy: {'+.*': https://unwanted.example/dns-query}\n  proxy-server-nameserver-policy: {'+.*': https://unwanted.example/dns-query}\n  listen: 0.0.0.0:53\nhosts: {node.example.test: 127.0.0.1}\n"
	importSource(input)
	s.Selected = s.Sources[0].Nodes[0].ID
	if err := s.Save(profile); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(profile)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.Sources[0].NodeDNS, loaded.Sources[0].NodeDNS) || s.Selected != loaded.Selected {
		t.Fatal("persisted source DNS or route changed")
	}
	s = loaded
	if os.Getenv("CAGE_PROXY_INTEGRATION") == "1" {
		if err := Validate(context.Background(), profile, loaded); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want []string) {
		t.Helper()
		wire, err := s.Runtime(profile)
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			DNS struct {
				Policy     map[string][]string `yaml:"proxy-server-nameserver-policy"`
				Nameserver []string            `yaml:"nameserver"`
			}
		}
		if err = yaml.Unmarshal(wire, &cfg); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(cfg.DNS.Policy["node.example.test"], want) {
			t.Fatal("refresh retained stale or lost source DNS", cfg.DNS.Policy)
		}
		if strings.Contains(string(wire), "unwanted.example") || strings.Contains(string(wire), "listen:") {
			t.Fatal("subscription changed unrelated DNS settings")
		}
	}
	check(s.Sources[0].NodeDNS)
	importSource(sourceDNSInput("node.example.test", "https://second.example.test/dns-query"))
	check([]string{"https://second.example.test/dns-query"})
	// Mixed real-world subscription resolvers ignore non-HTTP, local, or fragmented entries and keep valid HTTPS DoH.
	importSource(sourceDNSInput("node.example.test", "119.29.29.29", "https://223.5.5.5/dns-query#skip-cert-verify=true", "https://third.example.test/dns-query", "tls://223.5.5.5#skip-cert-verify=true", "udp://127.0.0.1:7874", "system"))
	check([]string{"https://third.example.test/dns-query"})
	// Refreshing with only fragmented/unsafe entries ignores them instead of failing import/refresh and falls back to Cage's default node DNS.
	importSource(sourceDNSInput("node.example.test", "https://resolver.example.test/dns-query#private-route", "https://223.5.5.5/dns-query#skip-cert-verify=true", "tls://223.5.5.5#skip-cert-verify=true"))
	if len(s.Sources[0].NodeDNS) != 0 {
		t.Fatalf("unsafe fragmented resolvers were adopted: %v", s.Sources[0].NodeDNS)
	}
	check(nil)
	importSource(sourceDNSInput("node.example.test"))
	check(nil)
}

func TestSubscriptionNodeDNSIsScopedToExactNodeDomains(t *testing.T) {
	s, _ := Load(t.TempDir())
	s.GuestDNS = []string{"https://9.9.9.9/dns-query"}
	s.BootstrapDNS = []string{"https://223.5.5.5/dns-query"}
	for i, input := range []string{sourceDNSInput("ONE.example.test.", "https://dns-one.example.test/dns-query"), sourceDNSInput("two.example.test", "https://dns-two.example.test/dns-query"), sourceDNSInput("ordinary.example.test"), sourceDNSInput("203.0.113.5", "https://unused.example.test/dns-query")} {
		if err := s.Import(fmt.Sprint(i), "", []byte(input), fmt.Sprint(i), 0, ""); err != nil {
			t.Fatal(err)
		}
	}
	s.Selected = s.Sources[0].Nodes[0].ID
	wire, err := s.Runtime(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	yaml.Unmarshal(wire, &cfg)
	dns := cfg["dns"].(map[string]any)
	want := map[string]any{"one.example.test": []any{"https://dns-one.example.test/dns-query"}, "two.example.test": []any{"https://dns-two.example.test/dns-query"}}
	if !reflect.DeepEqual(dns["proxy-server-nameserver-policy"], want) {
		t.Fatal("source DNS escaped exact domains", dns)
	}
	if !reflect.DeepEqual(dns["default-nameserver"], []any{s.BootstrapDNS[0]}) || !reflect.DeepEqual(dns["proxy-server-nameserver"], []any{s.BootstrapDNS[0]}) || !reflect.DeepEqual(dns["nameserver"], []any{s.GuestDNS[0] + "#" + Group}) || dns["nameserver-policy"] != nil {
		t.Fatal("source DNS changed defaults, bootstrap or business DNS")
	}
	if cfg["hosts"] != nil || cfg["external-controller"] != nil {
		t.Fatal("subscription changed runtime boundary")
	}
}

func TestSubscriptionNodeDNSRejectsCrossSourceConflicts(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, other := range []string{"", "https://other.example.test/dns-query"} {
			t.Run(fmt.Sprintf("%v/%s", reverse, other), func(t *testing.T) {
				inputs := []string{sourceDNSInput("shared.example.test", "https://first.example.test/dns-query"), sourceDNSInput("SHARED.example.test.")}
				if other != "" {
					inputs[1] = sourceDNSInput("SHARED.example.test.", other)
				}
				if reverse {
					inputs[0], inputs[1] = inputs[1], inputs[0]
				}
				s, _ := Load(t.TempDir())
				for i, input := range inputs {
					if err := s.Import(fmt.Sprint(i), "", []byte(input), fmt.Sprint(i), 0, ""); err != nil {
						t.Fatal(err)
					}
				}
				s.Selected = s.Sources[0].Nodes[0].ID
				if _, err := s.Runtime(t.TempDir()); err == nil || !strings.Contains(err.Error(), "不同 DNS") || strings.Contains(err.Error(), "example.test") {
					t.Fatal("conflicting sources silently shared DNS or leaked endpoints", err)
				}
			})
		}
	}
}

func TestSubscriptionNodeDNSValidatesUntrustedEndpoints(t *testing.T) {
	// Real-world Clash/Mihomo resolvers that cannot be safely adopted as bare
	// remote HTTPS DoH (non-HTTP, plain IP, local, credentialed, fragmented
	// such as #skip-cert-verify=true / #DIRECT / #h3=true, malformed) are
	// ignored on import rather than blocking proxy import.
	untrusted := []string{
		"udp://127.0.0.1:7874",
		"119.29.29.29",
		"223.5.5.5:53",
		"223.5.5.5#DIRECT",
		"system",
		"tls://resolver.example",
		"tls://223.5.5.5#skip-cert-verify=true",
		"quic://dns.example",
		"dhcp://en0",
		"http://resolver.example/dns-query",
		"https://localhost/dns-query",
		"https://127.0.0.1/dns-query",
		"https://user:private-password@resolver.example/dns-query",
		"https://223.5.5.5/dns-query#skip-cert-verify=true",
		"https://doh.pub/dns-query#h3=true",
		"https://resolver.example/dns-query#private-route",
		"https://resolver.example:65536/dns-query",
		"https://resolver.example:0/dns-query",
	}
	for _, server := range untrusted {
		t.Run("ignore-import/"+server, func(t *testing.T) {
			s, _ := Load(t.TempDir())
			if err := s.Import("source", "", []byte(sourceDNSInput("node.example.test", server)), "source", 0, ""); err != nil {
				t.Fatal("unsupported Clash resolver blocked import:", err)
			}
			if len(s.Sources) != 1 || len(s.Sources[0].NodeDNS) != 0 {
				t.Fatalf("unsafe resolver was adopted into NodeDNS: %v", s.Sources[0].NodeDNS)
			}
		})
	}
	// Persisted/tampered unsafe NodeDNS endpoints remain strictly rejected at Runtime generation.
	for _, server := range []string{
		"http://resolver.example/dns-query",
		"https://localhost/dns-query",
		"https://127.0.0.1/dns-query",
		"https://user:private-password@resolver.example/dns-query",
		"https://resolver.example/dns-query#private-route",
		"https://resolver.example:65536/dns-query",
		"https://resolver.example:0/dns-query",
	} {
		t.Run("reject-runtime/"+server, func(t *testing.T) {
			s, _ := Load(t.TempDir())
			if err := s.Import("source", "", []byte(sourceDNSInput("node.example.test")), "source", 0, ""); err != nil {
				t.Fatal(err)
			}
			s.Selected = s.Sources[0].Nodes[0].ID
			s.Sources[0].NodeDNS = []string{server}
			if _, err := s.Runtime(t.TempDir()); err == nil || strings.Contains(err.Error(), "private-") {
				t.Fatal("tampered unsafe endpoint accepted or leaked at Runtime", err)
			}
		})
	}
	if filtered := FilterNodeDNS([]string{
		"udp://127.0.0.1:7874",
		"119.29.29.29",
		"tls://223.5.5.5#skip-cert-verify=true",
		"system",
		"https://223.5.5.5/dns-query#skip-cert-verify=true",
		"http://resolver.example.test/dns-query",
		"https://127.0.0.1/dns-query",
		"https://d1.example.test/dns-query",
		"https://d1.example.test/dns-query",
		"https://resolver.example.test/dns-query#DIRECT",
		"https://d2.example.test/dns-query",
		"https://d3.example.test/dns-query",
		"https://d4.example.test/dns-query",
		"https://d5.example.test/dns-query",
	}); len(filtered) != 4 || filtered[0] != "https://d1.example.test/dns-query" || filtered[3] != "https://d4.example.test/dns-query" {
		t.Fatalf("unexpected FilterNodeDNS result: %v", filtered)
	}
	for _, host := range []string{"+.example.test", "*.example.test", "geosite:cn", "example.test,other.test"} {
		t.Run(host, func(t *testing.T) {
			s, _ := Load(t.TempDir())
			if err := s.Import("source", "", []byte(sourceDNSInput(host, "https://resolver.example/dns-query")), "source", 0, ""); err != nil {
				t.Fatal(err)
			}
			s.Selected = s.Sources[0].Nodes[0].ID
			if _, err := s.Runtime(t.TempDir()); err == nil {
				t.Fatal("node server became a wildcard DNS policy")
			}
		})
	}
	// Equal resolver lists may safely share the exact same node domain.
	s, _ := Load(t.TempDir())
	for _, id := range []string{"a", "b"} {
		if err := s.Import(id, "", []byte(sourceDNSInput("same.example.test", "https://dns.example/dns-query")), id, 0, ""); err != nil {
			t.Fatal(err)
		}
	}
	s.Selected = s.Sources[0].Nodes[0].ID
	if _, err := s.Runtime(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}
