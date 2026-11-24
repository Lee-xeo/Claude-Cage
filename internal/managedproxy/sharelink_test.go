package managedproxy

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func vmessLink(t *testing.T, fields map[string]any) string {
	t.Helper()
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return "vmess://" + base64.StdEncoding.EncodeToString(b)
}

func TestShareLinkProtocols(t *testing.T) {
	for _, tc := range []struct {
		name, link string
		want       map[string]any
	}{
		{"trojan", "trojan://pass%40word@trojan.example.com:443?sni=cdn.example.com&allowInsecure=1&type=ws&path=%2Fws&host=cdn.example.com#%F0%9F%87%AD%F0%9F%87%B0%20HK", map[string]any{
			"name": "🇭🇰 HK", "type": "trojan", "server": "trojan.example.com", "port": 443, "password": "pass@word", "sni": "cdn.example.com", "skip-cert-verify": true,
			"network": "ws", "ws-opts": map[string]any{"path": "/ws", "headers": map[string]any{"Host": "cdn.example.com"}}}},
		{"trojan-peer", "trojan://secret@198.51.100.7:8443?peer=sni.example.com#JP", map[string]any{
			"name": "JP", "type": "trojan", "server": "198.51.100.7", "port": 8443, "password": "secret", "sni": "sni.example.com"}},
		{"vless-reality", "vless://0b6c5b04-4f45-4f63-9d2e-6a7d5f3a2c11@reality.example.com:443?encryption=none&security=reality&sni=www.example.org&fp=chrome&pbk=PUBKEY&sid=6ba85179&flow=xtls-rprx-vision&type=tcp#US", map[string]any{
			"name": "US", "type": "vless", "server": "reality.example.com", "port": 443, "uuid": "0b6c5b04-4f45-4f63-9d2e-6a7d5f3a2c11", "tls": true, "servername": "www.example.org",
			"client-fingerprint": "chrome", "flow": "xtls-rprx-vision", "reality-opts": map[string]any{"public-key": "PUBKEY", "short-id": "6ba85179"}}},
		{"vless-grpc", "vless://uuid-1@grpc.example.com:443?security=tls&type=grpc&serviceName=svc&sni=grpc.example.com#SG", map[string]any{
			"name": "SG", "type": "vless", "server": "grpc.example.com", "port": 443, "uuid": "uuid-1", "tls": true, "servername": "grpc.example.com",
			"network": "grpc", "grpc-opts": map[string]any{"grpc-service-name": "svc"}}},
		{"vmess-ws-tls", vmessLink(t, map[string]any{"v": "2", "ps": "TW", "add": "vmess.example.com", "port": "443", "id": "uuid-2", "aid": "0", "scy": "auto", "net": "ws", "type": "none", "host": "cdn.example.com", "path": "/v", "tls": "tls", "sni": "cdn.example.com"}), map[string]any{
			"name": "TW", "type": "vmess", "server": "vmess.example.com", "port": 443, "uuid": "uuid-2", "alterId": 0, "cipher": "auto", "tls": true, "servername": "cdn.example.com",
			"network": "ws", "ws-opts": map[string]any{"path": "/v", "headers": map[string]any{"Host": "cdn.example.com"}}}},
		{"vmess-numeric-port", vmessLink(t, map[string]any{"ps": "KR", "add": "203.0.113.5", "port": 10086, "id": "uuid-3", "aid": 64, "net": "tcp"}), map[string]any{
			"name": "KR", "type": "vmess", "server": "203.0.113.5", "port": 10086, "uuid": "uuid-3", "alterId": 64, "cipher": "auto"}},
		{"ss-sip002", "ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:ss-pass")) + "@ss.example.com:8388#SS%201", map[string]any{
			"name": "SS 1", "type": "ss", "server": "ss.example.com", "port": 8388, "cipher": "aes-256-gcm", "password": "ss-pass"}},
		{"ss-2022-plain", "ss://2022-blake3-aes-128-gcm:YWJjZGVmZ2hpamtsbW5vcA%3D%3D@203.0.113.9:443#2022", map[string]any{
			"name": "2022", "type": "ss", "server": "203.0.113.9", "port": 443, "cipher": "2022-blake3-aes-128-gcm", "password": "YWJjZGVmZ2hpamtsbW5vcA=="}},
		{"ss-legacy", "ss://" + base64.StdEncoding.EncodeToString([]byte("chacha20-ietf-poly1305:legacy@legacy.example.com:8000")) + "#Legacy", map[string]any{
			"name": "Legacy", "type": "ss", "server": "legacy.example.com", "port": 8000, "cipher": "chacha20-ietf-poly1305", "password": "legacy"}},
		{"ss-obfs", "ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:p")) + "@obfs.example.com:80/?plugin=obfs-local%3Bobfs%3Dhttp%3Bobfs-host%3Dcdn.example.com#Obfs", map[string]any{
			"name": "Obfs", "type": "ss", "server": "obfs.example.com", "port": 80, "cipher": "aes-128-gcm", "password": "p", "plugin": "obfs", "plugin-opts": map[string]any{"mode": "http", "host": "cdn.example.com"}}},
		{"ss-obfs-shadowrocket-raw-semicolons", "ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:p")) + "@obfs.example.com:80?plugin=obfs-local;obfs=http;obfs-host=cdn.example.com#Obfs", map[string]any{
			"name": "Obfs", "type": "ss", "server": "obfs.example.com", "port": 80, "cipher": "aes-128-gcm", "password": "p", "plugin": "obfs", "plugin-opts": map[string]any{"mode": "http", "host": "cdn.example.com"}}},
		{"ss-obfs-with-other-params", "ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:p")) + "@obfs.example.com:80/?foo=bar&plugin=simple-obfs;obfs=tls;obfs-host=cdn.example.com&baz=1#Obfs", map[string]any{
			"name": "Obfs", "type": "ss", "server": "obfs.example.com", "port": 80, "cipher": "aes-128-gcm", "password": "p", "plugin": "obfs", "plugin-opts": map[string]any{"mode": "tls", "host": "cdn.example.com"}}},
		{"ss-obfs-then-other-params", "ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:p")) + "@obfs.example.com:80?plugin=obfs-local;obfs=http;obfs-host=cdn.example.com&foo=bar#Obfs", map[string]any{
			"name": "Obfs", "type": "ss", "server": "obfs.example.com", "port": 80, "cipher": "aes-128-gcm", "password": "p", "plugin": "obfs", "plugin-opts": map[string]any{"mode": "http", "host": "cdn.example.com"}}},
		{"ss-empty-plugin", "ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:p")) + "@ss.example.com:8388/?plugin=&foo=bar#Plain", map[string]any{
			"name": "Plain", "type": "ss", "server": "ss.example.com", "port": 8388, "cipher": "aes-128-gcm", "password": "p"}},
		{"ss-no-plugin-other-params", "ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:p")) + "@ss.example.com:8388/?foo=bar#Plain", map[string]any{
			"name": "Plain", "type": "ss", "server": "ss.example.com", "port": 8388, "cipher": "aes-128-gcm", "password": "p"}},
		{"ss-legacy-obfs-raw-semicolons", "ss://" + base64.StdEncoding.EncodeToString([]byte("aes-128-gcm:p@legacy.example.com:80")) + "?plugin=obfs-local;obfs=http;obfs-host=cdn.example.com#Legacy", map[string]any{
			"name": "Legacy", "type": "ss", "server": "legacy.example.com", "port": 80, "cipher": "aes-128-gcm", "password": "p", "plugin": "obfs", "plugin-opts": map[string]any{"mode": "http", "host": "cdn.example.com"}}},
		{"hysteria2", "hysteria2://auth-pass@hy.example.com:443/?sni=hy.example.com&insecure=1&obfs=salamander&obfs-password=ob&mport=20000-30000#HY2", map[string]any{
			"name": "HY2", "type": "hysteria2", "server": "hy.example.com", "port": 443, "password": "auth-pass", "sni": "hy.example.com", "skip-cert-verify": true,
			"obfs": "salamander", "obfs-password": "ob", "mport": "20000-30000"}},
		{"hy2-alias", "hy2://pw@198.51.100.20:8443#Alias", map[string]any{
			"name": "Alias", "type": "hysteria2", "server": "198.51.100.20", "port": 8443, "password": "pw"}},
		{"tuic", "tuic://uuid-4:tuic-pass@tuic.example.com:443?congestion_control=bbr&udp_relay_mode=native&alpn=h3&sni=tuic.example.com&allow_insecure=0#TUIC", map[string]any{
			"name": "TUIC", "type": "tuic", "server": "tuic.example.com", "port": 443, "uuid": "uuid-4", "password": "tuic-pass", "congestion-controller": "bbr",
			"udp-relay-mode": "native", "alpn": []string{"h3"}, "sni": "tuic.example.com"}},
		{"anytls", "anytls://any-pass@any.example.com:443?sni=any.example.com&fp=chrome#AnyTLS", map[string]any{
			"name": "AnyTLS", "type": "anytls", "server": "any.example.com", "port": 443, "password": "any-pass", "sni": "any.example.com", "client-fingerprint": "chrome"}},
		{"unnamed", "trojan://pw@203.0.113.1:443", map[string]any{
			"name": "trojan 203.0.113.1:443", "type": "trojan", "server": "203.0.113.1", "port": 443, "password": "pw"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseShareLink(tc.link)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got  %#v\nwant %#v", got, tc.want)
			}
		})
	}
}

