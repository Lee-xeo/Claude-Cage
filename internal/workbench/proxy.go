package workbench

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/managedproxy"
	"github.com/sylvinus/agent-vm/internal/usererror"
)

var (
	validateProxyCore = managedproxy.Validate
	startProxyCore    = managedproxy.Start
	fetchProxySource  = managedproxy.Fetch
	proxyCorePath     = managedproxy.CorePath
)

type proxyInput struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	URL          string   `json:"url"`
	Content      string   `json:"content"`
	Interval     int      `json:"interval_hours"`
	HostDownload bool     `json:"host_download"`
	NodeID       string   `json:"node_id"`
	NodeIDs      []string `json:"node_ids"`
	Port         int      `json:"port"`
	Server       string   `json:"server"`
	ServerPort   int      `json:"server_port"`
	// Action may repeat the operation named by the request path, which
	// scripted callers tend to send (#763); a different value is rejected.
	Action string `json:"action"`
}

func (s *Server) proxyStatus(w http.ResponseWriter, r *http.Request) {
	p, err := managedproxy.Load(s.profile)
	if err != nil {
		requestError(w, err)
		return
	}
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		requestError(w, err)
		return
	}
	blocked := p.BlockedNodes()
	sources := []any{}
	for _, src := range p.Sources {
		nodes := []any{}
		for _, n := range src.Nodes {
			node := map[string]any{"id": n.ID, "name": n.Name, "type": n.Type}
			if src.URL == "" {
				// Local nodes can be edited in place, so the form shows their
				// address. Credentials and other options are never returned.
				node["server"], node["port"] = n.Config["server"], n.Config["port"]
			}
			if reason := blocked[n.ID]; reason != "" {
				node["blocked_reason"] = reason
			}
			nodes = append(nodes, node)
		}
		host := ""
		if u, e := url.Parse(src.URL); e == nil {
			host = u.Hostname()
		}
		sources = append(sources, map[string]any{"id": src.ID, "name": src.Name, "remote": src.URL != "", "host": host, "interval_hours": src.Interval, "updated_at": src.Updated, "error": src.Error, "usage": src.Usage, "nodes": nodes, "skipped": src.Skipped})
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	_, appliedErr := managedproxy.Applied(s.profile, cfg.ProxyURL, cfg.ProxyRevision)
	active := appliedErr == nil
	running := managedproxy.Running(ctx, s.profile, p)
	state, label := "unconfigured", "尚未配置"
	_, selected := p.Find(p.Selected)
	if selected {
		state, label = "unapplied", "尚未应用"
	}
	if active {
		state, label = "stopped", "代理已停止"
		if running {
			state, label = "prepared", "待启动环境"
			if s.vmRunning() {
				state, label = "paused", "外网未放行"
				if client, e := s.gateClient(); e == nil {
					if gate, e := client.Status(ctx); e == nil {
						if gate.Mode == cage.GateChecking {
							state, label = "checking", "正在检查"
						} else if gate.Mode == cage.GateOpen && gate.Report != nil && gate.Report.Authorizes(gate.RunID, cfg.Fingerprint(), time.Now()) == nil && time.Now().Before(gate.Until) {
							state, label = "ready", "已联网"
						}
					}
				}
			}
		}
	}
	var version struct {
		Version string `json:"version"`
	}
	if running {
		_ = managedproxy.API(ctx, s.profile, "GET", "/version", nil, &version)
	}
	_, coreErr := proxyCorePath()
	coreError := ""
	if coreErr != nil {
		coreError = coreErr.Error()
	}
	route, routeErr := p.Route()
	if route == nil {
		route = append([]string{}, p.Chain...)
	}
	routeError := ""
	if routeErr != nil && selected {
		routeError = routeErr.Error()
		if errors.Is(routeErr, managedproxy.ErrLocalProxyNode) {
			state, label = "migration_required", "需要迁移代理节点"
		}
	}
	jsonReply(w, map[string]any{"route": route, "route_error": routeError, "sources": sources, "selected": p.Selected, "port": p.Port, "running": running, "active": active, "state": state, "state_label": label, "network_open": state == "ready", "core_available": coreErr == nil, "core_error": coreError, "version": version.Version, "revision": p.Revision, "bootstrap_dns": strings.Join(p.NodeResolvers(), "、"), "udp_blocked": true, "ipv6_blocked": true})
}
func (s *Server) proxyMutation(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("action") == "import-local" && !s.requireLocalConfigAccess(w, r) {
		return
	}
	var input proxyInput
	if !readJSON(w, r, &input, 12<<20) {
		return
	}
	action := r.PathValue("action")
	allowed := map[string]bool{"import": true, "import-local": true, "refresh": true, "edit": true, "edit-node": true, "delete": true, "select": true, "restart": true, "stop": true, "route": true}
	if !allowed[action] {
		http.NotFound(w, r)
		return
	}
	if input.Action != "" && input.Action != action {
		http.Error(w, "字段 action 与请求路径不一致：此接口执行 "+action+"，可省略 action", http.StatusBadRequest)
		return
	}
	if err := checkProxyInput(action, input); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if proxyCoreActions[action] {
		if _, err := proxyCorePath(); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
	}
	s.mu.Lock()
	pauseVersion := s.safety.pauseVersion
	s.mu.Unlock()
	s.launchRequest(w, r, "proxy", func() error { return s.changeProxy(action, input, &pauseVersion) })
}

