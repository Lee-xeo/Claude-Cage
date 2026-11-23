package proxyimport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/managedproxy"
)

func writeConfig(t *testing.T, root, name, data string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

const testNode = `proxies:
  - {name: Example, type: ss, server: proxy.example, port: 443, cipher: aes-128-gcm, password: private-node-password}
`

func TestScanVergeRuntimeProfilesProvidersAndRedaction(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "clash-verge.yaml", testNode+`proxy-providers:
  local: {path: ./proxy_providers/list.yaml}
  missing: {path: ./not-cached.yaml}
rules: [MATCH,DIRECT]
mixed-port: 8080
secret: private-controller-secret
`)
	writeConfig(t, root, "proxy_providers/list.yaml", `proxies:
  - {name: Residential, type: socks5, server: residential.example, port: 1080, password: private-provider-password}
`)
	writeConfig(t, root, "profiles.yaml", `current: active
items:
  - {uid: active, name: My subscription, type: remote, file: active.yaml, url: 'https://subscription.example/private-subscription-token'}
  - {uid: script, type: script, file: script.js}
  - {uid: escape, type: local, file: '../outside.yaml'}
`)
	writeConfig(t, root, "profiles/active.yaml", testNode)
	writeConfig(t, root, "profiles/script.js", `throw Error('must never execute')`)
	loc := []Location{{"Clash Verge Rev", root, Verge, ""}}
	configs := Scan(loc, []byte("session"))
	if len(configs) != 2 || configs[0].Nodes != 2 || configs[0].Warning == "" || configs[0].Error != "" || !configs[0].Current || !configs[1].Refreshable {
		t.Fatalf("unexpected metadata: %s", mustJSON(t, configs))
	}
	view := mustJSON(t, configs)
	for _, secret := range []string{root, "private-node-password", "private-provider-password", "private-subscription-token", "private-controller-secret", "proxy.example"} {
		if strings.Contains(view, secret) {
			t.Fatalf("display metadata leaked %s", secret)
		}
	}
	if strings.Contains(string(configs[0].Data), "mixed-port") || strings.Contains(string(configs[0].Data), "secret") || strings.Contains(string(configs[0].Data), "rules") {
		t.Fatal("import contains client settings")
	}
	var store managedproxy.Store
	if err := store.Import(configs[0].Name, "", configs[0].Data, configs[0].SourceID, 0, ""); err != nil {
		t.Fatal(err)
	}
	if store.Sources[0].Nodes[1].Config["password"] != "private-provider-password" {
		t.Fatal("provider credentials lost")
	}
	again := Scan(loc, []byte("session"))
	if again[0].ID != configs[0].ID {
		t.Fatal("stable snapshot changed handle")
	}
	if Scan(loc, []byte("other-session"))[0].ID == configs[0].ID {
		t.Fatal("handle not bound to session")
	}
	writeConfig(t, root, "proxy_providers/list.yaml", strings.ReplaceAll(string(configs[0].Data), "private-provider-password", "changed-password"))
	if Scan(loc, []byte("session"))[0].ID == configs[0].ID {
		t.Fatal("updated provider retained stale handle")
	}
}

