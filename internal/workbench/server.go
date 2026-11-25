package workbench

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/guestos"
	"github.com/sylvinus/agent-vm/internal/host"
	"github.com/sylvinus/agent-vm/internal/hostimage"
	"github.com/sylvinus/agent-vm/internal/managedproxy"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

//go:embed web/*
var assets embed.FS

const cookieName = "cage_session"

type controlFile struct {
	Port       int                `json:"port"`
	Token      string             `json:"token"`
	PID        int                `json:"pid"`
	Controller ControllerIdentity `json:"controller"`
}
type Server struct {
	shellLoginWatch                                  shellLoginWatchState
	desktopCatalogRevision                           atomic.Uint64
	desktopLoginWatch                                desktopWatchState
	desktopAction                                    func(context.Context, string, claudeAccount, string) error
	desktopViews                                     displayViewDemand
	desktopDisplayUpdate                             func(bool) error
	browserViews                                     displayViewDemand
	desktopStatus                                    desktopStatusCache
	agentNativeQueue                                 agentNativeQueue
	clipboardFiles                                   map[string]clipboardFileSelection
	clipboardRelease                                 func([]string)
	clipboardCancel                                  context.CancelFunc
	clipboardDone                                    chan struct{}
	clipboardDesktop                                 string
	clipboardGuestSession                            string
	phones                                           mobilePairing
	instanceID                                       string
	clientOnly                                       bool
	desktopEntry                                     bool
	appUpdateConfigPath                              string
	windowOwners                                     map[int]bool
	remote                                           *remoteGateway
	account                                          *localAccount
	identity                                         ControllerIdentity
	guestManifest                                    *cage.GuestManifest
	guestUpdateRequired                              bool
	guestUpdateError                                 string
	storageRecoveryError                             string
	closing                                          bool
	shutdown                                         func()
	claudeAccounts                                   claudeAccountManager
	tokenUsage                                       tokenUsageManager
	accountNetworks                                  accountNetworks
	favoritesMu                                      sync.Mutex
	diagnosticMu                                     sync.Mutex
	diagnosticRateMu                                 sync.Mutex
	diagnosticWindow                                 time.Time
	diagnosticCount                                  int
	tickets                                          map[string]time.Time
	remoteDesktopTickets                             map[string]bool
	browserControl                                   *browserClient
	controlSession                                   guestSession
	op                                               sync.Mutex
	profile, configPath, exe, origin, token, session string
	mu                                               sync.Mutex
	preferences                                      Preferences
	jobs                                             jobManager
	desktopOnboardingUntil                           time.Time
	desktopOnboardingReleased                        bool
	ready                                            bool
	location                                         *Location
	exitProbeError                                   string
	locationError                                    string
	networkIntentError                               string
	credentials                                      guestCredentials
	desktopFileOwners                                map[string]string
	terminals                                        terminalManager
	notifications                                    notificationHub
	mobileCLI                                        mobileCLIStore
	conversationMu                                   sync.Mutex
	conversationDiscovered                           conversationDiscovery       // guarded by conversationMu
	conversationViews                                map[string]conversationView // guarded by conversationMu
	conversationGeneration                           atomic.Uint64
	proxyStore                                       managedproxy.StoreReader // read-only monitor parses
	monitorTimingMu                                  sync.Mutex
	monitorTimingLogs                                map[string]monitorTimingLog
	mobileConnectMu                                  sync.Mutex
	audioClients                                     int
	audioInputClients                                int
	projectShares                                    map[string]*projectShare
	hostTools                                        *hostTools
	hostToolsRemoved                                 bool
	hostMarket                                       hostMarketCache
	hostGrants                                       *hostGrants
	hostGrantsOnce                                   sync.Once
	remoteApprovals                                  remoteApprovalHub
	clientBridge                                     serverClientPluginBridge
	// pluginSettingsMu serializes reading and writing plugin-settings.json.
	pluginSettingsMu sync.Mutex
	// defaultPluginsMu serializes reading and writing default-plugins.json.
	defaultPluginsMu sync.Mutex
	// draftStoreOverride stands in for the guest extension-submissions folder in tests.
	draftStoreOverride string
	// tailnetTool stands in for the running Tailscale plugin in tests.
	tailnetTool             func(context.Context, string, json.RawMessage) (*mcp.CallToolResult, error)
	hostViewMu              sync.Mutex
	hostViewKeyData         []byte
	projectErrors           []projectShareError
	clientMounts            map[string]*guestClientMount
	clientWorkspace         string
	safety                  safetyState
	recovery                networkRecovery
	locations               map[string]Location
	monitorCancel           context.CancelFunc
	networkMonitorDone      chan struct{}
	projectOperationWaiters int
	monitorSafetyOp         sync.Mutex
	safetyOp                sync.Mutex
	networkPaused           bool
	proxyNetworkLost        bool
	proxyNetworkRetry       time.Time
	checkCancel             context.CancelFunc
	lifeContext             context.Context
	lifeCancel              context.CancelFunc
	monitors                sync.WaitGroup
	activities              sync.WaitGroup
	closeOnce               sync.Once
	closeErr                error
	deviceReset             deviceResetHooks
	abandonedVMState        func(context.Context, string) (bool, error)
	controlPublished        bool

	// guestUpdateGeneration changes whenever an attach, update or stop
	// decides guestUpdateRequired, so a slower version query cannot
	// overwrite a newer decision.
	guestUpdateGeneration uint64

	// projectPluginRestarts runs one project switch's plugin restarts at a time.
	projectPluginRestarts sync.Mutex
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func equalSecret(a, b string) bool {
	return len(a) > 0 && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func Serve(configPath string) error { return serve(configPath, false, "") }

func ServeClient(configPath string) error { return serve(configPath, true, "") }

// ServeDesktop is the desktop window's connection gateway. Its control lock and
// logs live separately from the workload it may later attach to.
func ServeDesktop(fullConfigPath string) error {
	return serve(desktopConfigPath(fullConfigPath), false, fullConfigPath)
}

func serve(configPath string, clientOnly bool, fullConfigPath string) (returned error) {
	store := diagnostics.New(filepath.Dir(configPath))
	diagnostics.SetDefault(store)
	if err := store.Clean(); err != nil {
		return err
	}

	diagnostics.Log("info", "controller", "service starting")
	defer func() { diagnostics.Log("info", "controller", "service stopped") }()
	profile := filepath.Dir(configPath)
	if err := host.EnsurePrivateDir(profile); err != nil {
		return err
	}
	lock, err := openControllerLock(profile, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = lockControllerForStart(lock); err != nil {
		if controllerLockContended(err) {
			return errors.New("此配置已有工作台服务正在运行")
		}
		return fmt.Errorf("无法锁定工作台控制文件: %w", err)
	}
	if err := host.ProtectFile(lock.Name()); err != nil {
		return err
	}
	if err := store.ImportLegacy(filepath.Dir(configPath)); err != nil {
		diagnostics.Log("warn", "diagnostics", "legacy log import failed: "+err.Error())
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	listener, port, err := listenController(profile)
	if err != nil {
		diagnostics.Log("error", "controller", err.Error())
		return err
	}
	defer listener.Close()
	s := &Server{profile: profile, configPath: configPath, exe: exe, clientOnly: clientOnly, origin: fmt.Sprintf("http://localhost:%d", port), token: randomToken(), session: randomToken(), preferences: Preferences{Keyboard: defaultKeyboard(), AutoTimezone: true, AutoLanguage: true, BalancedDisplay: true, ProjectFileCache: true}}
	s.desktopEntry = fullConfigPath != ""
	s.appUpdateConfigPath = configPath
	if fullConfigPath != "" {
		s.appUpdateConfigPath = fullConfigPath
	}
	workload := !s.desktopEntry && !clientOnly
	if s.desktopEntry {
		if err = importDesktopConnections(filepath.Dir(fullConfigPath), profile); err != nil {
			return err
		}
		s.remote = newDesktopGateway(profile, NewDesktopLocalController(fullConfigPath, exe))
	} else {
		s.remote = newRemoteGateway(profile)
		s.remote.clientOnly = clientOnly
	}
	defer s.remote.close()
	if workload {
		if err = cage.RecoverStorage(context.Background(), profile, nil); err != nil {
			s.failStorageRecovery(err)
		}
		s.instanceID, err = ensureRemoteInstance(profile)
		if err != nil {
			return err
		}
		s.account, err = loadLocalAccount(profile)
		if err != nil {
			return err
		}
		defer s.account.lock()
		defer func() { returned = errors.Join(returned, s.closeService()) }()
		if b, e := os.ReadFile(filepath.Join(profile, "workbench.json")); e == nil {
			// Existing profiles without this field used manual language settings.
			s.preferences.AutoLanguage = false
			// Preserve the original density of profiles saved before this setting.
			s.preferences.BalancedDisplay = false
			_ = json.Unmarshal(b, &s.preferences)
		}
		if !s.storageRecoveryStatus().Required {
			if err = s.prepareEnvironment(); err != nil {
				return err
			}
		}
		// Invalid intent keeps the gate paused and its error visible in status.
		s.restoreNetworkIntent()
		if s.preferences.Keyboard == "" {
			s.preferences.Keyboard = defaultKeyboard()
		}
		if _, e := os.Stat(configPath); os.IsNotExist(e) {
			if err = writePrivate(configPath, defaultConfig()); err != nil {
				return err
			}
		}
		s.ensureDefaultPlugins()
		if e := s.retireLocalProxyCore(); e != nil {
			// Revoke the gate even if the old process cannot be stopped.
			// Keep the workbench available for importing its replacement.
			s.safety.monitorError = "旧代理核心尚未停止，请迁移到远程节点后重新应用；外网保持关闭"
			diagnostics.Log("error", "proxy", "停止旧代理核心失败："+e.Error())
		}
		// A previously running VM cannot keep an unmanaged endpoint online.
		if cfg, e := loadConfig(configPath); e != nil || cfg.RequireManagedProxy(profile) != nil {
			_ = s.closeProxyGate("尚未配置有效节点，外网保持关闭")
		}
		if s.networkPaused {
			_ = s.closeProxyGate("网络保持暂停，请手动恢复后再联网")
		}
		s.startMonitors()
		if err = s.restorePhone(); err != nil {
			s.phones.status = "无法恢复手机配对，请移除旧记录后重试"
		}
		defer s.stopPhone()
	}
	s.identity, err = currentController()
	if err != nil {
		return err
	}
	if err = writePrivate(filepath.Join(profile, "control.json"), controlFile{Port: port, Token: s.token, PID: os.Getpid(), Controller: s.identity}); err != nil {
		return err
	}
	s.controlPublished = true
	if !workload {
		defer os.Remove(filepath.Join(profile, "control.json"))
	}
	serverErrors := store.Stream("http", "error")
	defer serverErrors.Close()
	server := &http.Server{ErrorLog: log.New(serverErrors, "", 0), Handler: s.handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	defer server.Close()
	shutdownDone := make(chan struct{})
	var shutdownOnce sync.Once
	s.shutdown = func() {
		shutdownOnce.Do(func() {
			defer close(shutdownDone)
			s.beginClose()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = server.Shutdown(ctx)
			_ = server.Close()
		})
	}
	signals, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	serveDone := make(chan struct{})
	defer close(serveDone)
	if s.desktopEntry {
		go s.watchDesktopOwners(serveDone)
	}
	go func() {
		select {
		case <-signals.Done():
			s.shutdown()
		case <-serveDone:
		}
	}()
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		<-shutdownDone
		return nil
	}
	return err
}
func (s *Server) handler() http.Handler {
	mux := newDiscoverableMux()
	mux.HandleFunc("GET /api/agent/routes", func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, mux.routes())
	})
	s.agentRoutes(mux)
	s.agentNativeRoutes(mux)
	s.mobileCLIRoutes(mux)
	s.mobileSurfaceRoutes(mux)
	mux.HandleFunc("GET /api/mobile-pair", s.phoneStatus)
	mux.HandleFunc("POST /api/mobile-pair", s.pairPhone)
	mux.HandleFunc("DELETE /api/mobile-pair", s.removePhone)
	mux.HandleFunc("POST /api/mobile/device", s.reportPhoneDevice)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { jsonReply(w, map[string]bool{"ready": true}) })
	mux.HandleFunc("POST /api/launch", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Token         string `json:"token"`
			RemoteDesktop bool   `json:"remote_desktop,omitempty"`
		}
		if !readJSON(w, r, &input) {
			return
		}
		if !equalSecret(input.Token, s.token) {
			http.Error(w, "Launcher refused", http.StatusUnauthorized)
			return
		}
		ticket := s.issueTicketWithScope(input.RemoteDesktop)
		jsonReply(w, launchResponse{ConfigPath: s.configPath, ClientOnly: s.clientOnly, DesktopGateway: s.desktopEntry, SessionID: desktopSessionID(), URL: s.origin + "/?launch=" + randomToken()[:8] + "#" + ticket, Controller: s.identity})
	})
	if s.desktopEntry || s.clientOnly {
		mux.HandleFunc("POST /api/handoff", s.handoffEntry)
	} else {
		mux.HandleFunc("POST /api/handoff", s.handoff)
		mux.HandleFunc("POST /api/desktop-local/trusted-login", s.localTrustedLogin)
		mux.HandleFunc("POST /api/desktop-local/{action}", s.desktopLocalControl)
	}
	if s.desktopEntry {
		mux.HandleFunc("POST /api/desktop-owner", s.registerDesktopOwner)
	}
	mux.HandleFunc("POST /api/session", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Token string `json:"token"`
		}
		if !readJSON(w, r, &v) {
			return
		}
		ok, remoteDesktop := s.consumeTicketWithScope(v.Token)
		if !ok {
			http.Error(w, "访问凭证无效，请从 Cage 应用重新打开。", http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: s.cookieName(), Value: s.session, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 86400 * 30})
		if remoteDesktop {
			http.SetCookie(w, &http.Cookie{Name: s.cookieName() + "-remote-desktop", Value: remoteDesktopProof(s.token), Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 86400 * 30})
		}
		jsonReply(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/environment/initialization", s.initialization)
	mux.HandleFunc("POST /api/environment/initialization", s.initialization)
	mux.HandleFunc("GET /api/remote/manifest", s.remoteManifest)
	mux.HandleFunc("GET /api/projects/directories", s.projectDirectories)
	mux.HandleFunc("POST /api/input", s.remoteInput)
	mux.HandleFunc("GET /api/diagnostics", s.diagnosticStatus)
	mux.HandleFunc("POST /api/diagnostics/events", s.diagnosticEvent)
	mux.HandleFunc("GET /api/diagnostics/export", s.diagnosticExport)
	mux.HandleFunc("GET /api/claude-accounts", s.listClaudeAccounts)
	mux.HandleFunc("POST /api/claude-accounts", s.mutateClaudeAccounts)
	mux.HandleFunc("GET /api/token-usage", s.getTokenUsage)
	mux.HandleFunc("POST /api/token-usage", s.mutateTokenUsage)
	mux.HandleFunc("GET /api/vault", s.vaultList)
	mux.HandleFunc("POST /api/vault", s.vaultMutation)
	mux.HandleFunc("POST /api/vault/browser", s.vaultBrowser)
	mux.HandleFunc("GET /api/account", s.accountStatus)
	mux.HandleFunc("POST /api/account/{action}", s.accountAction)
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("GET /api/host-tools", s.getHostTools)
	mux.HandleFunc("POST /api/host-tools", s.setHostTools)
	mux.HandleFunc("POST /api/host-tools/call", s.callHostTool)
	mux.HandleFunc("GET /api/host-tools/market", s.getHostMarket)
	mux.HandleFunc("GET /api/host-tools/drafts", s.getHostPluginDrafts)
	mux.HandleFunc("GET /api/host-tools/drafts/file", s.getHostPluginDraftFile)
	mux.HandleFunc("POST /api/host-tools/drafts/install", s.installHostPluginDraft)
	mux.HandleFunc("POST /api/host-tools/drafts/discard", s.discardHostPluginDraft)
	mux.HandleFunc("GET /api/host-tools/sync", s.getHostPluginSync)
	mux.HandleFunc("POST /api/host-tools/sync", s.syncHostPlugin)
	mux.HandleFunc("POST /api/host-tools/approvals/channel", s.remoteApprovalChannel)
	mux.HandleFunc("POST /api/host-tools/client-bridge", s.remoteClientPluginBridge)
	mux.HandleFunc("GET "+hostPluginIconPath, s.hostPluginIcon)
	mux.HandleFunc("GET /api/client/about", s.appAbout)
	mux.HandleFunc("POST /api/client/about", s.appAbout)
	mux.HandleFunc("GET /api/client/language", s.interfaceLanguage)
	mux.HandleFunc("POST /api/client/language", s.interfaceLanguage)
	mux.HandleFunc("GET /api/client/notifications", s.notificationPreferences)
	mux.HandleFunc("POST /api/client/notifications", s.notificationPreferences)
	mux.HandleFunc("GET /api/notifications/events", s.notificationEvents)
	mux.HandleFunc("POST /api/projects/open", s.openProject)
	mux.HandleFunc("POST /api/projects/save", s.saveProject)
	mux.HandleFunc("POST /api/projects/remove", s.removeProject)
	mux.HandleFunc("POST /api/projects/refresh", s.refreshProjectFiles)
	mux.HandleFunc("POST /api/projects/client-share", s.guestClientShare)
	mux.HandleFunc("GET /api/projects/client-share", s.guestClientShare)
	mux.HandleFunc("GET /api/environment/versions", s.environmentVersions)
	mux.HandleFunc("POST /api/environment/device-check", s.deviceCheck)
	mux.HandleFunc("GET /api/environment/guest-profile", s.guestProfileOptions)
	mux.HandleFunc("POST /api/environment/guest-profile/preview", s.guestProfileOptions)
	s.registerCacheMaintenance(mux)
	s.registerDeviceReset(mux)
	mux.HandleFunc("GET /api/environment/guest-systems", s.guestInstallation)
	mux.HandleFunc("DELETE /api/environment/guest-systems", s.guestInstallation)
	mux.HandleFunc("GET /api/environment/storage", s.environmentStorage)
	mux.HandleFunc("POST /api/environment/storage", s.environmentStorage)
	mux.HandleFunc("POST /api/environment/storage/recover", s.recoverEnvironmentStorage)
	mux.HandleFunc("GET /api/environment/resources", s.resourceStatus)
	mux.HandleFunc("POST /api/environment/resources", s.applyResources)
	mux.HandleFunc("POST /api/environment/update", s.updateEnvironment)
	mux.HandleFunc("POST /api/environment/tools", s.updateEnvironmentTools)
	mux.HandleFunc("GET /api/host/debian-mirrors", s.hostDebianMirrors)
	mux.HandleFunc("POST /api/host/debian-mirrors", s.chooseDebianMirror)
	mux.HandleFunc("GET /api/appearance", s.getAppearance)
	mux.HandleFunc("GET /api/favorites", s.serveFavorites)
	mux.HandleFunc("POST /api/favorites", s.serveFavorites)
	mux.HandleFunc("POST /api/appearance", s.saveAppearance)
	mux.HandleFunc("GET /api/appearance/codex", s.codexAppearance)
	mux.HandleFunc("POST /api/settings/probe", s.probe)
	mux.HandleFunc("POST /api/settings/dns-check", s.checkDNS)
	mux.HandleFunc("GET /api/proxy", s.proxyStatus)
	mux.HandleFunc("GET /api/proxy/local-configs", s.proxyLocalConfigs)
	mux.HandleFunc("POST /api/proxy/delay", s.proxyDelay)
	mux.HandleFunc("GET /api/proxy/connections", s.proxyConnections)
	mux.HandleFunc("GET /api/proxy/logs", s.proxyLogs)
	mux.HandleFunc("POST /api/proxy/{action}", s.proxyMutation)
	mux.HandleFunc("POST /api/settings", s.saveSettings)
	mux.HandleFunc("POST /api/restore", s.restore)
	mux.HandleFunc("POST /api/start", s.start)
	mux.HandleFunc("POST /api/stop", s.stop)
	mux.HandleFunc("POST /api/safety/check", s.safetyCheck)
	mux.HandleFunc("POST /api/safety/close", s.pauseNetwork)
	mux.HandleFunc("GET /api/safety/report", s.safetyExport)
	mux.HandleFunc("GET /api/tailscale", s.tailnet)
	mux.HandleFunc("POST /api/tailscale/{action}", s.tailnet)
	mux.HandleFunc("GET /api/browser/tabs", s.browser)
	mux.HandleFunc("GET /api/browser/events", s.browserEvents)
	mux.HandleFunc("POST /api/browser/clipboard", s.browserClipboard)
	mux.HandleFunc("GET /api/browser/preferences", s.browserPreferences)
	mux.HandleFunc("POST /api/browser/preferences", s.browserPreferences)
	mux.HandleFunc("POST /api/browser/extensions/upload", s.browserExtensionUpload)
	mux.HandleFunc("GET /api/client/browser-extensions", s.localBrowserExtensions)
	mux.HandleFunc("POST /api/client/browser-extensions", s.localBrowserExtensions)
	mux.HandleFunc("POST /api/browser/{action}", s.browser)
	mux.HandleFunc("POST /api/input/image", s.imageInput)
	mux.HandleFunc("POST /api/input/text", s.textInput)
	mux.HandleFunc("POST /api/input/paste/start", s.pasteInputStart)
	mux.HandleFunc("POST /api/input/paste/cancel", s.pasteInputCancel)
	mux.HandleFunc("POST /api/input/files/{action}", s.fileInputAction)
	mux.HandleFunc("POST /api/input/files/{target}/{id}", s.fileInputUpload)
	mux.HandleFunc("GET /api/input/files/desktop/requests", s.desktopFileRequests)
	mux.HandleFunc("POST /api/input/files/desktop/open", s.desktopFileOpenAction)
	mux.HandleFunc("POST /api/browser/files/uploads/{id}", s.browserUpload)
	mux.HandleFunc("GET /api/browser/files/downloads/{id}", s.browserDownload)
	mux.HandleFunc("GET /api/browser/files/downloads/{id}/{action}", s.browserDownload)
	mux.HandleFunc("GET /api/browser/files/saved", s.savedBrowserDownloads)
	mux.HandleFunc("GET /api/vnc-credentials", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.ready {
			http.Error(w, "环境尚未就绪", 409)
			return
		}
		// The guest scrolls its page by the viewer's distance, outside the
		// display stream.
		jsonReply(w, map[string]any{"password": s.credentials.VNCPassword, "display_scale": s.credentials.DisplayScale, "dynamic_scale": s.credentials.DynamicScale, "pixel_wheel": true})
	})
	mux.HandleFunc("GET /api/vnc", s.vnc)
	mux.HandleFunc("GET /api/desktop-instances", s.desktopCatalog)
	mux.HandleFunc("POST /api/desktop-instances", s.newAccountDesktop)
	mux.HandleFunc("DELETE /api/desktop-instances/{id}", s.deleteAccountDesktop)
	mux.HandleFunc("POST /api/desktop-instances/{id}/metadata", s.updateAccountDesktopMetadata)
	mux.HandleFunc("GET /api/desktop-instances/{id}/{action}", s.accountDesktop)
	mux.HandleFunc("POST /api/desktop-instances/{id}/{action}", s.accountDesktop)
	mux.HandleFunc("GET /api/desktop-apps/{plugin}/{app}/{action}", s.desktopApp)
	mux.HandleFunc("POST /api/desktop-apps/{plugin}/{app}/{action}", s.desktopApp)
	mux.HandleFunc("GET /api/claude-desktop", s.claudeDesktopStatus)
	mux.HandleFunc("GET /api/claude-desktop/vnc-credentials", s.claudeDesktopCredentials)
	mux.HandleFunc("GET /api/claude-desktop/vnc", s.claudeDesktopVNC)
	mux.HandleFunc("POST /api/claude-desktop/resize", s.claudeDesktopResize)
	mux.HandleFunc("POST /api/claude-desktop/{action}", s.claudeDesktopAction)
	mux.HandleFunc("GET /api/terminals", s.listTerminals)
	mux.HandleFunc("GET /api/audio", s.audio)
	mux.HandleFunc("GET /api/audio/input", s.audioInput)
	mux.HandleFunc("GET /api/clipboard", s.sharedClipboard)
	mux.HandleFunc("POST /api/clipboard/files", s.clipboardArchive)
	mux.HandleFunc("GET /api/clipboard/files/{id}", s.clipboardArchive)
	mux.HandleFunc("DELETE /api/clipboard/files/{id}", s.clipboardArchive)
	mux.HandleFunc("POST /api/terminals", s.newTerminal)
	mux.HandleFunc("DELETE /api/terminals/{id}", s.closeTerminal)
	mux.HandleFunc("POST /api/terminals/{id}/login-link", s.terminalLoginLink)
	mux.HandleFunc("POST /api/terminals/{id}/metadata", s.terminalSessionMetadata)
	mux.HandleFunc("GET /api/terminals/{id}/stream", s.terminalStream)
	web, _ := fs.Sub(assets, "web")
	mux.Handle("GET /", http.FileServer(http.FS(web)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin, err := url.Parse(s.origin)
		if err != nil || r.Host != origin.Host {
			http.Error(w, "Host refused", 403)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self' ws://"+origin.Host+"; img-src 'self' data: blob:; media-src 'self' blob:; font-src 'self'; object-src 'none'; frame-src "+s.origin+hostViewPrefix+"; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		if (r.Method != "GET" && r.Method != "HEAD") || strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			if r.Header.Get("Origin") != s.origin {
				http.Error(w, "Origin refused", 403)
				return
			}
		}
		// A plugin's page belongs to the currently selected workload (local or
		// sandbox-proxied from the active remote host), and sets its own headers.
		if strings.HasPrefix(r.URL.Path, hostViewPrefix) {
			if s.routeRemotePluginView(w, r) {
				return
			}
			s.servePluginView(w, r)
			return
		}
		// Launcher-token routes are handled only by this listener, never by a
		// selected workload. They perform their own authentication.
		if strings.HasPrefix(r.URL.Path, "/api/desktop-local/") || r.URL.Path == "/api/desktop-owner" {
			if (strings.HasPrefix(r.URL.Path, "/api/desktop-local/") && (s.desktopEntry || s.clientOnly)) || (r.URL.Path == "/api/desktop-owner" && !s.desktopEntry) {
				http.NotFound(w, r)
				return
			}
			mux.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/remote/manifest" && (s.desktopEntry || s.clientOnly) {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/api/session" && r.URL.Path != "/api/launch" && r.URL.Path != "/api/handoff" {
			c, e := r.Cookie(s.cookieName())
			if e != nil || !equalSecret(c.Value, s.session) {
				http.Error(w, "请从 Cage 应用重新打开工作台", 401)
				return
			}
		}
		// Shell preferences and metadata belong to this computer, even while an
		// SSH workload is selected. They use local session authentication.
		if r.URL.Path == "/api/client/about" || r.URL.Path == "/api/client/language" || r.URL.Path == "/api/client/notifications" {
			mux.ServeHTTP(w, r)
			return
		}
		if !s.desktopEntry && !s.clientOnly && strings.HasPrefix(r.URL.Path, "/api/") {
			if _, err := guestos.LoadInstallation(s.profile); err != nil {
				http.Error(w, "安装记录无法读取，请保留现有数据", http.StatusConflict)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/api/agent/native") || r.URL.Path == "/api/agent/routes" {
			mux.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/mobile/") {
			// Device trust is scoped to this host. Never forward a paired phone
			// into a different host selected by the desktop SSH client.
			if s.desktopEntry && !s.clientOnly && s.remote != nil && s.remote.desktop != nil && r.Header.Get(connectionHeader) != "" {
				// The desktop's own viewer pins the selected local workload's
				// connection generation. routeRemote checks the acquired target.
				if s.routeRemote(w, r) {
					return
				}
			}
			if s.clientOnly || s.desktopEntry {
				http.Error(w, "请连接运行 Cage 环境的主机", http.StatusConflict)
				return
			}
		} else if s.routeRemote(w, r) {
			return
		}
		ctx, finish, err := s.beginActivity(r.Context())
		if err != nil {
			http.Error(w, "后台正在更新，请重新打开 Cage。", http.StatusServiceUnavailable)
			return
		}
		defer finish()
		r = r.WithContext(ctx)
		accountRoute := r.URL.Path == "/api/account" || strings.HasPrefix(r.URL.Path, "/api/account/")
		bootstrapRoute := r.URL.Path == "/api/session" || r.URL.Path == "/api/launch" || r.URL.Path == "/api/handoff" || r.URL.Path == "/api/remote/manifest"
		trustedDesktop := s.hasTrustedView(r)
		phoneRoom := s.pairedPhoneRoom(r)
		if strings.HasPrefix(r.URL.Path, "/api/") && !accountRoute && !bootstrapRoute && !strings.HasPrefix(r.URL.Path, "/api/diagnostics") && !(r.Method == "GET" && r.URL.Path == "/api/appearance") {
			epoch, access := s.account.accessStateFor(trustedDesktop, phoneRoom != nil)
			if access != accountAccessGranted {
				if access == accountAccessDesktopOnly {
					w.Header().Set("X-Cage-Recovery", "desktop-account-mobile-access")
					http.Error(w, s.account.desktopOnlyMobileMessage(), http.StatusLocked)
					return
				}
				s.account.markMissingAccount(w)
				http.Error(w, "Cage 已锁定，请先登录", http.StatusLocked)
				return
			}
			ctx, cancel := context.WithCancel(r.Context())
			ctx = context.WithValue(ctx, accountEpochKey{}, epoch)
			stop := context.AfterFunc(epoch, cancel)
			defer func() { stop(); cancel() }()
			if phoneRoom != nil {
				stopPhone := context.AfterFunc(phoneRoom, cancel)
				defer stopPhone()
			}
			r = r.WithContext(ctx)
		}
		defer interruptHTTPOnCancel(w, r)()
		response := &diagnosticResponse{ResponseWriter: w}
		w = response
		defer func() {
			if response.status >= 400 && r.URL.Path != "/api/diagnostics/events" {
				_ = diagnostics.New(s.profile).Record("warn", "http", fmt.Sprintf("%s %s returned %d", r.Method, r.Pattern, response.status))
			}
		}()
		defer func() {
			if failure := recover(); failure != nil {
				diagnostics.Log("error", "http", fmt.Sprintf("handler panic: %v\n%s", failure, debug.Stack()))
				panic(failure)
			}
		}()
		if !storageRecoveryRoute(r) {
			if err := s.requireStorageReady(); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			// Writes may change session records; do not reuse a discovery
			// made before or during them.
			s.invalidateConversationDiscovery()
			defer s.invalidateConversationDiscovery()
		}
		mux.ServeHTTP(w, r)
	})
}
func readJSON(w http.ResponseWriter, r *http.Request, v any, limits ...int64) bool {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "需要 JSON 请求", 415)
		return false
	}
	limit := int64(64 << 10)
	if len(limits) > 0 {
		limit = limits[0]
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		http.Error(w, jsonRequestError(err), 400)
		return false
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		http.Error(w, "请求格式无效：请求体只能包含一个 JSON 值", 400)
		return false
	}
	return true
}

