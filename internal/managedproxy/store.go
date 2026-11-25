// Package managedproxy owns Cage's private proxy sources and generated core configuration.
package managedproxy

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/sylvinus/agent-vm/internal/exitpolicy"
	"github.com/sylvinus/agent-vm/internal/host"
	"go.yaml.in/yaml/v3"
)

const Group = "CAGE"
const MaxImport = 8 << 20
const maxStoreSize = 32 << 20

type Node struct {
	ID     string         `json:"id"`
	Name   string         `json:"name"`
	Type   string         `json:"type"`
	Config map[string]any `json:"config"`
}
type Source struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	URL       string      `json:"url,omitempty"`
	Interval  int         `json:"interval_hours"`
	Updated   time.Time   `json:"updated_at"`
	Attempted time.Time   `json:"attempted_at"`
	Error     string      `json:"error,omitempty"`
	Usage     string      `json:"usage,omitempty"`
	Nodes     []Node      `json:"nodes"`
	Skipped   []NodeIssue `json:"skipped,omitempty"`
	NodeDNS   []string    `json:"node_dns,omitempty"`
}
type Store struct {
	Version      int                          `json:"version"`
	Port         int                          `json:"port"`
	Auth         string                       `json:"auth"`
	Revision     string                       `json:"revision"`
	Selected     string                       `json:"selected"`
	Chain        []string                     `json:"chain,omitempty"`
	Sources      []Source                     `json:"sources"`
	ExitPolicies map[string]exitpolicy.Policy `json:"exit_policies,omitempty"`
	// GuestDNS and BootstrapDNS copy the Cage DNS settings when a route is
	// applied, so a core restarted from this store keeps using them.
	GuestDNS     []string `json:"guest_dns,omitempty"`
	BootstrapDNS []string `json:"bootstrap_dns,omitempty"`
}

// NodeResolvers resolve proxy server names on the host network.
func (s Store) NodeResolvers() []string {
	if len(s.BootstrapDNS) > 0 {
		return append([]string(nil), s.BootstrapDNS...)
	}
	return BootstrapDNSURLs()
}

// CoreDNS is the DoH list the core resolves with through the selected route:
// the guest DNS endpoints copied when the route was applied.
func (s Store) CoreDNS() []string {
	if len(s.GuestDNS) == 0 {
		return []string{"https://1.1.1.1/dns-query"}
	}
	return append([]string(nil), s.GuestDNS...)
}

// coreNameservers are the core's own resolvers, queried through the selected
// route like guest DNS. They use the same endpoints as guest DNS.
func (s Store) coreNameservers() []string {
	list := s.CoreDNS()
	out := make([]string, 0, len(list))
	for _, endpoint := range list {
		out = append(out, endpoint+"#"+Group)
	}
	return out
}

func Token() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func Dir(profile string) string { return filepath.Join(profile, "proxy") }
func Socket(profile string) string {
	if runtime.GOOS == "windows" {
		if path, err := windowsSocket(profile, false); err == nil {
			return path
		}
		// No usable directory: starting the core reports WindowsSocket's cause.
		return filepath.Join(Dir(profile), "control.sock")
	}
	h := sha256.Sum256([]byte(filepath.Clean(profile)))
	return filepath.Join("/tmp", fmt.Sprintf("cage-proxy-%d-%x", os.Getuid(), h[:8]), "control.sock")
}

var windowsSockets sync.Map

// WindowsSocket is the core's control socket in a private ASCII directory
// outside AppData and Temp, where Windows AF_UNIX sockets could not be
// connected or removed (#484). The choice is stable for a profile. Starting
// the core creates the directory; other callers only look it up.
func WindowsSocket(profile string) (string, error) { return windowsSocket(profile, true) }