func TestScanClashPartyRuntimeAndProfiles(t *testing.T) {
	root := t.TempDir()
	// The root config.yaml holds Clash Party app settings, not nodes.
	writeConfig(t, root, "config.yaml", "appTheme: system\nproxies:\n  - {name: Settings, type: ss, server: settings.example, port: 1, cipher: aes-128-gcm, password: x}\n")
	writeConfig(t, root, "work/config.yaml", testNode+`proxy-providers:
  cached: {path: ./proxy_providers/list.yaml}
mixed-port: 7890
secret: private-controller-secret
`)
	writeConfig(t, root, "work/proxy_providers/list.yaml", `proxies:
  - {name: Residential, type: socks5, server: residential.example, port: 1080, password: private-provider-password}
`)
	writeConfig(t, root, "profile.yaml", `current: 19a1b2c3d4e
items:
  - {id: 19a1b2c3d4e, name: My subscription, type: remote, url: 'https://subscription.example/private-subscription-token'}
  - {id: 19a1b2c3d4f, name: Offline, type: local}
  - {id: '../outside', type: local}
  - {id: '..', type: local}
  - {id: script, type: script}
`)
	writeConfig(t, root, "profiles/19a1b2c3d4e.yaml", testNode)
	writeConfig(t, root, "profiles/19a1b2c3d4f.yaml", `proxies:
  - {name: Offline, type: socks5, server: offline.example, port: 1080}
`)
	writeConfig(t, root, "profiles/script.yaml", testNode)
	writeConfig(t, filepath.Dir(root), "outside.yaml", testNode)
	configs := Scan([]Location{{"Clash Party", root, Party, ""}}, []byte("session"))
	if len(configs) != 3 {
		t.Fatalf("unexpected configs: %s", mustJSON(t, configs))
	}
	byName := map[string]Config{}
	for _, c := range configs {
		if c.Error != "" || c.Client != "Clash Party" {
			t.Fatalf("unexpected config: %s", mustJSON(t, c))
		}
		byName[c.Name] = c
	}
	runtime, active, offline := byName["当前生效配置"], byName["My subscription"], byName["Offline"]
	if runtime.Nodes != 2 || !runtime.Current || runtime.Warning != "" || runtime.Refreshable {
		t.Fatalf("runtime config: %s", mustJSON(t, runtime))
	}
	if strings.Contains(string(runtime.Data), "settings.example") || strings.Contains(string(runtime.Data), "mixed-port") || strings.Contains(string(runtime.Data), "private-controller-secret") {
		t.Fatal("runtime import contains app settings")
	}
	if !strings.Contains(string(runtime.Data), "private-provider-password") {
		t.Fatal("provider cache under work/ not read")
	}
	if active.Nodes != 1 || !active.Current || !active.Refreshable || active.URL != "https://subscription.example/private-subscription-token" {
		t.Fatalf("active profile: %s", mustJSON(t, active))
	}
	if offline.Nodes != 1 || offline.Current || offline.Refreshable {
		t.Fatalf("local profile: %s", mustJSON(t, offline))
	}
	view := mustJSON(t, configs)
	for _, secret := range []string{root, "private-subscription-token", "private-provider-password", "private-controller-secret"} {
		if strings.Contains(view, secret) {
			t.Fatalf("display metadata leaked %s", secret)
		}
	}
}

func TestScanRejectsEscapesAndPreservesSSRFields(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeConfig(t, outside, "secret.yaml", testNode)
	if err := os.Symlink(filepath.Join(outside, "secret.yaml"), filepath.Join(root, "escape.yaml")); err == nil {
		entries := Scan([]Location{{"Mihomo", root, Plain, ""}}, []byte("key"))
		if len(entries) != 1 || entries[0].Nodes != 0 || entries[0].Error == "" || len(entries[0].Data) != 0 {
			t.Fatal("followed outside symlink")
		}
	}
	if _, err := readWithin(root, "../secret.yaml"); err == nil {
		t.Fatal("accepted parent traversal")
	}
	writeConfig(t, root, "gui-config.json", `{"localPort":8080,"configs":[{"server":"ssr.example","server_port":443,"password":"ssr-secret","method":"aes-256-cfb","protocol":"auth_aes128_md5","protocolparam":"user:secret","obfs":"tls1.2_ticket_auth","obfsparam":"cdn.example","remarks_base64":"U1NSIG5vZGU"}]}`)
	entries := Scan([]Location{{"ShadowsocksR", root, Plain, ""}}, []byte("key"))
	var entry Config
	for _, e := range entries {
		if e.Name == "gui-config.json" {
			entry = e
		}
	}
	nodes, err := managedproxy.Parse(entry.Data, "ssr")
	if err != nil || len(nodes) != 1 {
		t.Fatal(err, entry.Error)
	}
	n := nodes[0]
	if n.Name != "SSR node" || n.Type != "ssr" || n.Config["protocol-param"] != "user:secret" || n.Config["obfs-param"] != "cdn.example" || n.Config["password"] != "ssr-secret" {
		t.Fatal("SSR parameters lost")
	}
	if strings.Contains(string(entry.Data), "8080") {
		t.Fatal("imported local listening port")
	}
	if _, _, err := nodeData(root, "", []byte(`{"server":"ss.example","server_port":443,"method":"aes-128-gcm","plugin":"unsupported-plugin"}`)); err == nil {
		t.Fatal("silently dropped required plugin")
	}
}