// jsonRequestError names the offending field, never its value, so API and
// Agent callers can correct a request.
func jsonRequestError(err error) string {
	const invalid = "请求格式无效"
	var tooLarge *http.MaxBytesError
	var typeErr *json.UnmarshalTypeError
	var syntaxErr *json.SyntaxError
	switch {
	case errors.As(err, &tooLarge):
		return fmt.Sprintf("%s：请求体超过 %d 字节", invalid, tooLarge.Limit)
	case errors.As(err, &typeErr) && typeErr.Field != "":
		return fmt.Sprintf("%s：字段 %s 应为%s", invalid, quoteJSONField(typeErr.Field), jsonKind(typeErr.Type))
	case errors.As(err, &typeErr):
		return invalid + "：请求体应为 JSON " + jsonKind(typeErr.Type)
	case errors.As(err, &syntaxErr), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return invalid + "：不是完整的 JSON"
	}
	// encoding/json reports unknown fields only as text.
	if quoted, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		if field, e := strconv.Unquote(quoted); e == nil {
			return fmt.Sprintf("%s：不支持字段 %s", invalid, quoteJSONField(field))
		}
	}
	return invalid
}

func quoteJSONField(field string) string {
	if r := []rune(field); len(r) > 64 {
		field = string(r[:64]) + "…"
	}
	return strconv.Quote(field)
}