// Every operation that validates or starts the bundled core fails before a job
// is queued when the core is missing; delete, edit and stop still work.
var proxyCoreActions = map[string]bool{"import": true, "import-local": true, "refresh": true, "edit-node": true, "select": true, "restart": true, "route": true}

// checkProxyInput rejects malformed input synchronously, so API callers get a
// 4xx instead of an accepted job that fails. Messages already shown by the
// workbench keep their wording, and translation. Checks against saved sources
// and nodes stay in changeProxy, which runs serialized with other operations.
func checkProxyInput(action string, in proxyInput) error {
	switch action {
	case "import":
		if in.ID != "" {
			return errors.New("proxy.import 不按 id 导入：GET /api/proxy/local-configs 列出的配置只能在本机 Cage 窗口中导入；请在 content 中提供整份配置，或在 url 中提供订阅链接")
		}
		if name := strings.TrimSpace(in.Name); name == "" || len(name) > 100 {
			return errors.New("请填写 1–100 字节的来源名称")
		}
		if in.Interval < 0 || in.Interval > 168 {
			return errors.New("更新间隔应为 0–168 小时，0 表示手动更新")
		}
		if in.URL == "" && strings.TrimSpace(in.Content) == "" {
			return errors.New("缺少 content 或 url：content 填写整份 Clash/Mihomo 配置或节点分享链接文本，url 填写 HTTPS 订阅链接")
		}
		if in.URL != "" && !subscriptionURL(in.URL) {
			return errors.New("订阅需要完整的 HTTPS 网址，不支持地址内用户名和密码")
		}
		if len(in.Content) > managedproxy.MaxImport {
			return errors.New("配置为空或超过 8 MiB")
		}
	case "import-local":
		if in.ID == "" {
			return errors.New("缺少 id：请填写 GET /api/proxy/local-configs 返回的配置 id")
		}
	case "refresh", "edit", "edit-node", "delete":
		if in.ID == "" {
			return errors.New("缺少 id：请填写 proxy.status 返回的来源 id")
		}
		if len(in.Content) > managedproxy.MaxImport {
			return errors.New("配置为空或超过 8 MiB")
		}
		if action == "edit" {
			if strings.TrimSpace(in.Name) == "" || len(in.Name) > 100 || in.Interval < 0 || in.Interval > 168 {
				return errors.New("名称或更新间隔无效")
			}
			if in.URL != "" && !subscriptionURL(in.URL) {
				return errors.New("订阅需要完整的 HTTPS 网址")
			}
		}
		if action == "edit-node" {
			if in.NodeID == "" {
				return errors.New("缺少 node_id：请填写 proxy.status 返回的节点 ID")
			}
			server := strings.Trim(strings.TrimSpace(in.Server), "[]")
			if len(server) > 253 || net.ParseIP(server) == nil && !nodeHostname.MatchString(server) {
				return errors.New("服务器地址无效，请填写 IP 地址或域名")
			}
			if in.ServerPort < 1 || in.ServerPort > 65535 {
				return errors.New("节点端口应为 1–65535")
			}
		}
	case "select":
		if len(in.NodeIDs) > 0 {
			return errors.New("proxy.select 只接受 node_id；多跳代理链请使用 proxy.route 的 node_ids")
		}
		if in.NodeID == "" {
			return errors.New("缺少 node_id：请填写 proxy.status 返回的节点 ID")
		}
	case "route":
		if in.NodeID != "" {
			return errors.New("proxy.route 只接受 node_ids：按入口到最终出口的顺序填写节点 ID")
		}
		if len(in.NodeIDs) == 0 {
			return errors.New("请至少选择一个最终出口")
		}
	}
	if (action == "select" || action == "restart" || action == "route") && in.Port != 0 && (in.Port < 1024 || in.Port > 65535) {
		return errors.New("代理端口应为 1024–65535")
	}
	return nil
}

func subscriptionURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}

func (s *Server) subscriptionClient(host bool) (*http.Client, error) {
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return nil, err
	}
	if host {
		return managedproxy.BootstrapClientWith(cfg.BootstrapEndpoints()), nil
	}
	if err = cfg.RequireManagedProxy(s.profile); err != nil {
		return nil, err
	}
	e, err := cage.NewEgress(cfg)
	if err != nil {
		return nil, err
	}
	return &http.Client{Transport: &http.Transport{Proxy: nil, DialContext: e.DialContext}, Timeout: 30 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 3 || r.URL.Scheme != "https" || r.URL.User != nil {
			return errors.New("订阅重定向被拒绝")
		}
		return nil
	}}, nil
}
func (s *Server) changeProxy(action string, in proxyInput, resumeVersion *uint64) error {
	p, err := managedproxy.Load(s.profile)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(s.configPath)
	if err != nil {
		return err
	}
	ctx := s.operationContext()
	if action == "import-local" {
		// Resolve the content-bound handle again inside the serialized job.
		// Never accept a path, subscription URL, or raw node secrets from the UI.
		in, err = s.localProxyConfig(in.ID)
		if err != nil {
			return err
		}
	}
	if action == "stop" {
		if err = s.closeProxyGate("内置代理已停止，外网保持关闭"); err != nil {
			return err
		}
		return managedproxy.Stop(s.profile)
	}
	if action == "select" || action == "restart" || action == "route" {
		if action == "route" {
			if len(in.NodeIDs) == 0 {
				return errors.New("请至少选择一个最终出口")
			}
			p.Chain = append([]string(nil), in.NodeIDs...)
			p.Selected = p.Chain[len(p.Chain)-1]
			// A one-node route is the interface's single-exit mode. It keeps
			// the exit's imported dialer-proxy hops, as select does, instead of
			// overriding them with a direct connection (#345).
			if len(p.Chain) == 1 {
				p.Chain = nil
			}
			if _, err := p.Route(); err != nil {
				return err
			}
		}
		if action == "select" {
			if _, ok := p.Find(in.NodeID); !ok {
				return errors.New("所选节点已不存在，请刷新列表")
			}
			// select 表示单节点出口：替换（而非改写）现有链，避免把所选节点
			// 追加到已含该节点的链里造成重复；需要多跳请使用 route。
			p.Selected = in.NodeID
			p.Chain = nil
		}
		if in.Port != 0 {
			if in.Port < 1024 || in.Port > 65535 {
				return errors.New("代理端口应为 1024–65535")
			}
			p.Port = in.Port
		}
		return s.applyProxy(p, cfg, resumeVersion, action != "restart")
	}
	_, oldRouteErr := p.Route()
	migrating := errors.Is(oldRouteErr, managedproxy.ErrLocalProxyNode)
	index := -1
	for i, src := range p.Sources {
		if src.ID == in.ID {
			index = i
			break
		}
	}
	if action == "delete" {
		if index < 0 {
			return errors.New("订阅来源不存在")
		}
		if len(p.Chain) > 1 {
			for _, id := range p.Chain {
				for _, n := range p.Sources[index].Nodes {
					if n.ID == id {
						return errors.New("该来源包含当前代理链中的节点，请先选择并应用其他来源的节点")
					}
				}
			}
		}
		p.Sources = append(p.Sources[:index], p.Sources[index+1:]...)
		if _, ok := p.Find(p.Selected); !ok {
			p.Selected = ""
			p.Chain = nil
		}
	} else if action == "edit-node" {
		// Address and port of a locally imported node can change in place; the
		// credentials and protocol options stay as imported (#204). Nodes of a
		// subscription are rewritten by its next refresh, so they are refused.
		if index < 0 {
			return errors.New("订阅来源不存在")
		}
		if p.Sources[index].URL != "" {
			return errors.New("订阅节点会在更新订阅时被覆盖，请在服务商处修改；只能编辑本地导入的节点")
		}
		server := strings.Trim(strings.TrimSpace(in.Server), "[]")
		if len(server) > 253 || net.ParseIP(server) == nil && !nodeHostname.MatchString(server) {
			return errors.New("服务器地址无效，请填写 IP 地址或域名")
		}
		if in.ServerPort < 1 || in.ServerPort > 65535 {
			return errors.New("节点端口应为 1–65535")
		}
		found := false
		for i, n := range p.Sources[index].Nodes {
			if n.ID == in.NodeID {
				config := maps.Clone(n.Config)
				config["server"], config["port"] = server, in.ServerPort
				p.Sources[index].Nodes[i].Config = config
				found = true
			}
		}
		if !found {
			return errors.New("节点不存在")
		}
		candidate := p
		if candidate.Selected == "" {
			candidate.Selected = in.NodeID
		}
		if err = validateProxyCore(ctx, s.profile, candidate); err != nil {
			return err
		}
	} else if action == "edit" {
		if index < 0 {
			return errors.New("订阅来源不存在")
		}
		if strings.TrimSpace(in.Name) == "" || len(in.Name) > 100 || in.Interval < 0 || in.Interval > 168 {
			return errors.New("名称或更新间隔无效")
		}
		p.Sources[index].Name = strings.TrimSpace(in.Name)
		p.Sources[index].Interval = in.Interval
		if in.URL != "" {
			u, e := url.Parse(in.URL)
			if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
				return errors.New("订阅需要完整的 HTTPS 网址")
			}
			p.Sources[index].URL = in.URL
		}
		return p.Save(s.profile)
	} else {
		var data []byte
		usage := ""
		if action == "refresh" {
			if index < 0 {
				return errors.New("订阅来源不存在")
			}
			src := p.Sources[index]
			if src.URL == "" {
				// A local source is updated from new YAML in place. The source ID
				// and node names keep node IDs, selection and exit bindings (#507).
				if strings.TrimSpace(in.Content) == "" {
					return errors.New("本地来源需要提供新的配置内容才能更新")
				}
				data = []byte(in.Content)
			} else if in.Content != "" {
				return errors.New("订阅来源从订阅链接更新，不能用粘贴的内容替换")
			}
			in.Name, in.URL, in.Interval = src.Name, src.URL, src.Interval
		} else {
			data = []byte(in.Content)
			if action != "import-local" {
				in.ID = ""
			}
		}
		if in.URL != "" && action != "import-local" {
			s.setMessage("正在下载节点订阅…")
			client, e := s.subscriptionClient(in.HostDownload)
			if e != nil {
				s.proxySourceFailure(in.ID, e)
				return e
			}
			defer client.CloseIdleConnections()
			data, usage, err = fetchProxySource(ctx, client, in.URL)
			if err != nil {
				if index >= 0 {
					p.Sources[index].Error = err.Error()
					p.Sources[index].Attempted = time.Now()
					_ = p.Save(s.profile)
				}
				return err
			}
		}
		if err = p.Import(in.Name, in.URL, data, in.ID, in.Interval, usage); err != nil {
			s.proxySourceFailure(in.ID, err)
			return err
		}
		candidate := p
		if _, ok := candidate.Find(candidate.Selected); !ok || migrating {
			// Validate new nodes without switching the saved environment chain.
			added := p.Sources[len(p.Sources)-1]
			if index >= 0 && in.ID != "" {
				added = p.Sources[index]
			}
			candidate.Selected = added.Nodes[0].ID
			candidate.Chain = nil
		}
		if err = validateProxyCore(ctx, s.profile, candidate); err != nil {
			s.proxySourceFailure(in.ID, err)
			return err
		}
		if _, ok := p.Find(p.Selected); !ok {
			p.Selected = ""
		}
	}
	if migrating {
		// Even a refreshed node with the same ID must be explicitly applied.
		p.Revision = ""
		return p.Save(s.profile)
	}
	if cfg.ProxyRevision != "" && cfg.ProxyRevision == p.Revision && cfg.ProxyURL == p.Endpoint() {
		if p.Selected == "" {
			if err = s.closeProxyGate("当前节点已被移除，请选择新节点"); err != nil {
				return err
			}
			if err = managedproxy.Stop(s.profile); err != nil {
				return err
			}
			return p.Save(s.profile)
		}
		return s.applyProxy(p, cfg, resumeVersion, false)
	}
	return p.Save(s.profile)
}

