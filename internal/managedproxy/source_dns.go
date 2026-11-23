package managedproxy

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// ErrSourceNodeDNS marks a source rejected for its node resolver list alone:
// its nodes parsed, so "no importable nodes" would misstate the cause.
var ErrSourceNodeDNS = errors.New("unsupported source node DNS")

type sourceNodeDNSError struct{ error }

func (sourceNodeDNSError) Is(target error) bool { return target == ErrSourceNodeDNS }

// Adopt only safe remote HTTPS DoH entries from the subscription's node
// resolver list (up to 4 unique entries). Unsupported, local, non-HTTPS,
// credentialed, or fragmented Clash/Mihomo resolvers (such as plain IPs,
// system, dhcp://, udp://, tcp://, tls://, quic://, h3://, http://, or DoH
// URLs carrying #skip-cert-verify=true, #DIRECT, #h3=true, etc.) are ignored
// rather than blocking node import or subscription refresh, so Cage falls back
// to its own verified node DNS when none can be safely adopted.
func subscriptionNodeDNS(data []byte) ([]string, error) {
	var input struct {
		DNS yaml.Node `yaml:"dns"`
	}
	if yaml.Unmarshal(data, &input) != nil || input.DNS.Kind != yaml.MappingNode {
		return nil, nil // Share-link lists have no DNS configuration.
	}
	var dns struct {
		Servers []string `yaml:"proxy-server-nameserver"`
	}
	if input.DNS.Decode(&dns) != nil {
		return nil, nil
	}
	return FilterNodeDNS(dns.Servers), nil
}

// FilterNodeDNS returns up to 4 unique, safe remote HTTPS DoH resolver URLs
// from servers, ignoring non-HTTPS, local/private, credentialed, fragmented
// (e.g. #skip-cert-verify=true, #DIRECT, #h3=true), malformed, or excess
// entries so real-world Clash/Mihomo profiles never block proxy import.
func FilterNodeDNS(servers []string) []string {
	var out []string
	local := newLocalServers()
	for _, raw := range servers {
		raw = strings.TrimSpace(raw)
		if !isSafeSourceDNS(raw, local) {
			continue
		}
		if !slices.Contains(out, raw) {
			out = append(out, raw)
			if len(out) == 4 {
				break
			}
		}
	}
	return out
}

// privateDNSHost reports a literal address inside a private, link-local or
// carrier-grade NAT range. A subscription must not point node lookups at the
// user's own network.
func privateDNSHost(host string) bool {
	addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	return addr.IsPrivate() || addr.IsLinkLocalUnicast() || netip.MustParsePrefix("100.64.0.0/10").Contains(addr)
}

func isSafeSourceDNS(raw string, local localServers) bool {
	if raw == "" || len(raw) > 2048 {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || local.contains(u.Hostname()) || privateDNSHost(u.Hostname()) {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}

func validateSourceDNS(servers []string) ([]string, error) {
	if len(servers) > 4 {
		return nil, sourceNodeDNSError{errors.New("订阅的节点 DNS 最多支持 4 个 HTTPS DoH 地址")}
	}
	var out []string
	local := newLocalServers()
	for i, raw := range servers {
		raw = strings.TrimSpace(raw)
		u, err := url.Parse(raw)
		if err == nil && privateDNSHost(u.Hostname()) {
			// Saved before such addresses were refused at import. Leaving it
			// out keeps the other nodes working.
			continue
		}
		if err != nil || len(raw) > 2048 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || local.contains(u.Hostname()) {
			return nil, sourceNodeDNSError{fmt.Errorf("订阅的第 %d 个节点 DNS 需要远程 HTTPS DoH 地址，不支持用户名、密码或 # 路由参数", i+1)}
		}
		if port := u.Port(); port != "" {
			n, err := strconv.Atoi(port)
			if err != nil || n < 1 || n > 65535 {
				return nil, sourceNodeDNSError{fmt.Errorf("订阅的第 %d 个节点 DNS 端口无效", i+1)}
			}
		}
		if !slices.Contains(out, raw) {
			out = append(out, raw)
		}
	}
	return out, nil
}

// The bundled core has a separate resolver/cache for proxy server domains.
// Exact policies there cannot alter business DNS or resolver bootstrapping.
// It keys policies by domain, not node/source; reject conflicting ownership
// rather than silently sending another source's queries to this resolver.
// The same resolvers in another order are the same ownership.
func (s Store) sourceDNSPolicies(blocked map[string]string) (map[string][]string, error) {
	policies := map[string][]string{}
	owners := map[string][]string{}
	sameServers := func(a, b []string) bool {
		a, b = slices.Clone(a), slices.Clone(b)
		slices.Sort(a)
		slices.Sort(b)
		return slices.Equal(a, b)
	}
	for _, src := range s.Sources {
		servers, err := validateSourceDNS(src.NodeDNS)
		if err != nil {
			return nil, err
		}
		custom := len(servers) > 0
		if !custom {
			servers = s.NodeResolvers()
		}
		for _, node := range src.Nodes {
			if blocked[node.ID] != "" {
				continue
			}
			host, _ := node.Config["server"].(string)
			host = strings.ToLower(strings.TrimSuffix(host, "."))
			if _, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
				continue
			}
			if previous, ok := owners[host]; ok && !sameServers(previous, servers) {
				return nil, errors.New("多个来源对同一节点域名指定了不同 DNS，无法隔离解析；请统一这些来源的节点 DNS 或移除冲突来源，原配置已保留")
			}
			owners[host] = servers
			if custom {
				if !exactDNSHost(host) {
					return nil, errors.New("使用订阅节点 DNS 时，节点服务器必须是具体域名或 IP，不能包含通配符或匹配规则")
				}
				policies[host] = servers
			}
		}
	}
	return policies, nil
}

func exactDNSHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return false
			}
		}
	}
	return true
}