func TestLocationsUsePlatformDataRoots(t *testing.T) {
	for _, test := range []struct{ platform, data string }{{"darwin", "Library/Application Support"}, {"windows", "AppData/Roaming"}, {"linux", ".local/share"}} {
		locations := Locations(test.platform, "/home/person", "", "")
		found := false
		for _, loc := range locations {
			if loc.Client == "Clash Verge Rev" {
				found = true
				if loc.Directory != filepath.Join("/home/person", test.data, "io.github.clash-verge-rev.clash-verge-rev") {
					t.Fatal(test.platform, loc.Directory)
				}
			}
		}
		if !found {
			t.Fatal(test.platform)
		}
	}
	for _, test := range []struct{ platform, data string }{{"darwin", "Library/Application Support"}, {"windows", "AppData/Roaming"}, {"linux", ".local/share"}} {
		found := false
		for _, loc := range Locations(test.platform, "/home/person", "", "") {
			if loc.Client == "Clash Neo" {
				found = loc.Layout == Verge && loc.Directory == filepath.Join("/home/person", test.data, "io.github.clash-neo")
			}
		}
		if !found {
			t.Fatal("missing Clash Neo location on", test.platform)
		}
	}
	for _, test := range []struct{ platform, dir string }{{"darwin", "Library/Application Support"}, {"windows", "AppData/Roaming"}, {"linux", ".config"}} {
		found := false
		for _, loc := range Locations(test.platform, "/home/person", "", "") {
			if loc.Client == "Clash Party" {
				found = loc.Layout == Party && loc.Directory == filepath.Join("/home/person", test.dir, "mihomo-party")
			}
		}
		if !found {
			t.Fatal("missing Clash Party location on", test.platform)
		}
	}
	for _, test := range []struct{ platform, data string }{{"darwin", "Library/Application Support"}, {"windows", "AppData/Roaming"}, {"linux", ".local/share"}} {
		found := false
		for _, loc := range Locations(test.platform, "/home/person", "", "") {
			if loc.Client == "FlClash for oixCloud" && loc.Directory == filepath.Join("/home/person", test.data, "com.oixcloud.clash") {
				found = loc.Layout == OixCloud
			}
		}
		if !found {
			t.Fatal("missing FlClash for oixCloud location on", test.platform)
		}
	}
	locations := Locations("linux", "/home/person", "/custom/data", "/custom/config")
	for _, loc := range locations {
		if loc.Client == "Clash Verge Rev" && loc.Directory != filepath.Join("/custom/data", "io.github.clash-verge-rev.clash-verge-rev") {
			t.Fatal(loc)
		}
		if loc.Client == "FlClash for oixCloud" && loc.Directory != filepath.Join("/custom/data", "com.oixcloud.clash") {
			t.Fatal(loc)
		}
		if loc.Client == "Clash Party" && loc.Directory != filepath.Join("/custom/config", "mihomo-party") {
			t.Fatal(loc)
		}
		if loc.Client == "Clash Verge" && loc.Directory != filepath.Join("/custom/config", "clash-verge") {
			t.Fatal(loc)
		}
	}
}

func TestScanWindowsFlClashFollowClashDirectory(t *testing.T) {
	data := t.TempDir()
	writeConfig(t, filepath.Join(data, "com.follow", "clash"), "config.yaml", testNode)
	writeConfig(t, filepath.Join(data, "com.follow", "clash", "profiles"), "123.yaml", testNode)
	var runtime, profile *Config
	entries := Scan(Locations("windows", t.TempDir(), data, ""), []byte("key"))
	for i, e := range entries {
		if e.Client != "FlClash" {
			continue
		}
		switch e.Name {
		case "config.yaml":
			runtime = &entries[i]
		case "123.yaml":
			profile = &entries[i]
		}
	}
	for _, e := range []*Config{runtime, profile} {
		if e == nil {
			t.Fatalf("missing FlClash config from com.follow/clash: %+v", entries)
		}
		if e.Nodes != 1 || e.Error != "" {
			t.Fatalf("FlClash config not importable: %+v", *e)
		}
	}
}

