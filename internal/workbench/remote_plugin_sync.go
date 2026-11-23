package workbench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sylvinus/agent-vm/internal/containerenv"
	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/hostbridge"
	"github.com/sylvinus/agent-vm/internal/remotehost"
	"github.com/sylvinus/agent-vm/internal/version"
)

type remotePluginSyncEntry struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Kind    string `json:"kind"`
	Digest  string `json:"digest,omitempty"`
	Enabled bool   `json:"enabled"`
}

type remotePluginSyncManifest struct {
	Platform string                  `json:"platform"`
	Headless bool                    `json:"headless"`
	Version  string                  `json:"version"`
	Plugins  []remotePluginSyncEntry `json:"plugins"`
}

func (s *Server) hostIsHeadless() bool {
	if containerenv.Configured(s.profile) {
		return true
	}
	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return true
	}
	return false
}

// getHostPluginSync returns the installed plugin inventory and host platform
// capabilities so a connected desktop client can compute an incremental sync.
func (s *Server) getHostPluginSync(w http.ResponseWriter, r *http.Request) {
	if !s.hasPluginControlView(r) {
		http.Error(w, "请通过受信任的 Cage 桌面窗口同步插件", http.StatusForbidden)
		return
	}
	s.mu.Lock()
	off := hostPluginsOff(s.preferences)
	s.mu.Unlock()
	result := remotePluginSyncManifest{
		Platform: runtime.GOOS,
		Headless: s.hostIsHeadless(),
		Version:  version.Version,
		Plugins:  []remotePluginSyncEntry{},
	}
	dir := s.hostPluginDir()
	entries, _ := os.ReadDir(dir)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		pluginDir := filepath.Join(dir, entry.Name())
		manifest, err := hostbridge.LoadManifest(pluginDir)
		if err != nil || manifest.Name != entry.Name() {
			continue
		}
		digest, _ := hostbridge.PluginDigest(pluginDir, manifest.Name)
		result.Plugins = append(result.Plugins, remotePluginSyncEntry{
			Name:    manifest.Name,
			Version: manifest.Version,
			Kind:    manifest.Kind,
			Digest:  digest,
			Enabled: !slices.Contains(off, manifest.Name),
		})
	}
	jsonReply(w, result)
}

// syncHostPlugin receives a validated .tar.gz plugin archive from an
// SSH-authenticated desktop client and installs or upgrades it without
// modifying grants, settings or secrets.
func (s *Server) syncHostPlugin(w http.ResponseWriter, r *http.Request) {
	if !s.hasPluginControlView(r) {
		http.Error(w, "请通过受信任的 Cage 桌面窗口同步插件", http.StatusForbidden)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if !hostbridge.ValidPluginName(name) {
		http.Error(w, "插件名称无效", http.StatusBadRequest)
		return
	}
	// A bundled plugin uninstalled on this computer stays uninstalled until
	// it is restored here; a connecting client does not put it back.
	if s.defaultPluginDismissed(name) {
		http.Error(w, "此插件已在远程主机上卸载，可在远程主机的插件页恢复", http.StatusUnprocessableEntity)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, hostbridge.PluginArchiveLimit+1))
	if err != nil {
		requestError(w, errors.New("无法读取插件压缩包"))
		return
	}
	if len(data) == 0 || int64(len(data)) > hostbridge.PluginArchiveLimit {
		http.Error(w, "插件压缩包为空或超出大小限制", http.StatusRequestEntityTooLarge)
		return
	}
	if !s.op.TryLock() {
		http.Error(w, "环境正在执行其他操作，请稍后再试", http.StatusConflict)
		return
	}
	defer s.op.Unlock()

	s.mu.Lock()
	ready, closing := s.ready, s.closing
	s.mu.Unlock()
	if closing {
		http.Error(w, "服务正在关闭", http.StatusServiceUnavailable)
		return
	}

	// Stage and validate the incoming archive completely before stopping tools
	// or moving any existing version aside.
	if err := os.MkdirAll(s.hostPluginDir(), 0o700); err != nil {
		requestError(w, err)
		return
	}
	stageParent, err := os.MkdirTemp(s.hostPluginDir(), ".sync-stage-*")
	if err != nil {
		requestError(w, err)
		return
	}
	defer os.RemoveAll(stageParent)

	manifest, err := hostbridge.InstallArchive(data, name, stageParent, version.Version)
	if err != nil {
		http.Error(w, "插件包校验失败："+err.Error(), http.StatusBadRequest)
		return
	}
	if s.hostIsHeadless() && hostbridge.RequiresGUI(manifest) {
		http.Error(w, "远程主机无图形桌面，不支持需要屏幕或键鼠权限的主机插件", http.StatusConflict)
		return
	}

	target := filepath.Join(s.hostPluginDir(), name)
	aside := ""
	s.closeHostTools()
	if _, statErr := os.Lstat(target); statErr == nil {
		if aside, err = s.setAsideHostPlugin(name); err != nil {
			requestError(w, err)
			return
		}
	}
	if err := os.Rename(filepath.Join(stageParent, name), target); err != nil {
		if aside != "" {
			_ = os.Rename(aside, target)
		}
		requestError(w, err)
		return
	}
	if ready {
		s.ensureHostTools()
	}
	digest, _ := hostbridge.PluginDigest(target, name)
	jsonReply(w, remotePluginSyncEntry{
		Name:    manifest.Name,
		Version: manifest.Version,
		Kind:    manifest.Kind,
		Digest:  digest,
		Enabled: true,
	})
}

