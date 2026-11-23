package workbench

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sylvinus/agent-vm/internal/desktop"
	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/hostbridge"
	"github.com/sylvinus/agent-vm/internal/remotehost"
	"github.com/sylvinus/agent-vm/internal/version"
	"golang.org/x/net/http/httpguts"
)

const (
	remotePluginBridgeProtocol   = "cage-plugin-bridge-v1"
	remotePluginBridgeFrameLimit = 4 << 20
	remotePluginBridgePingEvery  = 20 * time.Second
	remotePluginBridgeCallWait   = 3 * time.Minute
)

type bridgePluginSpec struct {
	Name        string      `json:"name"`
	DisplayName string      `json:"display_name,omitempty"`
	Tools       []*mcp.Tool `json:"tools"`
}

type bridgeFrame struct {
	Type      string                    `json:"type"`
	ID        string                    `json:"id,omitempty"`
	Plugins   []bridgePluginSpec        `json:"plugins,omitempty"`
	Accepted  []hostbridge.ToolInfo     `json:"accepted,omitempty"`
	Skipped   []hostbridge.ProxySkipped `json:"skipped,omitempty"`
	Tool      string                    `json:"tool,omitempty"`
	Arguments json.RawMessage           `json:"arguments,omitempty"`
	Result    *mcp.CallToolResult       `json:"result,omitempty"`
	Error     string                    `json:"error,omitempty"`
}

type hostClientBridgeView struct {
	Name        string                    `json:"name"`
	DisplayName string                    `json:"display_name,omitempty"`
	Tools       []hostbridge.ToolInfo     `json:"tools,omitempty"`
	Skipped     []hostbridge.ProxySkipped `json:"skipped,omitempty"`
}

type bridgeCallReply struct {
	result *mcp.CallToolResult
	err    error
}

type serverBridgeConn struct {
	stream  io.ReadWriteCloser
	writeMu sync.Mutex
	closed  chan struct{}
	once    sync.Once

	mu      sync.Mutex
	pending map[string]chan bridgeCallReply
	plugins []bridgePluginSpec
	views   []hostClientBridgeView
}

func newServerBridgeConn(stream io.ReadWriteCloser) *serverBridgeConn {
	return &serverBridgeConn{
		stream:  stream,
		closed:  make(chan struct{}),
		pending: map[string]chan bridgeCallReply{},
	}
}

func (c *serverBridgeConn) close() {
	c.once.Do(func() {
		close(c.closed)
		_ = c.stream.Close()
		c.mu.Lock()
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.mu.Unlock()
	})
}

func (c *serverBridgeConn) send(frame bridgeFrame) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if len(data) > remotePluginBridgeFrameLimit {
		return errors.New("bridge frame exceeds size limit")
	}
	data = append(data, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.closed:
		return io.ErrClosedPipe
	default:
	}
	_, err = c.stream.Write(data)
	return err
}

func (c *serverBridgeConn) callTool(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	id := randomToken()[:24]
	ch := make(chan bridgeCallReply, 1)
	c.mu.Lock()
	select {
	case <-c.closed:
		c.mu.Unlock()
		return nil, errors.New("the tool is no longer available")
	default:
		c.pending[id] = ch
	}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	if err := c.send(bridgeFrame{Type: "call", ID: id, Tool: name, Arguments: args}); err != nil {
		c.close()
		return nil, errors.New("the tool became unavailable before running")
	}

	timer := time.NewTimer(remotePluginBridgeCallWait)
	defer timer.Stop()
	select {
	case reply, ok := <-ch:
		if !ok {
			return nil, errors.New("the tool became unavailable while running")
		}
		return reply.result, reply.err
	case <-ctx.Done():
		_ = c.send(bridgeFrame{Type: "cancel", ID: id})
		return nil, ctx.Err()
	case <-timer.C:
		_ = c.send(bridgeFrame{Type: "cancel", ID: id})
		return nil, hostbridge.ErrCallTimedOut
	case <-c.closed:
		return nil, errors.New("the tool became unavailable while running")
	}
}