func TestScanFlClashForOixCloudEncryptedProfiles(t *testing.T) {
	const ageBinary = "age-encryption.org/v1\n-> X25519 private-recipient-marker\nciphertext-body\n--- mac\nbinary-payload"
	const ageArmored = "-----BEGIN AGE ENCRYPTED FILE-----\nYWdlLWVuY3J5cHRpb24ub3JnL3Yx\n-----END AGE ENCRYPTED FILE-----\n"

	oixRoot := t.TempDir()
	// Root config.yaml holds app settings with no nodes, while profiles/*.yaml
	// are age ciphertext (both binary and armored forms).
	writeConfig(t, oixRoot, "config.yaml", "mixed-port: 7890\nmode: rule\n")
	writeConfig(t, oixRoot, "profiles/1001.yaml", ageBinary)
	writeConfig(t, oixRoot, "profiles/1002.yaml", ageArmored)

	// A symlink inside profiles/ pointing outside root must never be read.
	outside := t.TempDir()
	writeConfig(t, outside, "outside-secret.yaml", testNode)
	_ = os.Symlink(filepath.Join(outside, "outside-secret.yaml"), filepath.Join(oixRoot, "profiles", "escape.yaml"))

	// An older readable Clash config on the same machine must remain importable
	// and sort ahead of the non-importable encrypted client notice.
	clashRoot := t.TempDir()
	writeConfig(t, clashRoot, "config.yaml", testNode)

	locs := []Location{
		{"FlClash for oixCloud", oixRoot, OixCloud, ""},
		{"Clash / ClashX", clashRoot, Plain, ""},
	}
	configs := Scan(locs, []byte("session"))
	if len(configs) != 2 {
		t.Fatalf("expected 1 importable Clash config + 1 oixCloud encrypted notice, got %s", mustJSON(t, configs))
	}
	if configs[0].Client != "Clash / ClashX" || configs[0].Error != "" || configs[0].Nodes != 1 {
		t.Fatalf("standard readable config broken or misordered: %s", mustJSON(t, configs[0]))
	}
	oix := configs[1]
	if oix.Client != "FlClash for oixCloud" || oix.Name != "加密配置" || oix.Nodes != 0 || len(oix.Data) != 0 || oix.Refreshable {
		t.Fatalf("unexpected oixCloud entry: %s", mustJSON(t, oix))
	}
	for _, want := range []string{"age", "添加来源", "手动添加"} {
		if !strings.Contains(oix.Error, want) {
			t.Fatalf("oixCloud error missing %q: %q", want, oix.Error)
		}
	}
	view := mustJSON(t, configs)
	for _, secret := range []string{oixRoot, outside, "private-recipient-marker", "ciphertext-body", "1001.yaml", "1002.yaml", "private-node-password"} {
		if strings.Contains(view, secret) {
			t.Fatalf("display metadata leaked %q", secret)
		}
	}
	if Scan(locs, []byte("session"))[1].ID != oix.ID {
		t.Fatal("stable encrypted client notice changed handle")
	}
	if Scan(locs, []byte("other-session"))[1].ID == oix.ID {
		t.Fatal("encrypted client notice handle not bound to session")
	}

	// If a plaintext YAML with nodes is present alongside encrypted profiles,
	// it remains importable while the encrypted profiles still surface cleanly.
	writeConfig(t, oixRoot, "profiles/plain.yaml", testNode)
	withPlain := Scan([]Location{{"FlClash for oixCloud", oixRoot, OixCloud, ""}}, []byte("session"))
	if len(withPlain) != 2 || withPlain[0].Error != "" || withPlain[0].Nodes != 1 || withPlain[1].Name != "加密配置" || withPlain[1].Error == "" {
		t.Fatalf("mixed plaintext and encrypted profiles unexpected: %s", mustJSON(t, withPlain))
	}

	// A symlinked profiles/ directory pointing outside root must be ignored.
	escapeRoot := t.TempDir()
	outsideProfiles := t.TempDir()
	writeConfig(t, outsideProfiles, "secret.yaml", ageBinary)
	if err := os.Symlink(outsideProfiles, filepath.Join(escapeRoot, "profiles")); err == nil {
		escaped := Scan([]Location{{"FlClash for oixCloud", escapeRoot, OixCloud, ""}}, []byte("session"))
		if len(escaped) != 0 {
			t.Fatalf("followed outside profiles symlink: %s", mustJSON(t, escaped))
		}
	}

	// Standard Plain/Verge clients with age-encrypted files also get the clear
	// encrypted reason rather than a generic parse error.
	plainRoot := t.TempDir()
	writeConfig(t, plainRoot, "locked.yaml", ageBinary)
	plain := Scan([]Location{{"FlClash", plainRoot, Plain, ""}}, []byte("session"))
	if len(plain) != 1 || !strings.Contains(plain[0].Error, "age") || !strings.Contains(plain[0].Error, "添加来源") {
		t.Fatalf("standard client age file did not report encrypted reason: %s", mustJSON(t, plain))
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestScanIgnoresUnsupportedNodeDNSAndRetainsSafeDoH(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "http-dns.yaml", testNode+"dns:\n  proxy-server-nameserver: [http://resolver.example.test/dns-query]\n")
	writeConfig(t, root, "fragment-dns.yaml", testNode+"dns:\n  proxy-server-nameserver: ['https://223.5.5.5/dns-query#skip-cert-verify=true', 'https://doh.pub/dns-query#skip-cert-verify=true', 'tls://223.5.5.5#skip-cert-verify=true']\n")
	writeConfig(t, root, "system.yaml", testNode+"dns:\n  proxy-server-nameserver: [system]\n")
	writeConfig(t, root, "doh.yaml", testNode+"dns:\n  proxy-server-nameserver: [https://resolver.example.test/dns-query]\n")
	entries := Scan([]Location{{"Mihomo", root, Plain, ""}}, []byte("key"))
	got := map[string]Config{}
	for _, e := range entries {
		got[e.Name] = e
	}
	for _, name := range []string{"http-dns.yaml", "fragment-dns.yaml", "system.yaml"} {
		accepted := got[name]
		if accepted.Nodes != 1 || accepted.Error != "" {
			t.Fatalf("config %s with unsupported node DNS blocked import: %s", name, mustJSON(t, accepted))
		}
		if strings.Contains(string(accepted.Data), "proxy-server-nameserver") {
			t.Fatalf("config %s retained unsafe node DNS: %s", name, string(accepted.Data))
		}
	}
	if accepted := got["doh.yaml"]; accepted.Nodes != 1 || accepted.Error != "" || !strings.Contains(string(accepted.Data), "https://resolver.example.test/dns-query") {
		t.Fatalf("remote DoH node DNS rejected or dropped: %s", mustJSON(t, accepted))
	}
}

func TestNodeDataPreservesOnlySourceNodeDNS(t *testing.T) {
	input := []byte(`proxies: [{name: node, type: http, server: node.example.test, port: 8080}]
dns:
  proxy-server-nameserver: [https://resolver.example.test:44443/dns-query]
  nameserver: [https://private-business.example.test/dns-query]
  proxy-server-nameserver-policy: {'+.*': https://private-policy.example.test/dns-query}
  listen: 0.0.0.0:53
`)
	data, warning, err := nodeData(t.TempDir(), "", input)
	if err != nil || warning != "" {
		t.Fatal(err, warning)
	}
	s, _ := managedproxy.Load(t.TempDir())
	if err := s.Import("source", "", data, "source", 0, ""); err != nil {
		t.Fatal(err)
	}
	if len(s.Sources[0].NodeDNS) != 1 || s.Sources[0].NodeDNS[0] != "https://resolver.example.test:44443/dns-query" {
		t.Fatal("local import lost subscription node DNS")
	}
	if strings.Contains(string(data), "private-") || strings.Contains(string(data), "listen") {
		t.Fatal("local import copied unrelated subscription DNS")
	}
}

func TestScanVergeImportsConfigsWithLocalOrMixedNodeDNS(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, "clash-verge.yaml", testNode+`dns:
  proxy-server-nameserver:
    - udp://127.0.0.1:7874
`)
	writeConfig(t, root, "profiles.yaml", `current: mixed
items:
  - {uid: mixed, name: MixedDNS, type: remote, file: mixed.yaml, url: 'https://sub.example/token'}
  - {uid: scalar, name: ScalarDNS, type: local, file: scalar.yaml}
`)
	writeConfig(t, root, "profiles/mixed.yaml", testNode+`dns:
  proxy-server-nameserver: [119.29.29.29, 'https://dns.alidns.com/dns-query', 'tls://223.5.5.5', 223.5.5.5]
`)
	writeConfig(t, root, "profiles/scalar.yaml", testNode+"dns: false\n")

	configs := Scan([]Location{{"Clash Verge Rev", root, Verge, ""}}, []byte("session"))
	if len(configs) != 3 {
		t.Fatalf("expected 3 configs, got %s", mustJSON(t, configs))
	}
	byName := map[string]Config{}
	for _, c := range configs {
		if c.Error != "" || c.Nodes != 1 {
			t.Fatalf("config %q failed to import: %s", c.Name, mustJSON(t, c))
		}
		byName[c.Name] = c
	}
	if strings.Contains(string(byName["当前生效配置"].Data), "127.0.0.1") {
		t.Fatal("runtime config retained local UDP DNS")
	}
	if !strings.Contains(string(byName["MixedDNS"].Data), "https://dns.alidns.com/dns-query") || strings.Contains(string(byName["MixedDNS"].Data), "119.29.29.29") || strings.Contains(string(byName["MixedDNS"].Data), "tls://") {
		t.Fatalf("mixed DNS not filtered to remote HTTPS DoH: %s", string(byName["MixedDNS"].Data))
	}
}