func (s *Server) proxySourceFailure(id string, failure error) {
	if id == "" {
		return
	}
	p, err := managedproxy.Load(s.profile)
	if err != nil {
		return
	}
	for i := range p.Sources {
		if p.Sources[i].ID == id {
			p.Sources[i].Error = failure.Error()
			p.Sources[i].Attempted = time.Now()
			_ = p.Save(s.profile)
			return
		}
	}
}
func (s *Server) closeProxyGate(reason string) error {
	if !s.vmRunning() {
		return nil
	}
	return s.revokeNetworkNow(reason, false)
}

func (s *Server) activateProxy(p managedproxy.Store, old cage.Config) (returned error) {
	// The explicit start operation already requested networking. A pause made
	// after that request must survive any core recovery within the start.
	return s.applyProxy(p, old, nil, false)
}

func (s *Server) applyProxy(p managedproxy.Store, old cage.Config, resumeVersion *uint64, selectExit bool) (returned error) {
	ctx := s.operationContext()
	previous, err := managedproxy.Load(s.profile)
	if err != nil {
		return err
	}
	s.setMessage("正在验证节点配置…")
	// The core resolves with the same endpoints as guest DNS and the DNS check.
	p.GuestDNS, p.BootstrapDNS = old.DNSEndpoints(), old.BootstrapEndpoints()
	if err := validateProxyCore(ctx, s.profile, p); err != nil {
		return err
	}
	// Changing the endpoint requires an environment restart; node-only switches
	// keep its SSH sessions and VM alive and revoke the gate synchronously.
	restartVM := s.vmRunning() && old.ProxyURL != p.Endpoint()
	if err := s.closeProxyGate("代理节点切换中，旧连接及检测授权已关闭"); err != nil {
		return err
	}
	if restartVM {
		if err := s.stopEnvironment(); err != nil {
			return err
		}
	}
	s.setMessage("正在应用内置代理节点…")
	if err := managedproxy.Stop(s.profile); err != nil {
		return err
	}
	committed := false
	defer func() {
		if returned != nil && !committed {
			_ = previous.Save(s.profile)
			_ = writePrivate(s.configPath, old)
		} else if returned != nil && !errors.Is(returned, context.Canceled) {
			returned = fmt.Errorf("代理节点已保存，外网尚未恢复；请重新检查联网条件：%w", usererror.Display("恢复联网", returned, "请重新检测；仍失败时导出诊断包。"))
		}
	}()
	p.Revision = managedproxy.Token()
	cfg := old
	cfg.ExitPolicy = selectProxyExitPolicy(&p, previous, old.ExitPolicy, selectExit)
	if err := p.Save(s.profile); err != nil {
		return err
	}
	cfg.ProxyURL = p.Endpoint()
	cfg.ProxyRevision = p.Revision
	if err := writePrivate(s.configPath, cfg); err != nil {
		return err
	}
	if err := startProxyCore(ctx, s.profile, p); err != nil {
		return err
	}
	committed = true
	s.jobs.execution().settingsSaved()
	if resumeVersion != nil {
		if err := s.resumeNetworkIntentAt(*resumeVersion); err != nil {
			return err
		}
	}
	s.mu.Lock()
	// Only a user request may resume networking, and a later pause wins even
	// when it arrived while subscription download or core startup was pending.
	paused := s.networkPaused
	s.proxyNetworkLost = false
	s.proxyNetworkRetry = time.Time{}
	s.exitProbeError = ""
	s.locationError = ""
	s.location = nil
	s.safety.Report = nil
	s.mu.Unlock()
	if s.vmRunning() {
		client, err := s.gateClient()
		if err != nil {
			return err
		}
		state, err := client.SyncProxyRevision(ctx)
		if err != nil {
			return err
		}
		s.publishGate(state)
		if paused {
			return nil
		}
		s.setMessage("节点已应用，正在重新检查环境出口…")
		return s.runSafetyCheck()
	}
	if restartVM {
		return s.startEnvironment(false)
	}
	s.setMessage("节点已应用；启动 Cage 环境后完成联网检查")
	return nil
}