func TestShareLinkRejectsAmbiguousEntries(t *testing.T) {
	for _, tc := range []struct{ name, link, reason string }{
		{"ssr", "ssr://c2VjcmV0", "协议暂不支持"},
		{"socks", "socks://dXNlcjpwYXNz@203.0.113.1:1080#S", "协议暂不支持"},
		{"vless-xhttp", "vless://u@h.example.com:443?security=tls&type=xhttp#X", "传输方式暂不支持"},
		{"vless-kcp", "vless://u@h.example.com:443?type=kcp#K", "传输方式暂不支持"},
		{"vless-encryption", "vless://u@h.example.com:443?encryption=mlkem768x25519plus#E", "encryption"},
		{"vless-reality-no-key", "vless://u@h.example.com:443?security=reality#R", "公钥"},
		{"trojan-http-header", "trojan://p@h.example.com:443?type=tcp&headerType=http#H", "伪装头"},
		{"vmess-not-json", "vmess://not-base64!!", "v2rayN"},
		{"vmess-quic", vmessLink(t, map[string]any{"add": "h.example.com", "port": 443, "id": "u", "net": "quic"}), "传输方式暂不支持"},
		{"ss-v2ray-plugin", "ss://YWVzLTEyOC1nY206cA@h.example.com:443/?plugin=v2ray-plugin%3Bmode%3Dwebsocket#V", "插件暂不支持"},
		{"ss-v2ray-plugin-raw-semicolons", "ss://YWVzLTEyOC1nY206cA@h.example.com:443?plugin=v2ray-plugin;mode=websocket;host=h.example.com#V", "插件暂不支持"},
		{"ss-obfs-raw-bad-mode", "ss://YWVzLTEyOC1nY206cA@h.example.com:80?plugin=obfs-local;obfs=bogus#O", "obfs 模式无效"},
		{"ss-plugin-bad-escape", "ss://YWVzLTEyOC1nY206cA@h.example.com:80?plugin=obfs-local%3Bobfs%3Dhttp%ZZ#O", "插件参数无效"},
		{"tuic-v4-token", "tuic://token@h.example.com:443#T", "uuid:password"},
		{"no-port", "trojan://p@h.example.com#N", "格式无效"},
		{"bad-port", "anytls://p@h.example.com:70000#N", "格式无效"},
		{"no-credential", "anytls://h.example.com:443#N", "格式无效"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseShareLink(tc.link)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("error = %v, want %q", err, tc.reason)
			}
		})
	}
}

