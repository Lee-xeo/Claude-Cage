package cage

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/sylvinus/agent-vm/internal/exitpolicy"
	"github.com/sylvinus/agent-vm/internal/host"
	"github.com/sylvinus/agent-vm/internal/managedproxy"
)

func validNodeID(id string) bool {
	_, err := hex.DecodeString(id)
	return len(id) == 24 && err == nil && strings.ToLower(id) == id
}

// Only the local controller requests listeners. Each guest-facing Unix socket
// serves one immutable node and has no management operations or credentials.
type nodeListeners struct {
	mu              sync.Mutex
	dir, configPath string
	gate            *EgressGate
	routes          map[string]*nodeListener
	closed          bool
	verify          func(context.Context, *Egress, Config, string, exitpolicy.LookupOptions) error
}
type nodeListener struct {
	server   *http.Server
	revision string
	stop     context.CancelFunc
}

func (m *nodeListeners) prepare(id string) error {
	if !validNodeID(id) {
		return errors.New("代理节点标识无效")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrGateClosed
	}
	cfg, err := Load(m.configPath)
	if err != nil {
		return err
	}
	profile := filepath.Dir(m.configPath)
	store, err := managedproxy.Applied(profile, cfg.ProxyURL, cfg.ProxyRevision)
	if err != nil {
		return err
	}
	fingerprint := cfg.Fingerprint()
	cfg.ProxyURL, err = store.NodeEndpoint(id)
	if err != nil {
		return err
	}
	if id != store.Selected {
		policy, saved := store.ExitPolicies[id]
		if !saved {
			policy = exitpolicy.Policy{Mode: cfg.ExitPolicy.Mode, Scope: cfg.ExitPolicy.Scope}.Normalized()
		}
		cfg.ExitPolicy = policy
	}
	revision := fingerprint + cfg.Fingerprint()
	if old := m.routes[id]; old != nil {
		if old.revision == revision {
			return nil
		}
		old.stop()
		old.server.Close()
		delete(m.routes, id)
	}
	raw, err := NewEgress(cfg)
	if err != nil {
		return err
	}
	raw.client.Transport.(*http.Transport).DisableKeepAlives = true
	socket := filepath.Join(m.dir, "n-"+id)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	if err := host.ProtectFile(socket); err != nil {
		listener.Close()
		return err
	}
	life, stop := context.WithCancel(context.Background())
	route := &nodeHandler{parent: m.gate, raw: raw, fingerprint: fingerprint, life: life, verify: func(ctx context.Context) error {
		return m.verify(ctx, raw, cfg, filepath.Join(managedproxy.Dir(profile), "account-policy", id+".json"), LocationLookupOptions(m.configPath))
	}}
	server := &http.Server{Handler: route, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 20 * time.Second, MaxHeaderBytes: 4096}
	if m.routes == nil {
		m.routes = map[string]*nodeListener{}
	}
	m.routes[id] = &nodeListener{server: server, revision: revision, stop: stop}
	go server.Serve(listener)
	return nil
}
func (m *nodeListeners) refresh() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.routes))
	for id := range m.routes {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		if err := m.prepare(id); err != nil {
			m.mu.Lock()
			if old := m.routes[id]; old != nil {
				old.stop()
				old.server.Close()
				delete(m.routes, id)
			}
			m.mu.Unlock()
		}
	}
}

func (m *nodeListeners) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for _, r := range m.routes {
		r.stop()
		r.server.Close()
	}
}

// Verify the actual outbound independently of the environment's selected node.
// Persist a first-use anchor only after the exit/country evidence passes.
// LocationLookupOptions reads the user's opt-in location fallbacks from the
// workbench preferences beside configPath at each check, so a change in
// settings reaches per-account node checks without restarting the gate. A
// missing or unreadable file keeps the default providers only.
func LocationLookupOptions(configPath string) exitpolicy.LookupOptions {
	var prefs struct {
		IPInfoFallback bool `json:"location_ipinfo_fallback"`
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(configPath), "workbench.json"))
	if err != nil || len(data) > 1<<20 || json.Unmarshal(data, &prefs) != nil {
		return exitpolicy.LookupOptions{}
	}
	return exitpolicy.LookupOptions{IPInfoFallback: prefs.IPInfoFallback}
}

