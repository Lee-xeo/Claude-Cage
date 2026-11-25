// Package workbench owns the trusted host UI; guest web content never runs in it.
package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/exitpolicy"
	"github.com/sylvinus/agent-vm/internal/host"
	"github.com/sylvinus/agent-vm/internal/mobilelink"
	"github.com/sylvinus/agent-vm/internal/mounts"
	"github.com/sylvinus/agent-vm/internal/paths"
)

type Preferences struct {
	ReuseClaudeLogin     bool `json:"reuse_claude_login"`
	TransferAllowUnclean bool `json:"transfer_allow_unclean"`
	BalancedDisplay      bool `json:"balanced_display"`
	ProjectFileCache     bool `json:"project_file_cache"`
	// HostTools lets Claude in the environment run commands on this computer.
	HostTools bool `json:"host_tools,omitempty"`
	// HostPluginsOff names the plugins the user turned off, separated by
	// commas; "commands" is the built-in one. One string keeps preferences
	// comparable.
	HostPluginsOff        string `json:"host_plugins_off,omitempty"`
	MobileRelay           string `json:"mobile_relay,omitempty"`
	Workspace             string `json:"workspace"`
	BrowserHomeURL        string `json:"browser_home_url"`
	ClaudeTerminalCommand string `json:"claude_terminal_command"`
	Keyboard              string `json:"keyboard"`
	Clock12Hour           bool   `json:"clock_12_hour"`
	AutoTimezone          bool   `json:"auto_timezone"`
	AutoLanguage          bool   `json:"auto_language"`
	// DebianMirror is the APT source policy for Debian guests, chosen in the
	// install dialog after measuring the sources through the proxy. Empty
	// keeps the guest default ("auto": official first); "nju" or "ustc" puts
	// that mirror first.
	DebianMirror string `json:"debian_mirror,omitempty"`
	// InstallTLS12Compat caps curl at TLS 1.2 during guest installation and
	// tool downloads for proxies that drop curl's TLS 1.3 ClientHello. Off by
	// default; does not affect normal browsing or API connections.
	InstallTLS12Compat bool `json:"install_tls12_compat,omitempty"`
	// TLSClientHelloSplit splits large (>1000 byte) TLS ClientHello records
	// across two writes with a short gap on outbound proxy tunnels, keeping
	// full TLS 1.3 / post-quantum handshakes intact for proxy chains that drop
	// large initial writes with EOF. Off by default.
	TLSClientHelloSplit bool `json:"tls_client_hello_split,omitempty"`
	// LocationIPInfoFallback lets ipinfo.io receive the measured exit IP
	// after the five default location providers all fail. Off by default; its
	// answer counts at country level only.
	LocationIPInfoFallback bool `json:"location_ipinfo_fallback,omitempty"`
}

func (p Preferences) locationOptions() exitpolicy.LookupOptions {
	return exitpolicy.LookupOptions{IPInfoFallback: p.LocationIPInfoFallback}
}

const defaultBrowserHomeURL = "https://www.bing.com/"

func claudeTerminalCommand(p Preferences) string {
	if strings.TrimSpace(p.ClaudeTerminalCommand) == "" {
		return "claude"
	}
	return strings.TrimSpace(p.ClaudeTerminalCommand)
}

func browserHomeURL(p Preferences) string {
	if p.BrowserHomeURL == "" {
		return defaultBrowserHomeURL
	}
	return p.BrowserHomeURL
}

// The environment directory identifies the VM and stores download transfers.
// It is independent of project selection and never grants a project by itself.
func environmentWorkspace(profile string) string { return cage.EnvironmentDir(profile) }

func guestPath(path string) string { return paths.Guest(paths.Host(path)) }