func windowsSocket(profile string, create bool) (string, error) {
	key := filepath.Clean(profile)
	if path, ok := windowsSockets.Load(key); ok && !create {
		return path.(string), nil
	}
	dir, err := host.PrivateSocketDir(key, len(`\proxy.sock`), create)
	if err != nil {
		return "", err
	}
	path := dir + `\proxy.sock`
	windowsSockets.Store(key, path)
	return path, nil
}

// legacyWindowsSocket is where versions before #484 placed the socket.
func legacyWindowsSocket(profile string) string {
	h := sha256.Sum256([]byte(filepath.Clean(profile)))
	return filepath.Join(os.TempDir(), fmt.Sprintf("cage-proxy-%x", h[:8]), "control.sock")
}
func Load(profile string) (Store, error) {
	b, missing, err := readStore(profile)
	if err != nil {
		return Store{}, err
	}
	return parseStore(b, missing)
}

// readStore returns the stored bytes; missing reports a store not yet created.
func readStore(profile string) (b []byte, missing bool, err error) {
	f, err := os.Open(filepath.Join(Dir(profile), "sources.json"))
	if os.IsNotExist(err) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	b, err = io.ReadAll(io.LimitReader(f, maxStoreSize+1))
	return b, false, err
}

func parseStore(b []byte, missing bool) (Store, error) {
	var s Store
	if missing {
		return Store{Version: 1, Port: 17896, Auth: Token(), Sources: []Source{}}, nil
	}
	if len(b) > maxStoreSize || json.Unmarshal(b, &s) != nil || s.Version != 1 || s.Port < 1024 || s.Port > 65535 || len(s.Auth) != 48 {
		return s, errors.New("Cage 代理配置无效，请恢复私有配置备份")
	}
	return s, nil
}
func Write(path string, data []byte) error {
	if err := host.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".proxy-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = host.ProtectFile(f.Name()); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func (s Store) Save(profile string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > maxStoreSize {
		return errors.New("代理来源配置总大小超过 32 MiB，请减少来源或节点；原配置已保留")
	}
	return Write(filepath.Join(Dir(profile), "sources.json"), b)
}
func (s Store) Endpoint() string { return fmt.Sprintf("socks5://cage:%s@127.0.0.1:%d", s.Auth, s.Port) }
func (s Store) Find(id string) (Node, bool) {
	for _, src := range s.Sources {
		for _, n := range src.Nodes {
			if n.ID == id {
				return n, true
			}
		}
	}
	return Node{}, false
}
func (s Store) Count() int {
	n := 0
	for _, src := range s.Sources {
		n += len(src.Nodes)
	}
	return n
}
func (s *Store) Import(name, rawURL string, data []byte, id string, interval int, usage string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return errors.New("请填写 1–100 字节的来源名称")
	}
	if interval < 0 || interval > 168 {
		return errors.New("更新间隔应为 0–168 小时，0 表示手动更新")
	}
	if id == "" {
		id = Token()[:16]
	}
	nodes, skipped, err := parseNodes(data, id)
	if err != nil {
		return err
	}
	nodeDNS, err := subscriptionNodeDNS(data)
	if err != nil {
		return err
	}
	src := Source{ID: id, Name: name, URL: rawURL, Interval: interval, Updated: time.Now(), Attempted: time.Now(), Nodes: nodes, Usage: usage, Skipped: skipped, NodeDNS: nodeDNS}
	for i := range s.Sources {
		if s.Sources[i].ID == id {
			// Refresh must retain every hop of the saved route, including legacy
			// node-level dialers. A partial import cannot shorten a working chain.
			route, routeErr := s.Route()
			if s.Selected != "" && routeErr != nil && !errors.Is(routeErr, ErrLocalProxyNode) {
				return routeErr
			}
			available := make(map[string]bool, len(nodes))
			for _, n := range nodes {
				available[n.ID] = true
			}
			required := make(map[string]bool, len(route))
			for _, hop := range route {
				required[hop] = true
			}
			for _, old := range s.Sources[i].Nodes {
				if required[old.ID] && !available[old.ID] {
					reason := "节点未包含在更新中"
					for _, issue := range skipped {
						if issue.Name == old.Name {
							reason = issue.Reason
							break
						}
					}
					return fmt.Errorf("订阅更新移除或跳过了当前出口或中转节点 %q：%s；原配置已保留，请先应用其他节点再更新", old.Name, reason)
				}
			}
			s.Sources[i] = src
			return nil
		}
	}
	s.Sources = append(s.Sources, src)
	return nil
}

