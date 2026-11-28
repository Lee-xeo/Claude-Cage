package proxyimport

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Surge keeps its profiles as INI-style .conf files. Its [Proxy] lines are
// "Name = type, server, port, key=value, ..." and [Proxy Group] lines list
// member policies. A profile may pull a section from another local file with
// "#!include", or from a URL; URLs are never fetched here.

// surgeIncludeDepth bounds nested local #!include lines.
const surgeIncludeDepth = 4

// surgeProfileDirectories returns where Surge for Mac keeps profiles: the
// directory chosen in its settings, with the profile it selects, then the
// default and iCloud folders.
func surgeProfileDirectories(home string) []Location {
	var out []Location
	if dir, name := surgeSettings(filepath.Join(home, "Library", "Application Support", "com.nssurge.surge-mac", "KDDefaults.plist")); dir != "" {
		selected := ""
		if name != "" {
			selected = name + ".conf"
		}
		out = append(out, Location{Client: "Surge", Directory: dir, Layout: Surge, Selected: selected})
	}
	// Scan skips a folder already read above, so the profile it selects stays
	// marked only where Surge actually reads it.
	for _, dir := range []string{
		filepath.Join(home, "Library", "Application Support", "Surge", "Profiles"),
		filepath.Join(home, "Library", "Mobile Documents", "iCloud~com~nssurge~inc", "Documents"),
	} {
		out = append(out, Location{Client: "Surge", Directory: dir, Layout: Surge})
	}
	return out
}

// surgeSettings reads the profile directory and selected profile name from
// Surge's XML property list. Values other than plain strings are ignored.
func surgeSettings(path string) (dir, name string) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 1<<20 {
		return "", ""
	}
	var plist struct {
		Dict struct {
			Items []struct {
				XMLName xml.Name
				Value   string `xml:",chardata"`
			} `xml:",any"`
		} `xml:"dict"`
	}
	if xml.Unmarshal(data, &plist) != nil {
		return "", ""
	}
	items := plist.Dict.Items
	for i := 0; i+1 < len(items); i++ {
		if items[i].XMLName.Local != "key" || items[i+1].XMLName.Local != "string" {
			continue
		}
		switch strings.TrimSpace(items[i].Value) {
		case "ConfigDirectoryPath":
			if v := strings.TrimSpace(items[i+1].Value); filepath.IsAbs(v) {
				dir = v
			}
		case "SelectedConfigName":
			if v := strings.TrimSpace(items[i+1].Value); v != "" && filepath.Base(v) == v && !strings.ContainsAny(v, `/\`) {
				name = v
			}
		}
	}
	return dir, name
}

type surgeSections map[string][]string

// surgeRead collects the [Proxy] and [Proxy Group] lines of a profile,
// following local #!include lines within root.
func surgeRead(root, file string, depth int, seen map[string]bool, remote *int) (surgeSections, error) {
	if depth > surgeIncludeDepth || seen[file] {
		return surgeSections{}, nil
	}
	seen[file] = true
	data, err := readWithin(root, file)
	if err != nil {
		return nil, err
	}
	out := surgeSections{}
	section := ""
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if strings.HasPrefix(line, "#!include") {
			if section != "Proxy" && section != "Proxy Group" {
				continue
			}
			for _, target := range strings.Split(strings.TrimSpace(strings.TrimPrefix(line, "#!include")), ",") {
				target = strings.TrimSpace(target)
				if target == "" {
					continue
				}
				if strings.Contains(target, "://") {
					*remote++
					continue
				}
				if !filepath.IsLocal(target) {
					continue
				}
				included, err := surgeRead(root, target, depth+1, seen, remote)
				if err == nil {
					out[section] = append(out[section], included[section]...)
				}
			}
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "//") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		if section == "Proxy" || section == "Proxy Group" {
			out[section] = append(out[section], line)
		}
	}
	return out, nil
}

// surgeFields splits a policy definition at commas outside double quotes and
// removes the quotes around values.
func surgeFields(s string) []string {
	var fields []string
	var b strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
		case r == ',' && !quoted:
			fields = append(fields, strings.TrimSpace(b.String()))
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	return append(fields, strings.TrimSpace(b.String()))
}

// surgeLine splits "Name = definition" and the definition's positional and
// key=value fields.
func surgeLine(line string) (name string, positional []string, params map[string]string, ok bool) {
	name, rhs, found := strings.Cut(line, "=")
	name = strings.TrimSpace(name)
	if !found || name == "" {
		return "", nil, nil, false
	}
	params = map[string]string{}
	for i, field := range surgeFields(rhs) {
		if key, value, isParam := strings.Cut(field, "="); isParam && i > 0 {
			params[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
			continue
		}
		positional = append(positional, field)
	}
	return name, positional, params, len(positional) > 0
}

func surgeBool(v string) bool {
	return strings.EqualFold(v, "true") || v == "1"
}

// surgeWebSocket adds Surge's ws, ws-path and ws-headers to a node.
func surgeWebSocket(node map[string]any, params map[string]string) {
	if !surgeBool(params["ws"]) {
		return
	}
	opts := map[string]any{}
	if path := params["ws-path"]; path != "" {
		opts["path"] = path
	}
	if raw := params["ws-headers"]; raw != "" {
		headers := map[string]any{}
		for _, header := range strings.Split(raw, "|") {
			if key, value, ok := strings.Cut(header, ":"); ok {
				headers[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"`)
			}
		}
		if len(headers) > 0 {
			opts["headers"] = headers
		}
	}
	node["network"] = "ws"
	node["ws-opts"] = opts
}