type clientPluginSyncItem struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

type clientPluginSyncStatus struct {
	State      string                 `json:"state"` // idle, syncing, done, locked, unsupported, error
	Synced     []clientPluginSyncItem `json:"synced"`
	Skipped    []clientPluginSyncItem `json:"skipped"`
	Failed     []clientPluginSyncItem `json:"failed"`
	LastSyncAt string                 `json:"last_sync_at,omitempty"`
	Error      string                 `json:"error,omitempty"`
}

type clientPluginSyncRunner struct {
	session    *remotehost.Session
	pluginsDir string
	trigger    chan struct{}
	cancel     context.CancelFunc
	done       chan struct{}

	mu     sync.Mutex
	status clientPluginSyncStatus
	pushed map[string]string
}

func newClientPluginSyncRunner(session *remotehost.Session, pluginsDir string) *clientPluginSyncRunner {
	ctx, cancel := context.WithCancel(session.Context)
	r := &clientPluginSyncRunner{
		session:    session,
		pluginsDir: pluginsDir,
		trigger:    make(chan struct{}, 1),
		cancel:     cancel,
		done:       make(chan struct{}),
		status: clientPluginSyncStatus{
			State:   "idle",
			Synced:  []clientPluginSyncItem{},
			Skipped: []clientPluginSyncItem{},
			Failed:  []clientPluginSyncItem{},
		},
		pushed: map[string]string{},
	}
	if !session.Manifest.Capabilities["remote_plugins"] {
		r.status.State = "unsupported"
		close(r.done)
		return r
	}
	go r.loop(ctx)
	return r
}

func (r *clientPluginSyncRunner) close() {
	r.cancel()
	<-r.done
}

func (r *clientPluginSyncRunner) snapshot() clientPluginSyncStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.status
	out.Synced = append([]clientPluginSyncItem{}, r.status.Synced...)
	out.Skipped = append([]clientPluginSyncItem{}, r.status.Skipped...)
	out.Failed = append([]clientPluginSyncItem{}, r.status.Failed...)
	return out
}

func (r *clientPluginSyncRunner) requestSync() {
	select {
	case r.trigger <- struct{}{}:
	default:
	}
}

func (r *clientPluginSyncRunner) loop(ctx context.Context) {
	defer close(r.done)
	// Run immediately once the remote session is established, retrying while
	// the remote account is locked, then re-sync whenever explicitly requested.
	retry := time.NewTimer(0)
	defer retry.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.trigger:
			if !retry.Stop() {
				select {
				case <-retry.C:
				default:
				}
			}
		case <-retry.C:
		}
		if ctx.Err() != nil {
			return
		}
		locked, busy := r.runOnce(ctx)
		if locked {
			retry.Reset(2500 * time.Millisecond)
		} else if busy {
			retry.Reset(1500 * time.Millisecond)
		}
	}
}