func jsonKind(t reflect.Type) string {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil {
		return "其他类型"
	}
	switch t.Kind() {
	case reflect.String:
		return "字符串"
	case reflect.Bool:
		return "布尔值"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return "数字"
	case reflect.Slice, reflect.Array:
		return "数组"
	case reflect.Map, reflect.Struct:
		return "对象"
	}
	return "其他类型"
}
func jsonReply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
func requestError(w http.ResponseWriter, err error) {
	diagnostics.Log("error", "api", fmt.Sprintf("%v; cause=%v", err, errors.Unwrap(err)))
	http.Error(w, err.Error(), http.StatusBadRequest)
}
func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	if requestInitiator(r) == jobInitiatorSetup {
		s.touchDesktopOnboarding()
	}
	projects, err := s.projectPaths()
	if err != nil {
		requestError(w, errors.New("无法读取项目列表，请检查项目配置"))
		return
	}
	groups, err := s.projectGroups()
	if err != nil {
		requestError(w, err)
		return
	}
	running := s.vmRunning()
	s.mu.Lock()
	defer s.mu.Unlock()
	_, e := os.Stat(filepath.Join(s.profile, "previous-profile.json"))
	projectError := ""
	if len(s.projectErrors) > 0 {
		projectError = "部分项目文件夹尚未连接，环境和浏览器可继续使用。"
	}
	if m := s.clientMounts[s.clientWorkspace]; s.clientWorkspace != "" && (m == nil || !m.live()) {
		projectError = "当前本机项目已断开，请重新共享或选择其他项目。"
	}
	view := map[string]any{
		"account_desktops_supported": s.accountDesktopsSupported(),
		"desktop_catalog_revision":   s.desktopCatalogRevision.Load(),
		"projects":                   projects, "project_groups": groups, "ready": s.ready, "vm_running": running,
		"workspace": s.workspaceLocked(), "environment_directory": s.environmentDir(),
		"environment_identity_directory": cage.EnvironmentIdentityDir(s.profile),
		"local_vm_storage":               s.localVMStorage(), "project_error": projectError, "project_errors": s.projectErrors,
		"storage_recovery": s.storageRecoveryStatusLocked(),
		"keyboard":         s.preferences.Keyboard, "auto_timezone": s.preferences.AutoTimezone, "job": s.jobs.snapshot(),
		"location": s.location, "network_error": s.currentNetworkErrorLocked(), "can_restore": e == nil,
		"network_paused": s.networkPaused, "network_open": s.networkOpenLocked(time.Now()),
		"safety": s.safety, "environment_update_required": s.guestUpdateRequired, "environment_update_error": s.guestUpdateError,
		"abandoned_guests": s.abandonedGuestList(), "abandoned_environment": s.abandonedEnvironmentSystem(),
	}
	if system, err := s.installedLinuxSystem(); err != nil {
		view["guest_system_error"] = err.Error()
	} else if system != guestos.Debian13 {
		view["guest_system"] = system
	}
	jsonReply(w, view)
}
func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	c, err := loadConfig(s.configPath)
	if err != nil {
		requestError(w, err)
		return
	}
	s.mu.Lock()
	p := s.preferences
	s.mu.Unlock()
	jsonReply(w, settingsView(c, p))
}
func (s *Server) proposed(w http.ResponseWriter, r *http.Request) (savedProfile, bool) {
	v, _, ok := s.proposedWithOptions(w, r)
	return v, ok
}