// surgeTLS adds the TLS parameters Surge shares between protocols.
func surgeTLS(node map[string]any, params map[string]string, serverNameKey string) {
	if sni := params["sni"]; sni != "" && !strings.EqualFold(sni, "off") {
		node[serverNameKey] = sni
	}
	if surgeBool(params["skip-cert-verify"]) {
		node["skip-cert-verify"] = true
	}
	if fp := params["server-cert-fingerprint-sha256"]; fp != "" {
		node["fingerprint"] = fp
	}
}

// surgeNode converts one [Proxy] definition. A non-empty reason explains why
// the policy cannot be imported; ignored reports built-in policies.
func surgeNode(name string, positional []string, params map[string]string) (node map[string]any, reason string, ignored bool) {
	kind := strings.ToLower(positional[0])
	switch kind {
	case "direct", "reject", "reject-tinygif", "reject-drop", "reject-no-drop":
		return nil, "", true
	case "wireguard":
		// Surge 的 WireGuard 行只引用 [WireGuard ...] 段，没有服务器和端口
		return nil, "WireGuard 暂不支持", false
	}
	if len(positional) < 3 {
		return nil, "缺少服务器或端口", false
	}
	port, err := strconv.Atoi(positional[2])
	if err != nil || port < 1 || port > 65535 {
		return nil, "端口无效", false
	}
	if params["shadow-tls-password"] != "" && kind != "ss" {
		return nil, "ShadowTLS 只支持与 Shadowsocks 搭配", false
	}
	node = map[string]any{"name": name, "server": positional[1], "port": port}
	credential := func(i int, key string) string {
		if v := params[key]; v != "" {
			return v
		}
		if i < len(positional) {
			return positional[i]
		}
		return ""
	}
	switch kind {
	case "trojan":
		node["type"] = "trojan"
		node["password"] = params["password"]
		surgeTLS(node, params, "sni")
		surgeWebSocket(node, params)
	case "ss":
		node["type"] = "ss"
		node["cipher"] = params["encrypt-method"]
		node["password"] = params["password"]
		if password := params["shadow-tls-password"]; password != "" {
			if params["obfs"] != "" {
				return nil, "同时使用 obfs 与 ShadowTLS，暂不支持", false
			}
			version, _ := strconv.Atoi(params["shadow-tls-version"])
			if version == 0 {
				version = 2
			}
			if version < 1 || version > 3 {
				return nil, fmt.Sprintf("ShadowTLS v%d 暂不支持（支持 v1～v3）", version), false
			}
			node["plugin"] = "shadow-tls"
			node["plugin-opts"] = map[string]any{"host": params["shadow-tls-sni"], "password": password, "version": version}
		} else if obfs := params["obfs"]; obfs != "" {
			opts := map[string]any{"mode": obfs}
			if host := params["obfs-host"]; host != "" {
				opts["host"] = host
			}
			node["plugin"] = "obfs"
			node["plugin-opts"] = opts
		}
	case "anytls":
		node["type"] = "anytls"
		node["password"] = params["password"]
		surgeTLS(node, params, "sni")
	case "vmess":
		node["type"] = "vmess"
		node["uuid"] = params["username"]
		node["alterId"] = 0
		node["cipher"] = "auto"
		if surgeBool(params["tls"]) {
			node["tls"] = true
			surgeTLS(node, params, "servername")
		}
		surgeWebSocket(node, params)
	case "vless":
		if enc := params["encryption"]; enc != "" && !strings.EqualFold(enc, "none") {
			return nil, "VLESS encryption 暂不支持", false
		}
		if params["username"] == "" {
			return nil, "VLESS 缺少 username（UUID）", false
		}
		node["type"] = "vless"
		node["uuid"] = params["username"]
		if flow := params["flow"]; flow != "" {
			node["flow"] = flow
		}
		if key := params["reality-public-key"]; key != "" {
			node["tls"] = true
			opts := map[string]any{"public-key": key}
			if id := params["reality-short-id"]; id != "" {
				opts["short-id"] = id
			}
			node["reality-opts"] = opts
			// Surge has no fingerprint option and its REALITY client sends a
			// Chrome ClientHello; the core's default Go ClientHello fails here.
			node["client-fingerprint"] = "chrome"
			surgeTLS(node, params, "servername")
		} else if surgeBool(params["tls"]) {
			node["tls"] = true
			surgeTLS(node, params, "servername")
		}
		surgeWebSocket(node, params)
	case "http", "https", "socks5", "socks5-tls":
		node["type"] = map[string]string{"http": "http", "https": "http", "socks5": "socks5", "socks5-tls": "socks5"}[kind]
		if user := credential(3, "username"); user != "" {
			node["username"] = user
			node["password"] = credential(4, "password")
		}
		if kind == "https" || kind == "socks5-tls" {
			node["tls"] = true
			surgeTLS(node, params, "sni")
			if kind == "socks5-tls" {
				delete(node, "sni") // Mihomo's SOCKS5 has no server name option.
			}
		}
	case "snell":
		version, _ := strconv.Atoi(params["version"])
		if version == 0 {
			version = 1
		}
		if version > 3 {
			return nil, fmt.Sprintf("Snell v%d 暂不支持（支持 v1～v3）", version), false
		}
		node["type"] = "snell"
		node["psk"] = params["psk"]
		node["version"] = version
		if obfs := params["obfs"]; obfs != "" {
			opts := map[string]any{"mode": obfs}
			if host := params["obfs-host"]; host != "" {
				opts["host"] = host
			}
			node["obfs-opts"] = opts
		}
	case "tuic", "tuic-v5":
		node["type"] = "tuic"
		if kind == "tuic" {
			node["token"] = params["token"]
		} else {
			node["uuid"] = params["uuid"]
			node["password"] = params["password"]
		}
		if alpn := params["alpn"]; alpn != "" {
			node["alpn"] = strings.Split(alpn, "|")
		}
		surgeTLS(node, params, "sni")
	case "hysteria2":
		node["type"] = "hysteria2"
		node["password"] = params["password"]
		if down := params["download-bandwidth"]; down != "" {
			if _, err := strconv.Atoi(down); err == nil {
				node["down"] = down + " Mbps"
			}
		}
		surgeTLS(node, params, "sni")
	case "ssh":
		if params["private-key"] != "" {
			return nil, "SSH 使用 Surge 钥匙串中的私钥，无法读取；请在 Cage 中手动添加 SSH 节点", false
		}
		node["type"] = "ssh"
		node["username"] = params["username"]
		node["password"] = params["password"]
	default:
		return nil, fmt.Sprintf("协议 %s 暂不支持", kind), false
	}
	if under := params["underlying-proxy"]; under != "" {
		node["dialer-proxy"] = under
	}
	if surgeBool(params["udp-relay"]) {
		node["udp"] = true
	}
	return node, "", false
}