func (r *clientPluginSyncRunner) runOnce(ctx context.Context) (locked bool, busy bool) {
	r.mu.Lock()
	r.status.State = "syncing"
	r.status.Error = ""
	r.mu.Unlock()

	remoteManifest, statusCode, err := fetchRemoteSyncManifest(ctx, r.session)
	if err != nil {
		r.mu.Lock()
		switch statusCode {
		case http.StatusLocked:
			r.status.State = "locked"
			r.status.Error = ""
			locked = true
		case http.StatusNotFound:
			r.status.State = "unsupported"
			r.status.Error = ""
		default:
			r.status.State = "error"
			r.status.Error = err.Error()
		}
		r.mu.Unlock()
		return locked, false
	}

	remoteByName := map[string]remotePluginSyncEntry{}
	for _, entry := range remoteManifest.Plugins {
		remoteByName[entry.Name] = entry
	}

	entries, err := os.ReadDir(r.pluginsDir)
	if err != nil && !os.IsNotExist(err) {
		r.mu.Lock()
		r.status.State = "error"
		r.status.Error = "无法读取本机插件目录"
		r.mu.Unlock()
		return false, false
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	synced := []clientPluginSyncItem{}
	skipped := []clientPluginSyncItem{}
	failed := []clientPluginSyncItem{}

	for _, entry := range entries {
		if ctx.Err() != nil {
			return false, false
		}
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		name := entry.Name()
		pluginDir := filepath.Join(r.pluginsDir, name)
		manifest, err := hostbridge.LoadManifest(pluginDir)
		if err != nil || manifest.Name != name {
			continue
		}
		if !hostbridge.SupportsPlatform(manifest, remoteManifest.Platform) {
			skipped = append(skipped, clientPluginSyncItem{Name: name, Version: manifest.Version, Reason: "不支持远程主机系统（" + remoteManifest.Platform + "）"})
			continue
		}
		if remoteManifest.Headless && hostbridge.RequiresGUI(manifest) {
			skipped = append(skipped, clientPluginSyncItem{Name: name, Version: manifest.Version, Reason: "远程主机无图形桌面，可改用本机反向共享"})
			continue
		}
		if unavailable := manifest.Unavailable(remoteManifest.Version, true); unavailable != "" {
			skipped = append(skipped, clientPluginSyncItem{Name: name, Version: manifest.Version, Reason: "远程 Cage 版本暂不兼容此插件"})
			continue
		}

		archive, digest, err := hostbridge.ArchivePlugin(pluginDir, name)
		if err != nil {
			failed = append(failed, clientPluginSyncItem{Name: name, Version: manifest.Version, Reason: err.Error()})
			continue
		}
		if existing, exists := remoteByName[name]; exists {
			if existing.Digest != "" && existing.Digest == digest {
				continue
			}
			if cmp, ok := hostbridge.CompareVersions(manifest.Version, existing.Version); ok && cmp < 0 {
				skipped = append(skipped, clientPluginSyncItem{Name: name, Version: manifest.Version, Reason: fmt.Sprintf("远程版本（%s）高于本机", existing.Version)})
				continue
			}
		}

		code, err := uploadPluginArchive(ctx, r.session, name, archive)
		if err != nil {
			if code == http.StatusConflict {
				busy = true
			}
			failed = append(failed, clientPluginSyncItem{Name: name, Version: manifest.Version, Reason: err.Error()})
			continue
		}
		r.mu.Lock()
		r.pushed[name] = digest
		r.mu.Unlock()
		synced = append(synced, clientPluginSyncItem{Name: name, Version: manifest.Version})
		diagnostics.Log("info", "host-tools", fmt.Sprintf("synced plugin %s@%s to remote", name, manifest.Version))
	}

	r.mu.Lock()
	r.status = clientPluginSyncStatus{
		State:      "done",
		Synced:     synced,
		Skipped:    skipped,
		Failed:     failed,
		LastSyncAt: time.Now().UTC().Format(time.RFC3339),
	}
	if len(failed) > 0 && len(synced) == 0 && busy {
		r.status.State = "syncing"
	}
	r.mu.Unlock()
	return false, busy
}

func fetchRemoteSyncManifest(ctx context.Context, session *remotehost.Session) (remotePluginSyncManifest, int, error) {
	var manifest remotePluginSyncManifest
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, session.Origin+"/api/host-tools/sync", nil)
	if err != nil {
		return manifest, 0, err
	}
	res, err := session.Client.Do(req)
	if err != nil {
		return manifest, 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return manifest, res.StatusCode, errors.New(strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&manifest); err != nil {
		return manifest, res.StatusCode, err
	}
	return manifest, res.StatusCode, nil
}

func uploadPluginArchive(ctx context.Context, session *remotehost.Session, name string, archive []byte) (int, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	endpoint := session.Origin + "/api/host-tools/sync?name=" + url.QueryEscape(name)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(archive))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/gzip")
	res, err := session.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		msg := strings.TrimSpace(string(body))
		if msg == "" {
			msg = fmt.Sprintf("同步失败（HTTP %d）", res.StatusCode)
		}
		return res.StatusCode, errors.New(msg)
	}
	return res.StatusCode, nil
}