// NodeLocation records the last verified country and timezone observed for an
// account-bound proxy node so per-account Desktop/CLI sessions can align their
// locale and timezone with that node's exit region.
type NodeLocation struct {
	CountryCode string `json:"country_code,omitempty"`
	Timezone    string `json:"timezone,omitempty"`
}

func nodeLocationPath(policyFile string) string {
	return strings.TrimSuffix(policyFile, ".json") + ".location.json"
}

// LoadNodeLocation reads the last verified exit region recorded for node id.
func LoadNodeLocation(profile, id string) (NodeLocation, bool) {
	if !validNodeID(id) {
		return NodeLocation{}, false
	}
	file := nodeLocationPath(filepath.Join(managedproxy.Dir(profile), "account-policy", id+".json"))
	data, err := os.ReadFile(file)
	if err != nil || len(data) > 64<<10 {
		return NodeLocation{}, false
	}
	var loc NodeLocation
	if json.Unmarshal(data, &loc) != nil {
		return NodeLocation{}, false
	}
	loc.CountryCode = strings.ToUpper(strings.TrimSpace(loc.CountryCode))
	loc.Timezone = strings.TrimSpace(loc.Timezone)
	if loc.CountryCode == "" && loc.Timezone == "" {
		return NodeLocation{}, false
	}
	return loc, true
}

func saveNodeLocation(policyFile string, loc exitpolicy.Location) {
	record := NodeLocation{
		CountryCode: strings.ToUpper(strings.TrimSpace(loc.CountryCode)),
		Timezone:    strings.TrimSpace(loc.Timezone),
	}
	if record.CountryCode == "" && record.Timezone == "" {
		return
	}
	if data, err := json.Marshal(record); err == nil {
		_ = managedproxy.Write(nodeLocationPath(policyFile), data)
	}
}