// serverClientPluginBridge manages the reverse-bridged client plugins attached
// to this server's hostbridge.Bridge.
type serverClientPluginBridge struct {
	mu     sync.Mutex
	active *serverBridgeConn
}

func (b *serverClientPluginBridge) attach(conn *serverBridgeConn) {
	b.mu.Lock()
	previous := b.active
	b.active = conn
	b.mu.Unlock()
	if previous != nil && previous != conn {
		previous.close()
	}
}

func (b *serverClientPluginBridge) detach(conn *serverBridgeConn, bridge *hostbridge.Bridge) {
	b.mu.Lock()
	removed := false
	if b.active == conn {
		b.active = nil
		removed = true
	}
	b.mu.Unlock()
	if removed && bridge != nil {
		bridge.SetEphemeral()
	}
}

func (b *serverClientPluginBridge) close() {
	b.mu.Lock()
	active := b.active
	b.active = nil
	b.mu.Unlock()
	if active != nil {
		active.close()
	}
}

func (b *serverClientPluginBridge) applyToBridge(bridge *hostbridge.Bridge) ([]hostbridge.ToolInfo, []hostbridge.ProxySkipped) {
	b.mu.Lock()
	active := b.active
	b.mu.Unlock()
	if active == nil {
		if bridge != nil {
			bridge.SetEphemeral()
		}
		return nil, nil
	}
	active.mu.Lock()
	specs := slices.Clone(active.plugins)
	active.mu.Unlock()

	var allTools []*mcp.Tool
	toolOwner := map[string]string{}
	for _, spec := range specs {
		for _, tool := range spec.Tools {
			if tool == nil {
				continue
			}
			allTools = append(allTools, tool)
			if _, exists := toolOwner[tool.Name]; !exists {
				toolOwner[tool.Name] = spec.Name
			}
		}
	}
	var taken, takenPrompts map[string]bool
	if bridge != nil {
		taken = bridge.BaseToolNames()
		takenPrompts = bridge.BasePromptNames()
	}
	proxy, err := hostbridge.NewProxyPlugin(allTools, taken, active.callTool)
	if err != nil {
		if bridge != nil {
			bridge.SetEphemeral()
		}
		return nil, []hostbridge.ProxySkipped{{Name: "*", Reason: err.Error()}}
	}
	accepted := proxy.ToolList()
	skipped := proxy.Skipped()

	acceptedByPlugin := map[string][]hostbridge.ToolInfo{}
	for _, item := range accepted {
		owner := toolOwner[item.Name]
		acceptedByPlugin[owner] = append(acceptedByPlugin[owner], item)
	}
	promptGroups := make([]hostbridge.ProxyPromptGroup, 0, len(specs))
	for _, spec := range specs {
		var names []string
		for _, item := range acceptedByPlugin[spec.Name] {
			names = append(names, item.Name)
		}
		promptGroups = append(promptGroups, hostbridge.ProxyPromptGroup{
			Name:        spec.Name,
			DisplayName: spec.DisplayName,
			Tools:       names,
		})
	}
	proxy.SetPluginPrompts(promptGroups, takenPrompts)
	if bridge != nil {
		bridge.SetEphemeral(proxy)
	}
	skippedByPlugin := map[string][]hostbridge.ProxySkipped{}
	for _, item := range skipped {
		owner := toolOwner[item.Name]
		skippedByPlugin[owner] = append(skippedByPlugin[owner], item)
	}
	views := make([]hostClientBridgeView, 0, len(specs))
	for _, spec := range specs {
		views = append(views, hostClientBridgeView{
			Name:        spec.Name,
			DisplayName: spec.DisplayName,
			Tools:       acceptedByPlugin[spec.Name],
			Skipped:     skippedByPlugin[spec.Name],
		})
	}
	active.mu.Lock()
	active.views = views
	active.mu.Unlock()
	return accepted, skipped
}

func (b *serverClientPluginBridge) views() []hostClientBridgeView {
	b.mu.Lock()
	active := b.active
	b.mu.Unlock()
	if active == nil {
		return nil
	}
	active.mu.Lock()
	defer active.mu.Unlock()
	return slices.Clone(active.views)
}