// surgeData converts a Surge profile to the proxies and groups the importer
// reads. Groups are kept by membership only: an underlying-proxy that names a
// group resolves when that group has a single member, as for Clash configs.
func surgeData(root, file string) ([]byte, string, error) {
	remote := 0
	sections, err := surgeRead(root, file, 0, map[string]bool{}, &remote)
	if err != nil {
		return nil, "", err
	}
	proxies := []map[string]any{}
	reasons := map[string]int{}
	for _, line := range sections["Proxy"] {
		name, positional, params, ok := surgeLine(line)
		if !ok {
			continue
		}
		node, reason, ignored := surgeNode(name, positional, params)
		switch {
		case ignored:
		case reason != "":
			reasons[reason]++
		default:
			proxies = append(proxies, node)
		}
	}
	groups := []map[string]any{}
	for _, line := range sections["Proxy Group"] {
		name, positional, _, ok := surgeLine(line)
		if !ok || len(positional) < 2 {
			continue
		}
		groups = append(groups, map[string]any{"name": name, "type": "select", "proxies": positional[1:]})
	}
	var notes []string
	if remote > 0 {
		notes = append(notes, fmt.Sprintf("有 %d 处引用了远程配置，未下载；这些节点不会导入。可在 Surge 中导出完整配置后再导入", remote))
	}
	if len(reasons) > 0 {
		keys := make([]string, 0, len(reasons))
		for reason := range reasons {
			keys = append(keys, reason)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, reason := range keys {
			parts = append(parts, fmt.Sprintf("%s（%d 个）", reason, reasons[reason]))
		}
		notes = append(notes, "以下节点未导入："+strings.Join(parts, "；"))
	}
	if len(proxies) == 0 {
		if len(notes) > 0 {
			return nil, "", errors.New(strings.Join(notes, "。"))
		}
		return nil, "", errors.New("这份 Surge 配置没有可导入的节点")
	}
	output := map[string]any{"proxies": proxies}
	if len(groups) > 0 {
		output["proxy-groups"] = groups
	}
	result, err := json.Marshal(output)
	return result, strings.Join(notes, "。"), err
}