func unsafeOption(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		for k, v := range x {
			if unsafeField(k, v) {
				return true
			}
		}
	case map[any]any:
		// YAML maps with a non-string key can still contain core option keys.
		for k, v := range x {
			key, _ := k.(string)
			if unsafeField(key, v) {
				return true
			}
		}
	case []any:
		for _, v := range x {
			if unsafeOption(v) {
				return true
			}
		}
	}
	return false
}

func unsafeField(key string, value any) bool {
	key = optionKey(key)
	if strings.Contains(key, "path") && key != "path" {
		return true
	}
	switch key {
	case "ca", "certificate", "private-key", "interface-name", "routing-mark":
		return true
	}
	return unsafeOption(value)
}

// Route returns the actual hops from entry to exit. Explicit UI chains override
// dialers at runtime without modifying subscription nodes or their credentials.
func (s Store) Route() ([]string, error) {
	return s.route(s.BlockedNodes())
}

func (s Store) route(blocked map[string]string) ([]string, error) {
	if _, ok := s.Find(s.Selected); !ok {
		return nil, errors.New("请先选择有效的最终出口")
	}
	if len(s.Chain) > 0 {
		if s.Chain[len(s.Chain)-1] != s.Selected {
			return nil, errors.New("代理链与最终出口不一致，请重新配置代理")
		}
		seen := map[string]bool{}
		for _, id := range s.Chain {
			if _, ok := s.Find(id); !ok {
				return nil, errors.New("代理链中的节点已被移除，请重新选择；原配置已保留")
			}
			if seen[id] {
				return nil, errors.New("代理链不能重复使用同一个节点")
			}
			seen[id] = true
		}
		return s.checkRouteNodes(append([]string(nil), s.Chain...), blocked)
	}
	var reversed []string
	seen := map[string]bool{}
	for id := s.Selected; id != ""; {
		n, ok := s.Find(id)
		if !ok || seen[id] {
			return nil, errors.New("导入的代理链存在循环或缺失节点，请检查来源配置")
		}
		seen[id] = true
		reversed = append(reversed, id)
		id, _ = n.Config["dialer-proxy"].(string)
	}
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	return s.checkRouteNodes(reversed, blocked)
}

func (s Store) Runtime(profile string) ([]byte, error) {
	return s.runtimeWithSocket(Socket(profile))
}