func (s *Server) reattachClientPluginBridge(bridge *hostbridge.Bridge) {
	if bridge == nil {
		return
	}
	s.clientBridge.applyToBridge(bridge)
}

// remoteClientPluginBridge accepts an SSH-authenticated desktop client upgrade
// that exposes selected client-side host plugins to the remote Claude bridge.
func (s *Server) remoteClientPluginBridge(w http.ResponseWriter, r *http.Request) {
	if !s.hasRemoteDesktopView(r) {
		http.Error(w, "需要远程桌面客户端身份", http.StatusForbidden)
		return
	}
	if !httpguts.HeaderValuesContainsToken(r.Header["Connection"], "Upgrade") ||
		!strings.EqualFold(r.Header.Get("Upgrade"), remotePluginBridgeProtocol) {
		http.Error(w, "需要升级到反向插件通道", http.StatusUpgradeRequired)
		return
	}
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, "无法建立反向插件通道", http.StatusInternalServerError)
		return
	}
	if _, err := io.WriteString(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: "+remotePluginBridgeProtocol+"\r\n\r\n"); err != nil {
		_ = conn.Close()
		return
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return
	}
	stream := &hijackedStream{ReadWriteCloser: conn, reader: rw.Reader}
	client := newServerBridgeConn(stream)
	s.clientBridge.attach(client)
	defer func() {
		s.mu.Lock()
		var bridge *hostbridge.Bridge
		if s.hostTools != nil {
			bridge = s.hostTools.bridge
		}
		s.mu.Unlock()
		s.clientBridge.detach(client, bridge)
		client.close()
	}()

	go func() {
		ticker := time.NewTicker(remotePluginBridgePingEvery)
		defer ticker.Stop()
		for {
			select {
			case <-s.lifetimeContext().Done():
				client.close()
				return
			case <-client.closed:
				return
			case <-ticker.C:
				if err := client.send(bridgeFrame{Type: "ping"}); err != nil {
					client.close()
					return
				}
			}
		}
	}()

	scanner := bufio.NewScanner(client.stream)
	scanner.Buffer(make([]byte, 8192), remotePluginBridgeFrameLimit)
	for scanner.Scan() {
		var frame bridgeFrame
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			return
		}
		switch frame.Type {
		case "pong":
		case "register":
			client.mu.Lock()
			client.plugins = frame.Plugins
			client.mu.Unlock()
			s.mu.Lock()
			var bridge *hostbridge.Bridge
			if s.hostTools != nil {
				bridge = s.hostTools.bridge
			}
			s.mu.Unlock()
			accepted, skipped := s.clientBridge.applyToBridge(bridge)
			if err := client.send(bridgeFrame{Type: "registered", Accepted: accepted, Skipped: skipped}); err != nil {
				return
			}
		case "result":
			client.mu.Lock()
			ch := client.pending[frame.ID]
			delete(client.pending, frame.ID)
			client.mu.Unlock()
			if ch != nil {
				var err error
				if frame.Error != "" {
					err = errors.New(frame.Error)
				}
				ch <- bridgeCallReply{result: frame.Result, err: err}
			}
		}
	}
}

// --- Client-side reverse bridge manager ---

type clientBridgePluginItem struct {
	Name        string                    `json:"name"`
	DisplayName string                    `json:"display_name,omitempty"`
	Version     string                    `json:"version,omitempty"`
	Description string                    `json:"description,omitempty"`
	Permissions []string                  `json:"permissions"`
	Shared      bool                      `json:"shared"`
	Running     bool                      `json:"running"`
	Tools       []hostbridge.ToolInfo     `json:"tools,omitempty"`
	Skipped     []hostbridge.ProxySkipped `json:"skipped,omitempty"`
	Problem     string                    `json:"problem,omitempty"`
}

type clientBridgeStatus struct {
	Supported bool                     `json:"supported"`
	Connected bool                     `json:"connected"`
	Plugins   []clientBridgePluginItem `json:"plugins"`
	Error     string                   `json:"error,omitempty"`
}

