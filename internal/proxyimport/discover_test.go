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
	loc := []Location{{"Clash Verge Rev", root, true}}
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

func TestScanRejectsEscapesAndPreservesSSRFields(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeConfig(t, outside, "secret.yaml", testNode)
	if err := os.Symlink(filepath.Join(outside, "secret.yaml"), filepath.Join(root, "escape.yaml")); err == nil {
		entries := Scan([]Location{{"Mihomo", root, false}}, []byte("key"))
		if len(entries) != 1 || entries[0].Nodes != 0 || entries[0].Error == "" || len(entries[0].Data) != 0 {
			t.Fatal("followed outside symlink")
		}
	}
	if _, err := readWithin(root, "../secret.yaml"); err == nil {
		t.Fatal("accepted parent traversal")
	}
	writeConfig(t, root, "gui-config.json", `{"localPort":8080,"configs":[{"server":"ssr.example","server_port":443,"password":"ssr-secret","method":"aes-256-cfb","protocol":"auth_aes128_md5","protocolparam":"user:secret","obfs":"tls1.2_ticket_auth","obfsparam":"cdn.example","remarks_base64":"U1NSIG5vZGU"}]}`)
	entries := Scan([]Location{{"ShadowsocksR", root, false}}, []byte("key"))
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
	if _, _, err := nodeData(root, []byte(`{"server":"ss.example","server_port":443,"method":"aes-128-gcm","plugin":"unsupported-plugin"}`)); err == nil {
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
	locations := Locations("linux", "/home/person", "/custom/data", "/custom/config")
	for _, loc := range locations {
		if loc.Client == "Clash Verge Rev" && loc.Directory != "/custom/data/io.github.clash-verge-rev.clash-verge-rev" {
			t.Fatal(loc)
		}
		if loc.Client == "Clash Verge" && loc.Directory != "/custom/config/clash-verge" {
			t.Fatal(loc)
		}
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
