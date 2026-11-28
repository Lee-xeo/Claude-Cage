package proxyimport

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeSurge(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func surgeNodes(t *testing.T, data []byte) map[string]map[string]any {
	t.Helper()
	var out struct {
		Proxies []map[string]any `json:"proxies"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	nodes := map[string]map[string]any{}
	for _, p := range out.Proxies {
		nodes[p["name"].(string)] = p
	}
	return nodes
}

func TestSurgeConvertsEachProtocol(t *testing.T) {
	dir := t.TempDir()
	writeSurge(t, dir, "all.conf", `[General]
loglevel = notify

[Proxy]
Direct = direct
Trojan = trojan, 203.0.113.1, 443, password="a,b", sni=t.example, skip-cert-verify=true, ws=true, ws-path=/ws, ws-headers=Host:"cdn.example"
SS = ss, 203.0.113.2, 8388, encrypt-method=aes-128-gcm, password=pw, obfs=http, obfs-host=o.example, udp-relay=true
SS-TLS = ss, 203.0.113.3, 443, encrypt-method=aes-256-gcm, password=pw, shadow-tls-password=stpw, shadow-tls-sni=s.example, shadow-tls-version=3
VMess = vmess, 203.0.113.4, 443, username=0b1e2a3c-0000-4000-8000-000000000001, ws=true, tls=true, sni=v.example
HTTP = http, 203.0.113.5, 8080, user, pass
HTTPS = https, 203.0.113.6, 443, username=u, password=p, sni=h.example
SOCKS = socks5, 203.0.113.7, 1080, username=u, password=p
SOCKS-TLS = socks5-tls, 203.0.113.8, 1443, u, p, sni=x.example
Snell = snell, 203.0.113.9, 6160, psk=k, version=3, obfs=tls, obfs-host=n.example
TUIC = tuic-v5, 203.0.113.10, 443, uuid=0b1e2a3c-0000-4000-8000-000000000002, password=p, alpn=h3, sni=q.example
Hy2 = hysteria2, 203.0.113.11, 443, password=p, download-bandwidth=100
AnyTLS = anytls, 203.0.113.12, 443, password=p, sni=a.example
SSH = ssh, 203.0.113.13, 22, username=root, password=p

[Proxy Group]
Select = select, Trojan, SS
`)
	data, warning, err := surgeData(dir, "all.conf")
	if err != nil || warning != "" {
		t.Fatalf("convert: %v %q", err, warning)
	}
	nodes := surgeNodes(t, data)
	if len(nodes) != 13 || nodes["Direct"] != nil {
		t.Fatalf("nodes: %v", reflect.ValueOf(nodes).MapKeys())
	}
	check := func(name string, want map[string]any) {
		t.Helper()
		for key, value := range want {
			got, _ := json.Marshal(nodes[name][key])
			expected, _ := json.Marshal(value)
			if string(got) != string(expected) {
				t.Errorf("%s.%s = %s, want %s", name, key, got, expected)
			}
		}
	}
	check("Trojan", map[string]any{"type": "trojan", "password": "a,b", "sni": "t.example", "skip-cert-verify": true, "network": "ws",
		"ws-opts": map[string]any{"path": "/ws", "headers": map[string]any{"Host": "cdn.example"}}})
	check("SS", map[string]any{"type": "ss", "cipher": "aes-128-gcm", "plugin": "obfs", "plugin-opts": map[string]any{"mode": "http", "host": "o.example"}, "udp": true})
	check("SS-TLS", map[string]any{"plugin": "shadow-tls", "plugin-opts": map[string]any{"host": "s.example", "password": "stpw", "version": 3}})
	check("VMess", map[string]any{"type": "vmess", "uuid": "0b1e2a3c-0000-4000-8000-000000000001", "alterId": 0, "tls": true, "servername": "v.example", "network": "ws"})
	check("HTTP", map[string]any{"type": "http", "username": "user", "password": "pass"})
	check("HTTPS", map[string]any{"type": "http", "tls": true, "sni": "h.example", "username": "u"})
	check("SOCKS", map[string]any{"type": "socks5", "username": "u", "password": "p"})
	check("SOCKS-TLS", map[string]any{"type": "socks5", "tls": true, "username": "u"})
	if _, ok := nodes["SOCKS-TLS"]["sni"]; ok {
		t.Error("SOCKS5 has no server name option")
	}
	check("Snell", map[string]any{"type": "snell", "psk": "k", "version": 3, "obfs-opts": map[string]any{"mode": "tls", "host": "n.example"}})
	check("TUIC", map[string]any{"type": "tuic", "uuid": "0b1e2a3c-0000-4000-8000-000000000002", "alpn": []string{"h3"}, "sni": "q.example"})
	check("Hy2", map[string]any{"type": "hysteria2", "down": "100 Mbps"})
	check("AnyTLS", map[string]any{"type": "anytls", "password": "p", "sni": "a.example"})
	check("SSH", map[string]any{"type": "ssh", "username": "root", "password": "p"})
	check("Trojan", map[string]any{"server": "203.0.113.1", "port": 443})
}

func TestSurgeChainsAndUnsupportedPolicies(t *testing.T) {
	dir := t.TempDir()
	writeSurge(t, dir, "chain.conf", `[Proxy]
Relay = trojan, 203.0.113.20, 443, password=p
Exit = socks5, 203.0.113.21, 1080, username=u, password=p, underlying-proxy=Relay
Via-Group = socks5, 203.0.113.22, 1080, underlying-proxy=Only
WG = wireguard, section-name=Home
Snell5 = snell, 203.0.113.23, 6160, psk=k, version=5
Key = ssh, 203.0.113.24, 22, username=root, private-key=mykey
TLS-Trojan = trojan, 203.0.113.25, 443, password=p, shadow-tls-password=x

[Proxy Group]
Only = select, Relay
`)
	data, warning, err := surgeData(dir, "chain.conf")
	if err != nil {
		t.Fatal(err)
	}
	nodes := surgeNodes(t, data)
	if nodes["Exit"]["dialer-proxy"] != "Relay" || nodes["Via-Group"]["dialer-proxy"] != "Only" || len(nodes) != 3 {
		t.Fatalf("chains: %v", nodes)
	}
	for _, reason := range []string{"WireGuard 暂不支持（1 个）", "Snell v5 暂不支持", "钥匙串中的私钥", "ShadowTLS 只支持与 Shadowsocks 搭配"} {
		if !strings.Contains(warning, reason) {
			t.Errorf("warning lacks %q: %s", reason, warning)
		}
	}
	// The importer resolves a chain through a single-member group to its node.
	var groups struct {
		Groups []map[string]any `json:"proxy-groups"`
	}
	_ = json.Unmarshal(data, &groups)
	if len(groups.Groups) != 1 || groups.Groups[0]["name"] != "Only" {
		t.Fatalf("groups: %v", groups.Groups)
	}
	configs := Scan([]Location{{Client: "Surge", Directory: dir, Layout: Surge}}, []byte("k"))
	if len(configs) != 1 || configs[0].Error != "" || configs[0].Nodes != 3 || configs[0].Skipped != 0 || configs[0].Warning != warning {
		t.Fatalf("scan: %+v", configs)
	}
}

func TestSurgeConvertsVLESS(t *testing.T) {
	dir := t.TempDir()
	writeSurge(t, dir, "vless.conf", `[Proxy]
Reality = vless, 203.0.113.30, 443, username=0b1e2a3c-0000-4000-8000-000000000003, flow=xtls-rprx-vision, sni=www.example.com, reality-public-key=CQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA, reality-short-id=0123456789abcdef, udp-relay=true
WS-TLS = vless, 203.0.113.31, 443, username=0b1e2a3c-0000-4000-8000-000000000004, tls=true, sni=w.example, ws=true, ws-path=/vl
Encrypted = vless, 203.0.113.32, 443, username=0b1e2a3c-0000-4000-8000-000000000005, encryption=mlkem768x25519plus
NoUUID = vless, 203.0.113.33, 443, sni=n.example
`)
	data, warning, err := surgeData(dir, "vless.conf")
	if err != nil {
		t.Fatal(err)
	}
	nodes := surgeNodes(t, data)
	if len(nodes) != 2 {
		t.Fatalf("nodes: %v", reflect.ValueOf(nodes).MapKeys())
	}
	check := func(name string, want map[string]any) {
		t.Helper()
		for key, value := range want {
			got, _ := json.Marshal(nodes[name][key])
			expected, _ := json.Marshal(value)
			if string(got) != string(expected) {
				t.Errorf("%s.%s = %s, want %s", name, key, got, expected)
			}
		}
	}
	check("Reality", map[string]any{"type": "vless", "uuid": "0b1e2a3c-0000-4000-8000-000000000003", "flow": "xtls-rprx-vision",
		"tls": true, "servername": "www.example.com", "client-fingerprint": "chrome", "udp": true,
		"reality-opts": map[string]any{"public-key": "CQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "short-id": "0123456789abcdef"}})
	check("WS-TLS", map[string]any{"type": "vless", "tls": true, "servername": "w.example", "network": "ws", "ws-opts": map[string]any{"path": "/vl"}})
	for _, key := range []string{"reality-opts", "client-fingerprint", "flow"} {
		if _, ok := nodes["WS-TLS"][key]; ok {
			t.Errorf("WS-TLS must not set %s", key)
		}
	}
	for _, reason := range []string{"VLESS encryption 暂不支持（1 个）", "VLESS 缺少 username（UUID）（1 个）"} {
		if !strings.Contains(warning, reason) {
			t.Errorf("warning lacks %q: %s", reason, warning)
		}
	}
	configs := Scan([]Location{{Client: "Surge", Directory: dir, Layout: Surge}}, []byte("k"))
	if len(configs) != 1 || configs[0].Error != "" || configs[0].Nodes != 2 || configs[0].Skipped != 0 {
		t.Fatalf("scan: %+v", configs)
	}
}

func TestSurgeIncludesLocalSectionsAndNeverFetchesRemote(t *testing.T) {
	dir := t.TempDir()
	writeSurge(t, dir, "main.conf", `[Proxy]
#!include nodes.dconf, https://example.invalid/sub.conf
#!include ../outside.conf

[Rule]
#!include rules.dconf
`)
	writeSurge(t, dir, "nodes.dconf", `[Proxy]
#!include main.conf
A = trojan, 203.0.113.30, 443, password=p
[Rule]
B = trojan, 203.0.113.31, 443, password=p
`)
	writeSurge(t, filepath.Dir(dir), "outside.conf", "[Proxy]\nOut = trojan, 203.0.113.32, 443, password=p\n")
	data, warning, err := surgeData(dir, "main.conf")
	if err != nil {
		t.Fatal(err)
	}
	nodes := surgeNodes(t, data)
	if len(nodes) != 1 || nodes["A"] == nil {
		t.Fatalf("only the [Proxy] section of the local include is read: %v", nodes)
	}
	if !strings.Contains(warning, "1 处引用了远程配置") {
		t.Fatalf("remote include not reported: %q", warning)
	}
	writeSurge(t, dir, "remote-only.conf", "[Proxy]\n#!include https://example.invalid/sub.conf\n")
	if _, _, err := surgeData(dir, "remote-only.conf"); err == nil || !strings.Contains(err.Error(), "远程配置") {
		t.Fatalf("remote-only profile: %v", err)
	}
}

func TestSurgeSettingsSelectProfileOnlyInTheirDirectory(t *testing.T) {
	home := t.TempDir()
	chosen := filepath.Join(home, "Chosen")
	settings := filepath.Join(home, "Library", "Application Support", "com.nssurge.surge-mac")
	writeSurge(t, settings, "KDDefaults.plist", `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>UpgradeProfileWizardVersion</key><integer>22</integer>
<key>ConfigDirectoryPath</key><string>`+chosen+`</string>
<key>SelectedConfigName</key><string>main</string>
</dict></plist>`)
	locations := surgeProfileDirectories(home)
	if len(locations) != 3 || locations[0].Directory != chosen || locations[0].Selected != "main.conf" || locations[1].Selected != "" || locations[2].Selected != "" {
		t.Fatalf("locations: %+v", locations)
	}
	node := "[Proxy]\nA = trojan, 203.0.113.40, 443, password=p\n"
	writeSurge(t, chosen, "main.conf", node)
	writeSurge(t, filepath.Join(home, "Library", "Mobile Documents", "iCloud~com~nssurge~inc", "Documents"), "main.conf", node)
	current := 0
	for _, c := range Scan(locations, []byte("k")) {
		if c.Client != "Surge" || c.Name != "main" || c.Error != "" {
			t.Fatalf("config: %+v", c)
		}
		if c.Current {
			current++
		}
	}
	if current != 1 {
		t.Fatalf("selected profile marked current %d times", current)
	}
	// Without readable settings no profile is guessed to be current.
	if dir, name := surgeSettings(filepath.Join(home, "missing.plist")); dir != "" || name != "" {
		t.Fatalf("missing settings: %q %q", dir, name)
	}
	if got := surgeProfileDirectories(t.TempDir()); len(got) != 2 || got[0].Selected != "" {
		t.Fatalf("default locations: %+v", got)
	}
}

func TestSurgeLocationsOnlyOnMac(t *testing.T) {
	for _, platform := range []string{"linux", "windows"} {
		for _, l := range Locations(platform, "/home/u", "", "") {
			if l.Layout == Surge {
				t.Fatalf("%s lists Surge", platform)
			}
		}
	}
	found := false
	for _, l := range Locations("darwin", "/Users/u", "", "") {
		found = found || l.Layout == Surge
	}
	if !found {
		t.Fatal("macOS does not list Surge")
	}
}