func verifyNodeExit(ctx context.Context, raw *Egress, cfg Config, file string, options exitpolicy.LookupOptions) error {
	ip, err := probeExitIP(ctx, raw, exitProbeReachabilityURL)
	if err != nil {
		return err
	}
	transport := &http.Transport{DialContext: raw.DialContext, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}
	loc, err := exitpolicy.LookupWithOptions(ctx, client, net.ParseIP(ip), options)
	if err != nil {
		return err
	}
	policy := cfg.ExitPolicy.Normalized()
	if policy.NeedsBinding() {
		var saved struct{ Requested, Bound exitpolicy.Policy }
		data, err := os.ReadFile(file)
		if err == nil {
			if json.Unmarshal(data, &saved) != nil || saved.Bound.Validate() != nil || saved.Bound.NeedsBinding() || saved.Bound.Mode != saved.Requested.Mode || saved.Bound.Scope != saved.Requested.Scope {
				return errors.New("账号出口绑定记录无效")
			}
			if saved.Requested == policy {
				policy = saved.Bound
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if policy.NeedsBinding() {
			bound := policy.BindMissing(loc)
			if state, _ := bound.Evaluate(ip, &loc); state != exitpolicy.Passed {
				return ErrGateClosed
			}
			saved.Requested, saved.Bound = policy, bound
			data, _ = json.Marshal(saved)
			if err := managedproxy.Write(file, data); err != nil {
				return err
			}
			policy = bound
		}
	}
	if result, _ := policy.Evaluate(ip, &loc); result != exitpolicy.Passed {
		return ErrGateClosed
	}
	saveNodeLocation(file, loc)
	return nil
}

type nodeHandler struct {
	life        context.Context
	parent      *EgressGate
	raw         *Egress
	fingerprint string
	verify      func(context.Context) error
	mu          sync.Mutex
	permit      context.Context
	revoke      context.CancelFunc
	checked     time.Time
	monitoring  bool
	active      int
}

func (n *nodeHandler) check(ctx context.Context, background bool) (context.Context, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := time.Now()
	if background && n.active == 0 {
		return nil, nil
	}
	if n.parent.Status().Configuration != n.fingerprint {
		return nil, ErrGateClosed
	}
	if now.Sub(n.checked) > 30*time.Second {
		verify, done := context.WithTimeout(ctx, 20*time.Second)
		err := n.verify(verify)
		done()
		if err != nil {
			if n.revoke != nil {
				n.revoke()
			}
			n.permit = nil
		} else if n.permit == nil {
			n.permit, n.revoke = context.WithCancel(n.life)
		}
		n.checked = time.Now()
	}
	if n.permit == nil || n.permit.Err() != nil {
		return nil, ErrGateClosed
	}
	return n.permit, nil
}
func (n *nodeHandler) monitor() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-n.life.Done():
			return
		case <-ticker.C:
			n.mu.Lock()
			if n.active == 0 {
				n.monitoring = false
				n.mu.Unlock()
				return
			}
			n.mu.Unlock()
			ctx, done, _, err := n.parent.operation(n.life, false)
			if err == nil {
				n.check(ctx, true)
				done()
			}
		}
	}
}
func (n *nodeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, done, epoch, err := n.parent.operation(r.Context(), false)
	if err != nil {
		http.Error(w, "closed", 403)
		return
	}
	defer done()
	permit, err := n.check(ctx, false)
	if err != nil {
		http.Error(w, "closed", 403)
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	end := context.AfterFunc(permit, cancel)
	defer end()
	if r.Method == "POST" && r.URL.Path == "/dns" {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 65535))
		var q dns.Msg
		if err != nil || q.Unpack(b) != nil || len(q.Question) != 1 {
			http.Error(w, "bad DNS", 400)
			return
		}
		ans, err := n.raw.ExchangeDNS(ctx, &q)
		if err != nil || ctx.Err() != nil {
			http.Error(w, "DNS unavailable", 502)
			return
		}
		wire, err := ans.Pack()
		if err != nil {
			http.Error(w, "DNS unavailable", 502)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(wire)))
		w.Write(wire)
		return
	}
	dst, err := netip.ParseAddrPort(r.Host)
	if r.Method != "CONNECT" || err != nil || !n.raw.Allows("tcp", dst) {
		http.Error(w, "denied", 403)
		return
	}
	upstream, err := n.raw.DialContext(ctx, "tcp", dst.String())
	if err != nil {
		http.Error(w, "unavailable", 502)
		return
	}
	upstream, err = n.parent.track(upstream, epoch)
	if err != nil {
		http.Error(w, "closed", 403)
		return
	}
	defer upstream.Close()
	peer, buf, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer peer.Close()
	n.mu.Lock()
	n.active++
	if !n.monitoring {
		n.monitoring = true
		go n.monitor()
	}
	n.mu.Unlock()
	defer func() { n.mu.Lock(); n.active--; n.mu.Unlock() }()
	onClose := context.AfterFunc(ctx, func() { upstream.Close(); peer.Close() })
	defer onClose()
	peer.SetDeadline(time.Time{})
	if _, err = buf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil || buf.Flush() != nil {
		return
	}
	go func() {
		_, err := io.Copy(upstream, buf.Reader)
		if err != nil || closeWrite(upstream) != nil {
			upstream.Close()
		}
	}()
	io.Copy(peer, upstream)
}

func (c *GateClient) PrepareAccountNode(ctx context.Context, id string) (string, error) {
	if !validNodeID(id) {
		return "", errors.New("代理节点标识无效")
	}
	_, err := c.request(ctx, &gateCommand{Action: "account-node", Node: id})
	return filepath.Join(filepath.Dir(c.record.Socket), "n-"+id), err
}