func TestShareLinkSubscriptionImportsPlainAndBase64(t *testing.T) {
	list := strings.Join([]string{
		"trojan://secret-one@203.0.113.1:443?sni=a.example.com#A",
		"",
		"ssr://c2VjcmV0LXNzcg",
		"vless://uuid-x@203.0.113.2:443?type=xhttp#Unsupported-transport",
		"hysteria2://secret-two@203.0.113.3:443/?mport=bad-range#Bad-ports",
		"anytls://secret-three@203.0.113.4:443#B",
	}, "\r\n")
	for name, data := range map[string]string{
		"plain":      list,
		"base64":     base64.StdEncoding.EncodeToString([]byte(list)),
		"base64-raw": base64.RawURLEncoding.EncodeToString([]byte(list)),
		"wrapped":    wrap(base64.StdEncoding.EncodeToString([]byte(list)), 76),
	} {
		t.Run(name, func(t *testing.T) {
			nodes, skipped, err := parseNodes([]byte(data), "links")
			if err != nil {
				t.Fatal(err)
			}
			if len(nodes) != 2 || nodes[0].Name != "A" || nodes[1].Name != "B" || nodes[0].Config["password"] != "secret-one" {
				t.Fatalf("nodes = %+v", nodes)
			}
			if len(skipped) != 3 {
				t.Fatalf("every unusable entry must be reported: %+v", skipped)
			}
			report, _ := json.Marshal(skipped)
			for _, secret := range []string{"secret-ssr", "c2VjcmV0", "uuid-x", "secret-two", "203.0.113"} {
				if strings.Contains(string(report), secret) {
					t.Fatalf("skip report leaked link data %q: %s", secret, report)
				}
			}
			if !strings.Contains(string(report), "Unsupported-transport") || !strings.Contains(string(report), "协议暂不支持") {
				t.Fatalf("skip report lost names or reasons: %s", report)
			}
		})
	}
}