func (s *Server) proposedWithOptions(w http.ResponseWriter, r *http.Request) (savedProfile, bool, bool) {
	var input struct {
		Settings
		SaveStaticDraft bool `json:"save_static_draft"`
	}
	// Older clients omit preferences introduced after their release.
	s.mu.Lock()
	input.MobileRelay = s.preferences.MobileRelay
	input.BalancedDisplay = s.preferences.BalancedDisplay
	input.ProjectFileCache = s.preferences.ProjectFileCache
	input.ReuseClaudeLogin = s.preferences.ReuseClaudeLogin
	input.TransferAllowUnclean = s.preferences.TransferAllowUnclean
	hostTools, hostPluginsOff := s.preferences.HostTools, s.preferences.HostPluginsOff
	input.Clock12Hour = s.preferences.Clock12Hour
	input.BrowserHomeURL = browserHomeURL(s.preferences)
	input.InstallTLS12Compat = s.preferences.InstallTLS12Compat
	input.TLSClientHelloSplit = s.preferences.TLSClientHelloSplit
	input.LocationIPInfoFallback = s.preferences.LocationIPInfoFallback
	s.mu.Unlock()
	// Clients predating the DNS options keep the saved ones.
	if current, err := loadConfig(s.configPath); err == nil {
		input.DNSFallbackURLs = current.DNS.FallbackURLs()
		input.DNSPublicFallback = current.DNS.PublicFallback
		input.DNSBootstrapURLs = current.DNS.BootstrapURLs()
	}
	if !readJSON(w, r, &input) {
		return savedProfile{}, false, false
	}
	// The tools that act on this computer have their own route, open only to
	// this computer's window; saving settings never changes them.
	input.HostTools, input.HostPluginsOff = hostTools, hostPluginsOff
	old, err := loadConfig(s.configPath)
	if err != nil {
		requestError(w, err)
		return savedProfile{}, false, false
	}
	c, p, err := proposedSettings(input.Settings, old, s.profile)
	if err != nil {
		requestError(w, err)
		return savedProfile{}, false, false
	}
	return savedProfile{c, p}, input.SaveStaticDraft, true
}