type clientPluginBridgeManager struct {
	session    *remotehost.Session
	hostName   string
	profileDir string
	ask        func(context.Context, string, string) (desktop.Scope, bool)
	grants     *hostGrants

	ctx    context.Context
	cancel context.CancelFunc

	mu          sync.Mutex
	shared      map[string]bool
	running     map[string]*hostbridge.External
	problems    map[string]string
	accepted    map[string][]hostbridge.ToolInfo
	skipped     map[string][]hostbridge.ProxySkipped
	stream      io.ReadWriteCloser
	streamMu    sync.Mutex
	streamReady bool
	streamDone  chan struct{}
	streamErr   string
}

func newClientPluginBridgeManager(session *remotehost.Session, hostName, profileDir string, ask func(context.Context, string, string) (desktop.Scope, bool)) *clientPluginBridgeManager {
	if ask == nil {
		ask = desktop.AskScope
	}
	ctx, cancel := context.WithCancel(session.Context)
	m := &clientPluginBridgeManager{
		session:    session,
		hostName:   hostName,
		profileDir: profileDir,
		ask:        ask,
		grants:     &hostGrants{path: filepath.Join(profileDir, "host-grants.json")},
		ctx:        ctx,
		cancel:     cancel,
		shared:     map[string]bool{},
		running:    map[string]*hostbridge.External{},
		problems:   map[string]string{},
		accepted:   map[string][]hostbridge.ToolInfo{},
		skipped:    map[string][]hostbridge.ProxySkipped{},
	}
	context.AfterFunc(ctx, func() { m.closeAll() })
	return m
}

func (m *clientPluginBridgeManager) close() {
	m.cancel()
	m.closeAll()
}

func (m *clientPluginBridgeManager) closeAll() {
	m.mu.Lock()
	stream := m.stream
	m.stream = nil
	m.streamReady = false
	done := m.streamDone
	m.streamDone = nil
	exts := make([]*hostbridge.External, 0, len(m.running))
	for name, ext := range m.running {
		exts = append(exts, ext)
		delete(m.running, name)
	}
	m.mu.Unlock()
	if stream != nil {
		_ = stream.Close()
	}
	if done != nil {
		<-done
	}
	for _, ext := range exts {
		ext.Close()
	}
}

func (m *clientPluginBridgeManager) pluginsDir() string {
	return filepath.Join(m.profileDir, "plugins")
}

func (m *clientPluginBridgeManager) readLocalSettings(name string) map[string]string {
	var all map[string]map[string]string
	data, err := os.ReadFile(filepath.Join(m.profileDir, "plugin-settings.json"))
	if err != nil {
		return nil
	}
	if json.Unmarshal(data, &all) != nil {
		return nil
	}
	return all[name]
}

func (m *clientPluginBridgeManager) pluginEnv(manifest hostbridge.Manifest) ([]string, error) {
	if manifest.Contributes == nil || len(manifest.Contributes.Settings) == 0 {
		return nil, nil
	}
	saved := m.readLocalSettings(manifest.Name)
	var env []string
	var missing []string
	for _, item := range manifest.Contributes.Settings {
		if item.Type == hostbridge.SettingSecret {
			if item.Required {
				return nil, errors.New("需要密钥设置的本机插件暂不支持反向共享给远程主机")
			}
			continue
		}
		val := strings.TrimSpace(saved[item.ID])
		if val == "" {
			val = item.Default
		}
		if val == "" {
			if item.Required {
				missing = append(missing, item.Title)
			}
			continue
		}
		if hostbridge.ValidSettingValue(val) {
			env = append(env, item.Env+"="+val)
		}
	}
	if len(missing) > 0 {
		return nil, &hostbridge.MissingSettingsError{Plugin: manifest.Name, Settings: missing}
	}
	return env, nil
}