func TestShareLinkSingleNodePaste(t *testing.T) {
	nodes, _, err := parseNodes([]byte("  vless://uuid-y@203.0.113.8:443?security=tls&sni=v.example.com#Pasted \n"), "paste")
	if err != nil || len(nodes) != 1 || nodes[0].Type != "vless" || nodes[0].Config["servername"] != "v.example.com" {
		t.Fatalf("pasted link = %+v, %v", nodes, err)
	}
}

func TestNonLinkContentKeepsYAMLError(t *testing.T) {
	for _, data := range []string{"not: [valid", "aGVsbG8gd29ybGQ=", "<html>login</html>", "{\"outbounds\": []}"} {
		if _, _, err := parseNodes([]byte(data), "x"); err == nil {
			t.Fatalf("%q imported", data)
		}
	}
}

func wrap(s string, n int) string {
	var b strings.Builder
	for len(s) > n {
		b.WriteString(s[:n] + "\n")
		s = s[n:]
	}
	b.WriteString(s)
	return b.String()
}

// Common link forms that were wrongly rejected: v2rayN's gRPC mode in the
// VMess "type" field, and the Hysteria 2 URI's multi-port and default port.
func TestShareLinkAcceptsCommonTransportAndPortForms(t *testing.T) {
	for _, tc := range []struct {
		name, link string
		want       map[string]any
	}{
		{"vmess-grpc-gun", vmessLink(t, map[string]any{"ps": "G", "add": "g.example.com", "port": "443", "id": "u", "net": "grpc", "type": "gun", "path": "svc", "tls": "tls"}), map[string]any{
			"name": "G", "type": "vmess", "server": "g.example.com", "port": 443, "uuid": "u", "alterId": 0, "cipher": "auto", "tls": true,
			"network": "grpc", "grpc-opts": map[string]any{"grpc-service-name": "svc"}}},
		{"vmess-grpc-multi", vmessLink(t, map[string]any{"ps": "M", "add": "g.example.com", "port": 443, "id": "u", "net": "grpc", "type": "multi", "path": "svc", "tls": "tls"}), map[string]any{
			"name": "M", "type": "vmess", "server": "g.example.com", "port": 443, "uuid": "u", "alterId": 0, "cipher": "auto", "tls": true,
			"network": "grpc", "grpc-opts": map[string]any{"grpc-service-name": "svc"}}},
		{"hysteria2-multi-port", "hysteria2://pw@hy.example.com:443,5000-6000/?sni=hy.example.com#Multi", map[string]any{
			"name": "Multi", "type": "hysteria2", "server": "hy.example.com", "port": 443, "password": "pw", "sni": "hy.example.com", "ports": "443,5000-6000"}},
		{"hysteria2-range-only", "hy2://pw@198.51.100.20:20000-30000#Range", map[string]any{
			"name": "Range", "type": "hysteria2", "server": "198.51.100.20", "port": 20000, "password": "pw", "ports": "20000-30000"}},
		{"hysteria2-default-port", "hysteria2://pw@hy.example.com/?sni=hy.example.com#Default", map[string]any{
			"name": "Default", "type": "hysteria2", "server": "hy.example.com", "port": 443, "password": "pw", "sni": "hy.example.com"}},
		{"hysteria2-default-port-no-path", "hysteria2://pw@hy.example.com#Bare", map[string]any{
			"name": "Bare", "type": "hysteria2", "server": "hy.example.com", "port": 443, "password": "pw"}},
		{"hysteria2-ipv6-multi-port", "hysteria2://pw@[2001:db8::1]:443,8443#V6", map[string]any{
			"name": "V6", "type": "hysteria2", "server": "2001:db8::1", "port": 443, "password": "pw", "ports": "443,8443"}},
		{"hysteria2-ipv6-default-port", "hysteria2://pw@[2001:db8::1]/#V6D", map[string]any{
			"name": "V6D", "type": "hysteria2", "server": "2001:db8::1", "port": 443, "password": "pw"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseShareLink(tc.link)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got  %#v\nwant %#v", got, tc.want)
			}
		})
	}
	for _, tc := range []struct{ name, link, reason string }{
		{"vmess-tcp-http-header", vmessLink(t, map[string]any{"add": "h.example.com", "port": 443, "id": "u", "net": "tcp", "type": "http"}), "伪装头"},
		{"vmess-ws-gun-is-header", vmessLink(t, map[string]any{"add": "h.example.com", "port": 443, "id": "u", "net": "ws", "type": "gun"}), "伪装头"},
		{"hysteria2-bad-range", "hysteria2://pw@hy.example.com:443,70000-80000#Bad", "格式无效"},
		{"hysteria2-no-auth", "hysteria2://hy.example.com:443,5000-6000#NoAuth", "格式无效"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseShareLink(tc.link)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("error = %v, want %q", err, tc.reason)
			}
		})
	}
}

func TestShareLinkHysteria2MultiPortSurvivesImport(t *testing.T) {
	nodes, _, err := parseNodes([]byte("hysteria2://pw@hy.example.com:443,5000-6000/?mport=443,5000-6000#Multi\nhy2://pw@hy.example.com#Default\n"), "paste")
	if err != nil || len(nodes) != 2 || nodes[0].Config["ports"] != "443,5000-6000" || nodes[0].Config["port"] != 443 || nodes[1].Config["port"] != 443 {
		t.Fatalf("imported = %+v, %v", nodes, err)
	}
}