var checkDNSFunc = cage.CheckDNS

func (s *Server) checkDNS(w http.ResponseWriter, r *http.Request) {
	v, ok := s.proposed(w, r)
	if !ok {
		return
	}
	if err := v.Config.RequireManagedProxy(s.profile); err != nil {
		requestError(w, err)
		return
	}
	result, err := checkDNSFunc(r.Context(), v.Config)
	if err != nil {
		requestError(w, err)
		return
	}
	jsonReply(w, result)
}

func (s *Server) probe(w http.ResponseWriter, r *http.Request) {
	v, ok := s.proposed(w, r)
	if !ok {
		return
	}
	if err := v.Config.RequireManagedProxy(s.profile); err != nil {
		requestError(w, err)
		return
	}
	old, err := loadConfig(s.configPath)
	if err != nil {
		requestError(w, err)
		return
	}
	if p, loadErr := s.proxyStore.Load(s.profile); loadErr != nil || !needsBootstrapPreflight(old, v.Config, p) {
		if err := s.ensureProxyInterfaceBeforeUse(r.Context()); err != nil {
			requestError(w, err)
			return
		}
	}
	loc, usedPreflight, err := s.probeCandidateLocation(r.Context(), old, v.Config, v.Preferences)
	if !usedPreflight {
		if finishErr := s.finishLocationProbe(v.Config, loc, err); finishErr != nil {
			requestError(w, finishErr)
			return
		}
	}
	if err != nil {
		requestError(w, err)
		return
	}
	jsonReply(w, loc)
}
func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	v, saveStaticDraft, ok := s.proposedWithOptions(w, r)
	if !ok {
		return
	}
	kind := "settings"
	old, err := loadConfig(s.configPath)
	s.mu.Lock()
	previous := s.preferences
	s.mu.Unlock()
	if err == nil && old == v.Config && previous.AutoTimezone == v.Preferences.AutoTimezone && previous.AutoLanguage == v.Preferences.AutoLanguage && previous.Workspace != v.Preferences.Workspace {
		kind = "project"
	}
	s.launchRequest(w, r, kind, func() error {
		s.mu.Lock()
		v.Preferences.BrowserHomeURL = s.preferences.BrowserHomeURL
		s.mu.Unlock()
		return s.applyWithOptions(v, true, saveStaticDraft)
	})
}
func (s *Server) restore(w http.ResponseWriter, r *http.Request) {
	var v savedProfile
	b, e := os.ReadFile(filepath.Join(s.profile, "previous-profile.json"))
	if e != nil || json.Unmarshal(b, &v) != nil {
		requestError(w, errors.New("没有可恢复的配置"))
		return
	}
	if v.Config.DNS, e = cage.LoadDNSOptionsFile(filepath.Join(s.profile, previousDNSFile)); e != nil {
		requestError(w, errors.New("没有可恢复的配置"))
		return
	}
	// The saved configuration does not carry this switch; the preferences
	// saved with it do. Without it a restored switch is written back while
	// the environment keeps running with the other setting.
	v.Config.TLSClientHelloSplit = v.Preferences.TLSClientHelloSplit
	s.launchRequest(w, r, "restore", func() error {
		if err := s.apply(v, false); err != nil {
			return err
		}
		return s.startEnvironment(false)
	})
}
func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	if requestInitiator(r) == jobInitiatorAgent && s.desktopOnboardingActive() {
		http.Error(w, "桌面端正在进行首次引导，请先在 Cage 窗口完成引导；如需由 Agent 继续，请先调用 initialization.set 并传入 {\"action\":\"release\"}", http.StatusConflict)
		return
	}
	var request struct {
		guestInstallRequest
		// SetAsideAbandoned moves an environment of a no longer supported
		// system aside, unchanged, before a Debian environment is created.
		SetAsideAbandoned bool `json:"set_aside_abandoned"`
	}
	if r.ContentLength != 0 && !readJSON(w, r, &request) {
		return
	}
	input := request.guestInstallRequest
	if input.GuestProfile != nil {
		if err := input.GuestProfile.Validate(); err != nil {
			requestError(w, err)
			return
		}
	}
	if abandonedGuestSystem(input.System) {
		http.Error(w, abandonedGuestMessage(input.System), http.StatusConflict)
		return
	}
	if request.SetAsideAbandoned {
		s.startAfterSettingAside(w, r, input)
		return
	}
	pending, err := guestos.LoadInstallation(s.profile)
	if err != nil {
		http.Error(w, "客户机安装记录无法读取，请保留现有数据", http.StatusConflict)
		return
	}
	if pending != nil && !hostimage.IsLinux(pending.System) {
		http.Error(w, abandonedGuestMessage(pending.System), http.StatusConflict)
		return
	}
	if pending != nil && input.System != "" && pending.System != input.System {
		http.Error(w, "此环境已绑定所选 Linux 系统，请继续原安装", http.StatusConflict)
		return
	}
	if pending != nil {
		input.System = pending.System
		s.beginGuestInstall(w, r, input)
		return
	}
	boundSystem, err := s.installedLinuxSystem()
	if abandoned := new(hostimage.AbandonedSystemError); errors.As(err, &abandoned) {
		http.Error(w, abandonedGuestMessage(abandoned.System), http.StatusConflict)
		return
	}
	if err != nil {
		requestError(w, err)
		return
	}
	if input.System != "" && input.System != boundSystem {
		http.Error(w, "当前环境无法启动所选客户机系统，请保留原环境并重新检查安装选择", http.StatusConflict)
		return
	}
	s.launchRequest(w, r, "start", func() error {
		if err := s.prepareGuestProfile(input.GuestProfile); err != nil {
			return err
		}
		if err := s.prepareEnvironment(); err != nil {
			return err
		}
		return s.startEnvironment(true)
	})
}