func (m *clientPluginBridgeManager) localApprove(manifest hostbridge.Manifest) func(context.Context, hostbridge.ToolRequest) error {
	approvals := newHostApprovals()
	approvals.ask = m.ask
	approvals.grants = m.grants
	approvals.permissions = map[string][]string{manifest.Name: manifest.Permissions}
	label := strings.TrimSpace(m.hostName)
	if label == "" {
		label = "远程主机"
	}
	return func(ctx context.Context, request hostbridge.ToolRequest) error {
		title := fmt.Sprintf("允许远程主机「%s」使用你这台电脑上的插件？", label)
		msg := "请求来源　远程主机「" + label + "」\n插件「" + request.Plugin + "」\n工具　" + request.Tool + "\n\n" + request.Arguments +
			"\n\n插件将以你的系统账户权限在当前这台电脑上运行。「始终允许」只记住远程主机对该插件的使用，可在插件页随时撤销。"
		return approvals.decide(ctx, hostGrantRemotePlugin+request.Plugin, manifest.Permissions, request.Arguments, len(request.Arguments), title, msg, remoteApprovalPrompt{})
	}
}

func (m *clientPluginBridgeManager) setShared(ctx context.Context, name string, shared bool) (clientBridgeStatus, error) {
	if !m.session.Manifest.Capabilities["client_plugin_bridge"] {
		return m.snapshot(), errors.New("此远程 Cage 尚不支持本机插件反向桥接")
	}
	if !hostbridge.ValidPluginName(name) {
		return m.snapshot(), errors.New("插件名称无效")
	}
	if !shared {
		m.mu.Lock()
		delete(m.shared, name)
		ext := m.running[name]
		delete(m.running, name)
		delete(m.problems, name)
		delete(m.accepted, name)
		delete(m.skipped, name)
		remaining := len(m.running)
		m.mu.Unlock()
		if ext != nil {
			ext.Close()
		}
		if remaining == 0 {
			m.closeStreamOnly()
		} else {
			_ = m.pushRegistration()
		}
		return m.snapshot(), nil
	}

	dir := filepath.Join(m.pluginsDir(), name)
	manifest, err := hostbridge.LoadManifest(dir)
	if err != nil || manifest.Name != name {
		return m.snapshot(), errors.New("本机没有找到这个插件")
	}
	if manifest.Kind != hostbridge.KindHost {
		return m.snapshot(), errors.New("只有本机插件需要反向桥接；环境内插件会自动同步到远程")
	}
	if !manifest.Supported() || manifest.Unavailable(version.Version, true) != "" {
		return m.snapshot(), errors.New("这个插件不支持当前本机系统或版本")
	}
	if len(manifest.ServerCommand()) == 0 {
		return m.snapshot(), errors.New("这个插件没有提供可供 Claude 调用的 MCP 工具")
	}
	if hostbridge.CommandUsesProjectDir(manifest.ServerCommand()) {
		return m.snapshot(), errors.New("依赖本机当前项目目录的插件暂不支持反向桥接")
	}

	m.mu.Lock()
	already := m.running[name]
	m.shared[name] = true
	delete(m.problems, name)
	m.mu.Unlock()

	if already == nil {
		ext := &hostbridge.External{
			Manifest: manifest,
			Dir:      dir,
			Approve:  m.localApprove(manifest),
			Env: func(context.Context) ([]string, error) {
				return m.pluginEnv(manifest)
			},
		}
		startCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		err := ext.Start(startCtx)
		cancel()
		if err != nil {
			ext.Close()
			m.mu.Lock()
			delete(m.shared, name)
			m.problems[name] = err.Error()
			m.mu.Unlock()
			return m.snapshot(), err
		}
		m.mu.Lock()
		if m.ctx.Err() != nil || !m.shared[name] {
			m.mu.Unlock()
			ext.Close()
			return m.snapshot(), errors.New("远程连接已变化")
		}
		m.running[name] = ext
		m.mu.Unlock()
	}

	if err := m.ensureStream(ctx); err != nil {
		m.mu.Lock()
		m.streamErr = err.Error()
		m.mu.Unlock()
		return m.snapshot(), err
	}
	if err := m.pushRegistration(); err != nil {
		return m.snapshot(), err
	}
	return m.snapshot(), nil
}

func (m *clientPluginBridgeManager) closeStreamOnly() {
	m.mu.Lock()
	stream := m.stream
	m.stream = nil
	m.streamReady = false
	done := m.streamDone
	m.streamDone = nil
	m.streamErr = ""
	m.mu.Unlock()
	if stream != nil {
		_ = stream.Close()
	}
	if done != nil {
		<-done
	}
}