var nodeHostname = regexp.MustCompile(`^(?i:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)(?:\.(?i:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?))*$`)

// measureProxyDelay asks the running core to time one node. Tests replace it.
var measureProxyDelay = func(ctx context.Context, profile, nodeID string) (int, error) {
	var v struct {
		Delay int `json:"delay"`
	}
	err := managedproxy.API(ctx, profile, "GET", "/proxies/"+url.PathEscape(nodeID)+"/delay?timeout=5000&url="+url.QueryEscape("https://www.gstatic.com/generate_204"), nil, &v)
	if err != nil {
		return 0, managedproxy.DelayError(err)
	}
	return v.Delay, nil
}

const (
	// Batch measurement shares the core with live traffic. A small pool keeps
	// upstream connection bursts bounded; the cap bounds one request's duration.
	proxyDelayWorkers = 4
	proxyDelayMaxIDs  = 300
)

type proxyDelayResult struct {
	NodeID string `json:"node_id"`
	Delay  int    `json:"delay,omitempty"`
	Error  string `json:"error,omitempty"`
}

// proxyDelay measures one node (node_id, reply {"delay"}) or several
// (node_ids, reply {"results", "best"}). It never changes the selected node:
// "best" is the fastest successful node, which the caller may apply with
// proxy.select, so the usual exit and network checks still run.
func (s *Server) proxyDelay(w http.ResponseWriter, r *http.Request) {
	var in proxyInput
	if !readJSON(w, r, &in) {
		return
	}
	p, err := managedproxy.Load(s.profile)
	if err != nil {
		requestError(w, err)
		return
	}
	if len(in.NodeIDs) == 0 {
		if _, ok := p.Find(in.NodeID); !ok {
			requestError(w, errors.New("节点不存在"))
			return
		}
		if reason := p.BlockedNodes()[in.NodeID]; reason != "" {
			requestError(w, errors.New(reason))
			return
		}
		if err := s.ensureProxyInterfaceBeforeUse(r.Context()); err != nil {
			requestError(w, err)
			return
		}
		delay, err := measureProxyDelay(r.Context(), s.profile, in.NodeID)
		if err != nil {
			requestError(w, err)
			return
		}
		jsonReply(w, map[string]int{"delay": delay})
		return
	}
	if in.NodeID != "" {
		requestError(w, errors.New("node_id 与 node_ids 只能填写一个"))
		return
	}
	if len(in.NodeIDs) > proxyDelayMaxIDs {
		requestError(w, fmt.Errorf("一次最多测速 %d 个节点", proxyDelayMaxIDs))
		return
	}
	seen := map[string]bool{}
	for _, id := range in.NodeIDs {
		if _, ok := p.Find(id); !ok {
			requestError(w, errors.New("节点不存在"))
			return
		}
		if seen[id] {
			requestError(w, errors.New("测速列表包含重复节点"))
			return
		}
		seen[id] = true
	}
	if err := s.ensureProxyInterfaceBeforeUse(r.Context()); err != nil {
		requestError(w, err)
		return
	}
	blocked := p.BlockedNodes()
	results := make([]proxyDelayResult, len(in.NodeIDs))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(proxyDelayWorkers, len(in.NodeIDs)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				id := in.NodeIDs[i]
				if reason := blocked[id]; reason != "" {
					results[i] = proxyDelayResult{NodeID: id, Error: reason}
					continue
				}
				delay, err := measureProxyDelay(r.Context(), s.profile, id)
				results[i] = proxyDelayResult{NodeID: id, Delay: delay}
				if err != nil {
					results[i] = proxyDelayResult{NodeID: id, Error: err.Error()}
				}
			}
		}()
	}
	for i := range in.NodeIDs {
		if r.Context().Err() != nil {
			break
		}
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if err := r.Context().Err(); err != nil {
		requestError(w, errors.New("测速已取消"))
		return
	}
	best := ""
	bestDelay := 0
	for _, result := range results {
		if result.Error == "" && result.Delay > 0 && (best == "" || result.Delay < bestDelay) {
			best, bestDelay = result.NodeID, result.Delay
		}
	}
	jsonReply(w, map[string]any{"results": results, "best": best})
}
func (s *Server) proxyConnections(w http.ResponseWriter, r *http.Request) {
	var response struct {
		Upload      int64 `json:"uploadTotal"`
		Download    int64 `json:"downloadTotal"`
		Connections []struct {
			ID       string `json:"id"`
			Upload   int64  `json:"upload"`
			Download int64  `json:"download"`
			Metadata struct {
				Network string `json:"network"`
				Host    string `json:"host"`
				IP      string `json:"destinationIP"`
				Port    string `json:"destinationPort"`
			} `json:"metadata"`
		} `json:"connections"`
	}
	if err := managedproxy.API(r.Context(), s.profile, "GET", "/connections", nil, &response); err != nil {
		jsonReply(w, map[string]any{"connections": []any{}, "uploadTotal": 0, "downloadTotal": 0})
		return
	}
	items := []any{}
	for i, c := range response.Connections {
		if i >= 100 {
			break
		}
		host := c.Metadata.Host
		if host == "" {
			host = c.Metadata.IP
		}
		items = append(items, map[string]any{"id": c.ID, "host": host, "port": c.Metadata.Port, "network": c.Metadata.Network, "upload": c.Upload, "download": c.Download})
	}
	jsonReply(w, map[string]any{"connections": items, "uploadTotal": response.Upload, "downloadTotal": response.Download})
}
func (s *Server) proxyLogs(w http.ResponseWriter, r *http.Request) {
	p, err := managedproxy.Load(s.profile)
	if err != nil {
		requestError(w, err)
		return
	}
	// Whitelisted lifecycle messages avoid publishing upstream raw log lines,
	// which can contain subscription secrets, credentials and host paths.
	lines := []string{"客机公网 UDP、IPv6、ICMP：阻断", "业务 DNS：经选定节点转发", "代理基础设施 DNS：" + strings.Join(p.NodeResolvers(), "、") + "（主机网络，并行查询）"}
	if proxyInterfaceCoreRunning(r.Context(), s.profile, p) {
		lines = append(lines, "内置核心运行中；已读回当前节点选择")
		if cfg, cfgErr := loadConfig(s.configPath); cfgErr == nil && s.proxyStore.Applied(s.profile, cfg.ProxyURL, cfg.ProxyRevision) == nil {
			if bindErr := s.checkProxyInterfaceBinding(r.Context()); bindErr != nil && !os.IsNotExist(bindErr) {
				lines = append(lines, bindErr.Error())
			}
		}
	} else {
		lines = append(lines, "内置核心未就绪；不会直连回退")
		if _, err := proxyCorePath(); err != nil {
			lines = append(lines, err.Error())
		}
	}
	for _, src := range p.Sources {
		lines = append(lines, src.Updated.Format("2006-01-02 15:04")+" · "+src.Name+" · "+src.Error)
	}
	s.mu.Lock()
	if issue := s.currentNetworkErrorLocked(); issue != "" {
		lines = append(lines, issue)
	}
	s.mu.Unlock()
	jsonReply(w, map[string]any{"lines": lines})
}
func (s *Server) refreshProxySubscriptions() {
	if s.busy() {
		return
	}
	p, err := s.proxyStore.Load(s.profile)
	if err != nil {
		return
	}
	if _, err := p.Route(); errors.Is(err, managedproxy.ErrLocalProxyNode) {
		return
	}
	for _, src := range p.Sources {
		if src.URL != "" && src.Interval > 0 && time.Since(src.Updated) >= time.Duration(src.Interval)*time.Hour && time.Since(src.Attempted) > 15*time.Minute {
			// Automatic refresh never opts into a host-network fallback.
			_, _ = s.submitJob("proxy-update", func() error { return s.changeProxy("refresh", proxyInput{ID: src.ID}, nil) })
			return
		}
	}
}