// startAfterSettingAside is only offered in the Cage window, where the user
// confirmed that the old environment is moved aside.
func (s *Server) startAfterSettingAside(w http.ResponseWriter, r *http.Request, input guestInstallRequest) {
	if !s.hasTrustedView(r) || requestInitiator(r) == jobInitiatorAgent || !s.localVMStorage() {
		http.Error(w, "请在本机 Cage 窗口确认保留旧 Ubuntu 环境并新建 Debian", http.StatusForbidden)
		return
	}
	if input.System != "" && input.System != guestos.Debian13 {
		http.Error(w, "新环境只提供 Debian 13", http.StatusConflict)
		return
	}
	if s.abandonedEnvironmentSystem() == "" {
		http.Error(w, "没有需要保留的旧 Ubuntu 环境", http.StatusConflict)
		return
	}
	s.launchRequest(w, r, "start", func() error {
		if _, err := s.setAsideAbandonedEnvironment(); err != nil {
			return err
		}
		if err := s.prepareGuestProfile(input.GuestProfile); err != nil {
			return err
		}
		if err := s.prepareEnvironment(); err != nil {
			return err
		}
		return s.hookStartEnv(true)
	})
}

func (s *Server) stop(w http.ResponseWriter, r *http.Request) {
	s.requestStop(w, r, false)
}