func (s Store) runtimeWithSocket(socketPath string) ([]byte, error) {
	blocked := s.BlockedNodes()
	route, err := s.route(blocked)
	if err != nil {
		return nil, err
	}
	dialers := map[string]string{}
	for i, id := range s.Chain {
		dialers[id] = ""
		if i > 0 {
			dialers[id] = s.Chain[i-1]
		}
	}
	proxies := []map[string]any{}
	originals := map[string]map[string]any{}
	for _, src := range s.Sources {
		for _, n := range src.Nodes {
			if blocked[n.ID] != "" {
				continue
			}
			config := make(map[string]any, len(n.Config))
			for key, value := range n.Config {
				config[key] = value
			}
			// Older stores may contain legacy headers accepted before normalization.
			if err := normalizeWebSocket(config); err != nil {
				return nil, fmt.Errorf("节点 %q：%w", n.Name, err)
			}
			originals[n.ID] = config
			if via, overridden := dialers[n.ID]; overridden {
				config = maps.Clone(config)
				delete(config, "dialer-proxy")
				if via != "" {
					config["dialer-proxy"] = via
				}
			}
			proxies = append(proxies, config)
		}
	}
	// Infrastructure DNS bootstraps only the core's server domains. Guest DNS
	// remains behind Cage Egress, and there is no host-system resolver fallback.
	// Top-level ipv6 (mihomo v1.19.33: resolver.DisableIPv6 = !ipv6, global to
	// the core) also makes every proxy server domain resolve AAAA and dial over
	// IPv6. It is enabled only when a node on the selected route uses a
	// literal IPv6 server address, so the core can dial that hop; other loaded
	// nodes never turn it on. dns.ipv6 only controls AAAA answers of the core's
	// own DNS listener (none is configured) and stays false. Cage Egress keeps
	// blocking all guest IPv6 and enforcing IPv4 exits.
	dns := map[string]any{"enable": true, "ipv6": false, "use-hosts": false, "use-system-hosts": false, "default-nameserver": s.NodeResolvers(), "proxy-server-nameserver": s.NodeResolvers(), "nameserver": s.coreNameservers()}
	policies, err := s.sourceDNSPolicies(blocked)
	if err != nil {
		return nil, err
	}
	if len(policies) > 0 {
		dns["proxy-server-nameserver-policy"] = policies
	}
	config := map[string]any{"mixed-port": s.Port, "bind-address": "127.0.0.1", "allow-lan": false, "skip-auth-prefixes": []string{}, "mode": "rule", "ipv6": routeHasIPv6Server(route, originals), "log-level": "warning", "find-process-mode": "off", "external-controller-unix": socketPath, "tun": map[string]any{"enable": false}, "dns": dns, "geo-auto-update": false, "profile": map[string]any{"store-selected": false}, "proxy-groups": []any{map[string]any{"name": Group, "type": "select", "proxies": []string{s.Selected}}}}
	// An explicit environment chain can override imported dialers. Named
	// account routes retain the node's imported chain, including dependent hops.
	targets := map[string]string{}
	var accountTarget func(string) string
	accountTarget = func(id string) string {
		if target, ok := targets[id]; ok {
			return target
		}
		targets[id] = id
		original := originals[id]
		parent, _ := original["dialer-proxy"].(string)
		via := parent
		if parent != "" {
			via = accountTarget(parent)
		}
		applied, overridden := dialers[id]
		if via != parent || overridden && applied != parent {
			clone := maps.Clone(original)
			name := "route-" + id
			clone["name"] = name
			if via != "" {
				clone["dialer-proxy"] = via
			}
			proxies = append(proxies, clone)
			targets[id] = name
		}
		return targets[id]
	}
	auth := []string{"cage:" + s.Auth}
	rules := []string{"IN-USER,cage," + Group}
	for _, src := range s.Sources {
		for _, n := range src.Nodes {
			if blocked[n.ID] != "" {
				continue
			}
			user := nodeUser(n.ID)
			auth = append(auth, user+":"+s.nodePassword(n.ID))
			rules = append(rules, "IN-USER,"+user+","+accountTarget(n.ID))
		}
	}
	config["proxies"] = proxies
	config["authentication"] = auth
	config["rules"] = append(rules, "MATCH,REJECT")
	// Measure a request on the established connection rather than folding
	// protocol and TLS setup into the node's displayed HTTP latency.
	config["unified-delay"] = true
	return yaml.Marshal(config)
}

func routeHasIPv6Server(route []string, nodes map[string]map[string]any) bool {
	for _, id := range route {
		server, _ := nodes[id]["server"].(string)
		server = strings.TrimSpace(server)
		if strings.HasPrefix(server, "[") && strings.HasSuffix(server, "]") {
			server = server[1 : len(server)-1]
		}
		if addr, err := netip.ParseAddr(server); err == nil && addr.Is6() && !addr.Is4In6() {
			return true
		}
	}
	return false
}