func (m *clientPluginBridgeManager) ensureStream(ctx context.Context) error {
	m.mu.Lock()
	if m.stream != nil && m.streamReady {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.session.Origin+"/api/host-tools/client-bridge", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", remotePluginBridgeProtocol)
	res, err := m.session.Transport.RoundTrip(req)
	if err != nil {
		return errors.New("无法建立本机插件反向通道")
	}
	stream, ok := res.Body.(io.ReadWriteCloser)
	if res.StatusCode != http.StatusSwitchingProtocols || !ok || !strings.EqualFold(res.Header.Get("Upgrade"), remotePluginBridgeProtocol) {
		res.Body.Close()
		if res.StatusCode == http.StatusLocked {
			return errors.New("请先解锁远程 Cage 账户，再共享本机插件")
		}
		return fmt.Errorf("远程服务未接受反向桥接（HTTP %d）", res.StatusCode)
	}

	done := make(chan struct{})
	m.mu.Lock()
	if m.ctx.Err() != nil {
		m.mu.Unlock()
		_ = stream.Close()
		return errors.New("远程连接已断开")
	}
	oldStream := m.stream
	m.stream = stream
	m.streamReady = true
	m.streamDone = done
	m.streamErr = ""
	m.mu.Unlock()
	if oldStream != nil {
		_ = oldStream.Close()
	}

	go m.readLoop(stream, done)
	return nil
}

func (m *clientPluginBridgeManager) sendFrame(stream io.ReadWriteCloser, frame bridgeFrame) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if len(data) > remotePluginBridgeFrameLimit {
		return errors.New("bridge frame too large")
	}
	data = append(data, '\n')
	m.streamMu.Lock()
	defer m.streamMu.Unlock()
	_, err = stream.Write(data)
	return err
}

func (m *clientPluginBridgeManager) pushRegistration() error {
	m.mu.Lock()
	stream := m.stream
	specs := make([]bridgePluginSpec, 0, len(m.running))
	names := make([]string, 0, len(m.running))
	for name := range m.running {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ext := m.running[name]
		tools := ext.ModelTools()
		if len(tools) == 0 {
			continue
		}
		specs = append(specs, bridgePluginSpec{
			Name:        ext.Manifest.Name,
			DisplayName: ext.Manifest.DisplayName,
			Tools:       tools,
		})
	}
	m.mu.Unlock()
	if stream == nil {
		return errors.New("反向插件通道未连接")
	}
	return m.sendFrame(stream, bridgeFrame{Type: "register", Plugins: specs})
}

func (m *clientPluginBridgeManager) readLoop(stream io.ReadWriteCloser, done chan struct{}) {
	defer func() {
		_ = stream.Close()
		m.mu.Lock()
		if m.stream == stream {
			m.stream = nil
			m.streamReady = false
		}
		m.mu.Unlock()
		close(done)
	}()

	var cancelsMu sync.Mutex
	cancels := map[string]context.CancelFunc{}
	defer func() {
		cancelsMu.Lock()
		for _, fn := range cancels {
			fn()
		}
		cancelsMu.Unlock()
	}()

	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 8192), remotePluginBridgeFrameLimit)
	for scanner.Scan() {
		var frame bridgeFrame
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			diagnostics.Log("warn", "host-tools", "client bridge read error: "+err.Error())
			return
		}
		switch frame.Type {
		case "ping":
			if err := m.sendFrame(stream, bridgeFrame{Type: "pong"}); err != nil {
				return
			}
		case "registered":
			m.recordRegistered(frame.Accepted, frame.Skipped)
		case "cancel":
			cancelsMu.Lock()
			fn := cancels[frame.ID]
			delete(cancels, frame.ID)
			cancelsMu.Unlock()
			if fn != nil {
				fn()
			}
		case "call":
			callID := frame.ID
			toolName := frame.Tool
			args := frame.Arguments
			callCtx, callCancel := context.WithCancel(m.ctx)
			cancelsMu.Lock()
			cancels[callID] = callCancel
			cancelsMu.Unlock()
			go func() {
				defer func() {
					callCancel()
					cancelsMu.Lock()
					delete(cancels, callID)
					cancelsMu.Unlock()
				}()
				result, err := m.invokeLocalTool(callCtx, toolName, args)
				reply := bridgeFrame{Type: "result", ID: callID, Result: result}
				if err != nil {
					reply.Error = err.Error()
					reply.Result = nil
				}
				_ = m.sendFrame(stream, reply)
			}()
		}
	}
}