func (s *Server) requestStop(w http.ResponseWriter, r *http.Request, logout bool) {
	if err := s.requireStorageReady(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	initiator := requestInitiator(r)
	for _, name := range []string{cage.InstanceName(s.profile, s.environmentDir()), "base-building"} {
		if owner := cage.InstanceJobID(filepath.Join(s.profile, "state"), name); owner != "" {
			_ = cage.RequestJobStop(s.profile, owner)
		}
	}
	jobID := s.jobs.cancelRunningBy(initiator)
	if jobID != "" {
		_ = cage.RequestJobStop(s.profile, jobID)
	}
	_ = s.pauseNetworkNow("正在停止环境")
	kind := "stop"
	if logout {
		kind = "logout"
	}
	s.launchFrom(w, initiator, kind, s.stopEnvironment)
}

func (s *Server) cookieName() string { u, _ := url.Parse(s.origin); return cookieName + "_" + u.Port() }

func (s *Server) issueTicket() string {
	return s.issueTicketWithScope(false)
}

func (s *Server) issueTicketWithScope(remoteDesktop bool) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tickets == nil {
		s.tickets = map[string]time.Time{}
	}
	if s.remoteDesktopTickets == nil {
		s.remoteDesktopTickets = map[string]bool{}
	}
	for token, expires := range s.tickets {
		if time.Now().After(expires) {
			delete(s.tickets, token)
			delete(s.remoteDesktopTickets, token)
		}
	}
	token := randomToken()
	s.tickets[token] = time.Now().Add(time.Minute)
	if remoteDesktop {
		s.remoteDesktopTickets[token] = true
	}
	return token
}

func (s *Server) consumeTicket(token string) bool {
	ok, _ := s.consumeTicketWithScope(token)
	return ok
}

func (s *Server) consumeTicketWithScope(token string) (bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	expires, ok := s.tickets[token]
	remoteDesktop := s.remoteDesktopTickets[token]
	delete(s.tickets, token)
	delete(s.remoteDesktopTickets, token)
	valid := ok && time.Now().Before(expires)
	return valid, valid && remoteDesktop
}