func (s *Server) prepareEnvironment() error {
	record, err := cage.LoadEnvironmentRecord(s.profile)
	if err != nil {
		return err
	}
	s.mu.Lock()
	p := s.preferences
	s.mu.Unlock()
	path := environmentWorkspace(s.profile)
	// Promote an existing installation's VM once, preserving its disk and login state.
	if _, err := os.Stat(filepath.Join(s.profile, "environment.json")); os.IsNotExist(err) {
		previous := s.workspace()
		if previous != "" {
			if _, err := os.Stat(filepath.Join(s.profile, "state", "lima", cage.InstanceName(s.profile, previous), "lima.yaml")); err == nil {
				path = previous
			}
		}
	}
	// Keep the original naming input so an existing virtual disk is reused.
	// Project files remain at their original path; only transfers use this root.
	if path != cage.DefaultEnvironmentDir(s.profile) {
		if info, e := os.Stat(path); e == nil && !info.IsDir() {
			return errors.New("环境目录被文件占用，请保留原数据并恢复目录后重试")
		} else if e != nil && !os.IsNotExist(e) {
			return e
		}
		if record.IdentityDirectory == "" {
			record.IdentityDirectory = path
		}
		path = cage.DefaultEnvironmentDir(s.profile)
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	path, err = validateWorkspace(path, s.profile)
	if err != nil {
		return err
	}
	record.Directory = path
	if err = writePrivate(filepath.Join(s.profile, "environment.json"), record); err != nil {
		return err
	}
	if p.Workspace != "" && record.IdentityDirectory != "" && paths.Equal(paths.Host(p.Workspace), paths.Host(record.IdentityDirectory)) {
		// A private legacy directory cannot remain an authorized project.
		if _, err := validateWorkspace(p.Workspace, s.profile); err != nil {
			p.Workspace = ""
		}
	}
	if p.Workspace == "" {
		if saved, err := s.readSharedProjects(); err == nil {
			for _, candidate := range saved.Paths {
				if valid, err := validateWorkspace(candidate, s.profile); err == nil {
					p.Workspace = valid
					break
				}
			}
		}
	}
	if err := writePrivate(filepath.Join(s.profile, "workbench.json"), p); err != nil {
		return err
	}
	s.mu.Lock()
	s.preferences = p
	s.mu.Unlock()
	return nil
}

type Settings struct {
	Preferences
	DNSURL string `json:"dns_url"`
	// Backup DoH endpoints tried through the selected proxy after dns_url,
	// the opt-in second public provider, and numeric DoH endpoints that
	// resolve proxy server names on the host network.
	DNSFallbackURLs   []string        `json:"dns_fallback_urls"`
	DNSPublicFallback bool            `json:"dns_public_fallback"`
	DNSBootstrapURLs  []string        `json:"dns_bootstrap_urls"`
	Timezone          string          `json:"timezone"`
	Locale            string          `json:"locale"`
	BrowserLanguage   string          `json:"browser_language"`
	ExitPolicy        cage.ExitPolicy `json:"exit_policy"`
}
type Location struct {
	IP                       string    `json:"ip"`
	Country                  string    `json:"country"`
	CountryCode              string    `json:"country_code"`
	Region                   string    `json:"region"`
	RegionCode               string    `json:"region_code,omitempty"`
	SuggestedLocale          string    `json:"suggested_locale,omitempty"`
	SuggestedBrowserLanguage string    `json:"suggested_browser_language,omitempty"`
	City                     string    `json:"city"`
	Timezone                 string    `json:"timezone"`
	CheckedAt                time.Time `json:"checked_at"`
}
type savedProfile struct {
	Config      cage.Config `json:"config"`
	Preferences Preferences `json:"preferences"`
}

func writePrivate(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err = host.EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".cage-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = host.ProtectFile(f.Name()); err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}
func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

func settingsView(c cage.Config, p Preferences) Settings {
	if p.MobileRelay == mobilelink.OfficialRelay {
		p.MobileRelay = ""
	}
	p.BrowserHomeURL = browserHomeURL(p)
	p.ClaudeTerminalCommand = claudeTerminalCommand(p)
	return Settings{Preferences: p, DNSURL: c.DNSURL, DNSFallbackURLs: nonNil(c.DNS.FallbackURLs()), DNSPublicFallback: c.DNS.PublicFallback, DNSBootstrapURLs: nonNil(c.DNS.BootstrapURLs()), Timezone: c.Timezone, Locale: c.Locale, BrowserLanguage: c.BrowserLanguage, ExitPolicy: c.ExitPolicy.Normalized()}
}
func proposedSettings(input Settings, old cage.Config, profile string) (cage.Config, Preferences, error) {
	if input.ExitPolicy == (cage.ExitPolicy{}) {
		input.ExitPolicy = old.ExitPolicy
	}
	c := cage.Config{ProxyURL: old.ProxyURL, ProxyRevision: old.ProxyRevision, DNSURL: input.DNSURL, DNS: cage.NewDNSOptions(input.DNSFallbackURLs, input.DNSBootstrapURLs, input.DNSPublicFallback), TLSClientHelloSplit: input.TLSClientHelloSplit, Timezone: input.Timezone, Locale: input.Locale, BrowserLanguage: input.BrowserLanguage, ExitPolicy: input.ExitPolicy.Normalized()}
	if err := c.Validate(); err != nil {
		return c, input.Preferences, err
	}
	if input.Keyboard != "mac" && input.Keyboard != "native" {
		return c, input.Preferences, errors.New("请选择 Mac 或原生 Linux 按键习惯")
	}
	if input.DebianMirror != "" && !cage.ValidDebianMirror(input.DebianMirror) {
		return c, input.Preferences, errors.New("Debian 软件源只能选择官方源优先、指定国内镜像优先或固定官方源")
	}
	p := input.Preferences
	p.MobileRelay = strings.TrimSuffix(strings.TrimSpace(p.MobileRelay), "/")
	if p.MobileRelay != "" && (!strings.HasPrefix(p.MobileRelay, "https://") || !mobilelink.ValidRelayOrigin(p.MobileRelay)) {
		return c, p, errors.New("中继地址须为 HTTPS 地址，只包含主机和可选端口")
	}
	if len(p.ClaudeTerminalCommand) > 4096 || strings.ContainsAny(p.ClaudeTerminalCommand, "\x00\r\n") {
		return c, p, errors.New("Claude 终端命令必须为一行，且不超过 4096 字节")
	}
	p.ClaudeTerminalCommand = claudeTerminalCommand(p)
	p.BrowserHomeURL = strings.TrimSpace(browserHomeURL(p))
	home, e := url.Parse(p.BrowserHomeURL)
	if e != nil || home == nil || (home.Scheme != "https" && home.Scheme != "http") || home.Hostname() == "" || home.User != nil {
		return c, p, errors.New("浏览器默认网站必须是完整的 HTTP 或 HTTPS 网址，且不能包含用户名或密码")
	}
	if p.Workspace != "" {
		workspace, err := validateWorkspace(p.Workspace, profile)
		if err != nil {
			return c, p, err
		}
		p.Workspace = workspace
	}
	return c, p, nil
}

// mountableEnvironmentDir refuses an environment directory Lima cannot share
// with the guest. The CLI refuses it too, but only when it starts the VM after
// setup, and its reason never reaches the window. The directory follows the
// configuration directory, so a Windows account name with a space is enough.
func mountableEnvironmentDir(dir string) error {
	why, bad := mounts.Unmountable(paths.Host(dir))
	if !bad {
		return nil
	}
	reason := "Lima 无法把它共享进环境（" + why + "）"
	if strings.HasPrefix(why, "contains whitespace") {
		reason = "路径含有空格，Lima 无法把它共享进环境"
	}
	return errors.New("Cage 的内部传输目录 " + dir + " " + reason + "。该目录位于 Cage 配置目录旁，用户目录含空格（例如 Windows 账户名含空格）时就会出现。请把 Cage 配置目录移到不含空格的路径后再启动，步骤见故障排查文档“用户目录含空格时环境无法启动”。")
}

func validateWorkspace(path, profile string) (string, error) {
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(path, "~/") {
		path = filepath.Join(home, path[2:])
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("项目路径无效")
	}
	abs, err = paths.Resolve(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("项目目录不存在或无法访问")
		}
		reason := err
		if pe := (*os.PathError)(nil); errors.As(err, &pe) {
			reason = pe.Err
		}
		return "", errors.New("项目目录无法访问：" + reason.Error())
	}
	st, err := os.Stat(abs)
	if err != nil || !st.IsDir() {
		return "", errors.New("请选择一个项目文件夹")
	}
	if realHome, err := paths.Resolve(home); err == nil {
		home = realHome
	}
	if executable, e := os.Executable(); e == nil {
		if real, e := paths.Resolve(executable); e == nil && cage.StorageContains(abs, real) {
			return "", errors.New("不能共享正在运行的工作台程序所在目录。请将安装版 Cage 放在项目外，并重启控制服务后选择此项目。")
		}
	}
	if mounts.UnderName(filepath.ToSlash(abs), nil) {
		return "", errors.New("请选择项目根目录，不要共享 Git 等版本控制元数据目录")
	}
	if paths.Equal(abs, home) || filepath.Dir(abs) == abs {
		return "", errors.New("请选择具体项目，不要共享主目录、Cage 配置或包含配置的上级目录")
	}
	private, err := privateProjectDirectories(profile)
	if err != nil {
		return "", err
	}
	for _, dir := range private {
		if cage.StoragePathsOverlap(abs, dir) {
			return "", errors.New("Cage 配置、环境存储、迁移备份及其上级目录不能共享给虚拟机")
		}
	}
	if err := validateHostProjectData(abs, profile); err != nil {
		return "", err
	}
	return abs, nil
}

// probeLocation uses the same fail-closed transport as the VM. No host resolver,
// environment proxy, or direct fallback participates, including redirects.
func probeLocation(ctx context.Context, c cage.Config, options exitpolicy.LookupOptions) (Location, error) {
	ip, err := cage.ProbeExitIP(ctx, c)
	if err != nil {
		return Location{}, err
	}
	if state, message := exitpolicy.CheckExitCountry(ip, nil); state == cage.CheckFailed {
		return Location{IP: ip, CheckedAt: time.Now()}, errors.New(message)
	}
	loc, err := probeLocationForIP(ctx, c, ip, options)
	if err != nil {
		return Location{IP: ip, CheckedAt: time.Now()}, err
	}
	observed := loc.exitLocation()
	if state, message := exitpolicy.CheckExitCountry(ip, &observed); state != cage.CheckPassed {
		return loc, errors.New(message)
	}
	return loc, nil
}

// All exit modes need country evidence. Manual locale settings only control
// whether the observed timezone/language is applied to the environment.
var probeEnvironmentLocation = func(ctx context.Context, c cage.Config, p Preferences) (Location, error) {
	return probeLocation(ctx, c, p.locationOptions())
}

func (l Location) exitLocation() cage.ExitLocation {
	return cage.ExitLocation{IP: l.IP, CountryCode: l.CountryCode, Region: l.Region, City: l.City}
}

func probeLocationForIP(ctx context.Context, c cage.Config, ip string, options exitpolicy.LookupOptions) (Location, error) {
	address := net.ParseIP(ip)
	if address == nil {
		return Location{}, errors.New("出口地址无效")
	}
	e, err := cage.NewEgress(c)
	if err != nil {
		return Location{}, err
	}
	transport := &http.Transport{Proxy: nil, DialContext: e.DialContext, ForceAttemptHTTP2: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }}
	return lookupLocation(ctx, client, address, options)
}