func (m *clientPluginBridgeManager) recordRegistered(accepted []hostbridge.ToolInfo, skipped []hostbridge.ProxySkipped) {
	m.mu.Lock()
	defer m.mu.Unlock()
	owner := map[string]string{}
	for name, ext := range m.running {
		for _, tool := range ext.ModelTools() {
			if _, exists := owner[tool.Name]; !exists {
				owner[tool.Name] = name
			}
		}
	}
	m.accepted = map[string][]hostbridge.ToolInfo{}
	m.skipped = map[string][]hostbridge.ProxySkipped{}
	for _, item := range accepted {
		p := owner[item.Name]
		m.accepted[p] = append(m.accepted[p], item)
	}
	for _, item := range skipped {
		p := owner[item.Name]
		m.skipped[p] = append(m.skipped[p], item)
	}
}

func (m *clientPluginBridgeManager) invokeLocalTool(ctx context.Context, toolName string, args json.RawMessage) (*mcp.CallToolResult, error) {
	m.mu.Lock()
	var target *hostbridge.External
	for _, ext := range m.running {
		if slices.Contains(ext.Tools(), toolName) {
			target = ext
			break
		}
	}
	m.mu.Unlock()
	if target == nil {
		return nil, errors.New("the requested tool is not available")
	}
	return target.CallForModel(ctx, toolName, args)
}

func (m *clientPluginBridgeManager) snapshot() clientBridgeStatus {
	supported := m.session != nil && m.session.Manifest.Capabilities["client_plugin_bridge"]
	m.mu.Lock()
	connected := m.stream != nil && m.streamReady
	streamErr := m.streamErr
	shared := make(map[string]bool, len(m.shared))
	for k, v := range m.shared {
		shared[k] = v
	}
	running := make(map[string]*hostbridge.External, len(m.running))
	for k, v := range m.running {
		running[k] = v
	}
	problems := make(map[string]string, len(m.problems))
	for k, v := range m.problems {
		problems[k] = v
	}
	accepted := make(map[string][]hostbridge.ToolInfo, len(m.accepted))
	for k, v := range m.accepted {
		accepted[k] = slices.Clone(v)
	}
	skipped := make(map[string][]hostbridge.ProxySkipped, len(m.skipped))
	for k, v := range m.skipped {
		skipped[k] = slices.Clone(v)
	}
	m.mu.Unlock()

	entries, _ := os.ReadDir(m.pluginsDir())
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	items := []clientBridgePluginItem{}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		dir := filepath.Join(m.pluginsDir(), entry.Name())
		manifest, err := hostbridge.LoadManifest(dir)
		if err != nil || manifest.Name != entry.Name() || manifest.Kind != hostbridge.KindHost {
			continue
		}
		if !manifest.Supported() || manifest.Unavailable(version.Version, true) != "" || len(manifest.ServerCommand()) == 0 {
			continue
		}
		item := clientBridgePluginItem{
			Name:        manifest.Name,
			DisplayName: manifest.DisplayName,
			Version:     manifest.Version,
			Description: manifest.Description,
			Permissions: manifest.Permissions,
			Shared:      shared[manifest.Name],
			Running:     running[manifest.Name] != nil && running[manifest.Name].Running(),
			Tools:       accepted[manifest.Name],
			Skipped:     skipped[manifest.Name],
			Problem:     problems[manifest.Name],
		}
		if len(item.Tools) == 0 && running[manifest.Name] != nil {
			item.Tools = running[manifest.Name].ToolList()
		}
		items = append(items, item)
	}
	return clientBridgeStatus{
		Supported: supported,
		Connected: connected,
		Plugins:   items,
		Error:     streamErr,
	}
}