// Before serving requests, retire an older core that could still expose local
// nodes through the environment group or through per-account credentials.
func (s *Server) retireLocalProxyCore() error {
	path := filepath.Join(managedproxy.Dir(s.profile), "config.yaml")
	file, statErr := os.Stat(path)
	if statErr != nil {
		return nil
	}
	_, err := managedproxy.BoundInterface(path)
	if !errors.Is(err, managedproxy.ErrLocalProxyNode) {
		return nil
	}
	_ = s.closeProxyGate(err.Error())
	// CLI operations can replace the generated file while the gate is closing.
	current, err := os.Stat(path)
	if err != nil || !os.SameFile(file, current) {
		return nil
	}
	if err := managedproxy.Stop(s.profile); err != nil {
		return err
	}
	// The source records used for migration remain in sources.json. Without
	// the retired runtime the next core start must render the filtered store.
	if current, err := os.Stat(path); err == nil && os.SameFile(file, current) {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	s.mu.Lock()
	s.proxyNetworkLost = true
	s.proxyInterfaceLost, s.proxyInterfaceError = false, ""
	s.mu.Unlock()
	return nil
}

const proxyNotReadyReason = "内置代理未就绪；外网保持关闭"

func (s *Server) checkProxyHealth() {
	s.checkProxyHealthWith(managedproxy.RunningStage)
}

func (s *Server) checkProxyHealthWith(runningStage func(context.Context, string, managedproxy.Store) string) {
	if s.busy() {
		return
	}
	cfg, err := loadConfig(s.configPath)
	if !s.vmRunning() {
		return
	}
	if err != nil {
		_ = s.closeProxyGate("代理配置无法读取，外网保持关闭")
		return
	}
	// Every five seconds: reuse the parse while the store bytes are unchanged.
	p, err := s.proxyStore.Load(s.profile)
	ctx, cancel := context.WithTimeout(s.lifetimeContext(), 2*time.Second)
	defer cancel()
	reason := proxyNotReadyReason
	stage := ""
	if err != nil {
		stage = "store.load"
	} else if _, routeErr := p.Route(); errors.Is(routeErr, managedproxy.ErrLocalProxyNode) {
		reason = routeErr.Error()
		stage = "route.local_node"
	} else if s.proxyStore.Applied(s.profile, cfg.ProxyURL, cfg.ProxyRevision) != nil {
		stage = "store.unapplied"
	} else if stage = runningStage(ctx, s.profile, p); stage == "" {
		s.scheduleProxyHealthRecovery(ctx, cfg.Fingerprint())
		return
	}
	s.mu.Lock()
	previouslyOpen := s.safety.Gate != nil && s.safety.Gate.Mode == cage.GateOpen
	previousError := s.safety.monitorError
	s.mu.Unlock()
	_ = s.closeProxyGate(reason)
	s.mu.Lock()
	s.safety.monitorError = reason
	if s.recovery.RunID != "" {
		s.recovery = networkRecovery{}
	}
	s.mu.Unlock()
	if previouslyOpen || previousError != reason {
		diagnostics.Log("warn", "proxy", "proxy health check failed; egress gate closed (stage="+stage+")")
	}
}

// After a transient proxy health check failure closes the gate, a recovered
// core never reopens networking directly: it schedules one ordinary full
// safety recheck for that exact closed gate epoch and configuration.
func (s *Server) scheduleProxyHealthRecovery(ctx context.Context, configuration string) {
	s.mu.Lock()
	paused, closing, ready := s.networkPaused, s.closing, s.ready
	gate := s.safety.Gate
	pauseVersion := s.safety.pauseVersion
	existing := s.recovery
	s.mu.Unlock()
	if paused || closing || !ready || gate == nil || gate.Mode != cage.GateClosed || gate.Reason != proxyNotReadyReason || gate.Configuration != configuration || gate.Report == nil {
		return
	}
	if recoveryMatches(existing, *gate, configuration, paused, pauseVersion) {
		return
	}
	client, err := s.gateClient()
	if err != nil {
		return
	}
	state, err := client.Status(ctx)
	if err != nil {
		return
	}
	s.publishGate(state)
	if state.Mode != cage.GateClosed || state.Reason != proxyNotReadyReason || state.Configuration != configuration || state.Report == nil {
		return
	}
	s.mu.Lock()
	if s.networkPaused || s.closing || !s.ready ||
		s.safety.pauseVersion != pauseVersion ||
		s.safety.Gate == nil || s.safety.Gate.RunID != state.RunID || s.safety.Gate.Epoch != state.Epoch || s.safety.Gate.Mode != cage.GateClosed ||
		recoveryMatches(s.recovery, state, configuration, s.networkPaused, s.safety.pauseVersion) {
		s.mu.Unlock()
		return
	}
	s.recovery = networkRecovery{
		RunID:         state.RunID,
		Configuration: state.Configuration,
		Epoch:         state.Epoch,
		PauseVersion:  s.safety.pauseVersion,
		Next:          time.Now().Add(15 * time.Second),
	}
	s.mu.Unlock()
	diagnostics.Log("warn", "network", "temporary monitoring failure; full recheck scheduled: "+state.Reason)
}
