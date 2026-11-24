// Package proxyimport reads node configurations from known local client data
// directories. It never starts a client, runs its scripts, or changes its files.
package proxyimport

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/sylvinus/agent-vm/internal/managedproxy"
	"go.yaml.in/yaml/v3"
)

// Config exposes display metadata only. Data and subscription credentials stay
// on the host; ID is a content-bound, session-specific handle, not a file path.
type Config struct {
	ID          string `json:"id"`
	SourceID    string `json:"-"`
	Client      string `json:"client"`
	Name        string `json:"name"`
	Current     bool   `json:"current"`
	Nodes       int    `json:"nodes"`
	Skipped     int    `json:"skipped"`
	Warning     string `json:"warning,omitempty"`
	Error       string `json:"error,omitempty"`
	Refreshable bool   `json:"refreshable"`
	Data        []byte `json:"-"`
	URL         string `json:"-"`
}

// Layout selects how a client directory is read. Plain scans top-level
// config files; Verge and Party read the runtime config plus the client's
// profile index without running any enhancement scripts.
type Layout int

const (
	Plain Layout = iota
	Verge
	// Party is Clash Party (formerly Mihomo Party). Its root config.yaml holds
	// app settings, while mihomo runs from work/ with work/config.yaml.
	Party
	// OixCloud is FlClash for oixCloud. Its root config.yaml holds app settings
	// without nodes and profiles/*.yaml may be age-encrypted ciphertext.
	OixCloud
	// Surge reads the INI-style .conf profiles of Surge for Mac.
	Surge
	// Nyanpasu is Clash Nyanpasu. Its runtime file moved between releases and
	// its root config files may hold only app or core settings.
	Nyanpasu
)

const encryptedConfigError = "配置文件使用 age 加密，Cage 不会解密私有配置，无法直接导入。请从原客户端或服务商导出明文 Clash/Mihomo YAML 后通过“添加来源”导入，或手动添加节点"

type Location struct {
	Client, Directory string
	Layout            Layout
	// Selected is the file name of the profile the client uses, when known.
	Selected string
}

// Locations also makes platform path rules testable without probing another
// user's home. Portable/custom locations remain available through file import.
func Locations(platform, home, dataHome, configHome string) []Location {
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	if dataHome == "" {
		switch platform {
		case "darwin":
			dataHome = filepath.Join(home, "Library", "Application Support")
		case "windows":
			dataHome = filepath.Join(home, "AppData", "Roaming")
		default:
			dataHome = filepath.Join(home, ".local", "share")
		}
	}
	// Clash Party is an Electron app; its userData lives under the config
	// home on Linux rather than the data home.
	partyHome := configHome
	if platform == "darwin" || platform == "windows" {
		partyHome = dataHome
	}
	// Clash Nyanpasu 1.6 and later use the platform config root (the data home
	// here on macOS and Windows); earlier releases used ~/.config on every
	// platform regardless of XDG_CONFIG_HOME.
	nyanpasuHome := filepath.Join(configHome, "clash-nyanpasu")
	if platform == "darwin" || platform == "windows" {
		nyanpasuHome = filepath.Join(dataHome, "Clash Nyanpasu", "config")
	}
	out := []Location{
		{"FlClash", filepath.Join(dataHome, "com.follow.clash"), Plain, ""},
		{"FlClash", filepath.Join(dataHome, "com.follow.clash", "profiles"), Plain, ""},
		{"FlClash for oixCloud", filepath.Join(dataHome, "com.oixcloud.clash"), OixCloud, ""},
		{"Clash Verge Rev", filepath.Join(dataHome, "io.github.clash-verge-rev.clash-verge-rev"), Verge, ""},
		// Clash Neo is a Verge fork and keeps the same profile layout.
		{"Clash Neo", filepath.Join(dataHome, "io.github.clash-neo"), Verge, ""},
		{"Clash Party", filepath.Join(partyHome, "mihomo-party"), Party, ""},
		{"Clash Nyanpasu", nyanpasuHome, Nyanpasu, ""},
		{"Clash Verge", filepath.Join(configHome, "clash-verge"), Verge, ""},
		{"Clash / ClashX", filepath.Join(configHome, "clash"), Plain, ""},
		{"Mihomo", filepath.Join(configHome, "mihomo"), Plain, ""},
		{"ShadowsocksR", filepath.Join(configHome, "shadowsocksr"), Plain, ""},
		{"Shadowsocks", filepath.Join(configHome, "shadowsocks"), Plain, ""},
	}
	if legacy := filepath.Join(home, ".config", "clash-nyanpasu"); legacy != nyanpasuHome {
		out = append(out, Location{"Clash Nyanpasu", legacy, Nyanpasu, ""})
	}
	if platform == "darwin" {
		// ClashX Meta keeps its YAML configs at the top level of
		// ~/.config/clash.meta; it does not read XDG_CONFIG_HOME.
		out = append(out, Location{"ClashX Meta", filepath.Join(home, ".config", "clash.meta"), Plain, ""})
		if custom := filepath.Join(configHome, "clash.meta"); custom != filepath.Join(home, ".config", "clash.meta") {
			out = append(out, Location{"ClashX Meta", custom, Plain, ""})
		}
		out = append(out, Location{"ShadowsocksX-NG", filepath.Join(home, ".ShadowsocksX-NG"), Plain, ""})
		out = append(out, surgeProfileDirectories(home)...)
	}
	if platform == "windows" {
		out = append(out,
			Location{"FlClash", filepath.Join(dataHome, "com.follow", "FlClash"), Plain, ""},
			Location{"FlClash", filepath.Join(dataHome, "com.follow", "FlClash", "profiles"), Plain, ""},
			Location{"FlClash", filepath.Join(dataHome, "com.follow", "clash"), Plain, ""},
			Location{"FlClash", filepath.Join(dataHome, "com.follow", "clash", "profiles"), Plain, ""},
			Location{"FlClash for oixCloud", filepath.Join(dataHome, "com.oixcloud", "FlClash"), OixCloud, ""},
			Location{"Clash / Mihomo", filepath.Join(home, ".config", "clash"), Plain, ""},
			Location{"Clash Verge", filepath.Join(home, ".config", "clash-verge"), Verge, ""},
			Location{"ShadowsocksR", filepath.Join(dataHome, "ShadowsocksR"), Plain, ""})
	}
	return out
}

func Discover(key []byte) ([]Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, errors.New("无法读取当前用户的配置目录，请重新检测或手动导入文件")
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if runtime.GOOS == "windows" {
		dataHome = os.Getenv("APPDATA")
	}
	if runtime.GOOS == "darwin" {
		dataHome = ""
	}
	return Scan(Locations(runtime.GOOS, home, dataHome, os.Getenv("XDG_CONFIG_HOME")), key), nil
}

func Scan(locations []Location, key []byte) []Config {
	out := []Config{}
	seen := map[string]bool{}
	for _, loc := range locations {
		root, err := filepath.EvalSymlinks(loc.Directory)
		if err != nil || seen[root] {
			continue
		}
		seen[root] = true
		// providers is the directory, relative to root, that the client's
		// mihomo core uses to resolve proxy-provider cache paths.
		build := func(file, providers, name, remote string, current bool) (Config, bool, bool) {
			data, err := readWithin(root, file)
			if os.IsNotExist(err) {
				return Config{}, false, false
			}
			c := Config{Client: loc.Client, Name: displayName(name), Current: current}
			identity := filepath.Join(root, file)
			encrypted := false
			if err != nil {
				c.Error = "无法读取这份配置，请检查文件权限或改为手动导入"
			} else if loc.Layout == Surge {
				c.Data, c.Warning, err = surgeData(root, file)
				if err != nil {
					c.Error = err.Error()
				}
			} else if isAgeEncrypted(data) {
				encrypted = true
				c.Error = encryptedConfigError
			} else {
				c.Data, c.Warning, err = nodeData(root, providers, data)
				if err != nil {
					c.Error = "这份配置没有可导入的节点，或格式暂不支持；请从原软件导出 Clash/Mihomo 配置"
				}
			}
			if c.Error == "" {
				var preview managedproxy.Store
				if err := preview.Import(c.Name, "", c.Data, "preview", 0, ""); err != nil {
					c.Error = "这份配置没有可导入的节点；请从原软件导出含节点的 Clash/Mihomo 配置"
					// A Surge profile is converted here, so the importer's reasons
					// (such as a chain through a group) describe that profile.
					if errors.Is(err, managedproxy.ErrLocalProxyNode) || loc.Layout == Surge {
						c.Error = err.Error()
					} else if errors.Is(err, managedproxy.ErrSourceNodeDNS) {
						c.Error = err.Error() + "。请在原软件中把节点 DNS（dns.proxy-server-nameserver）改为远程 HTTPS DoH 地址后重新导出，或在 Cage 中手动添加节点"
					}
				} else {
					c.Nodes = len(preview.Sources[0].Nodes)
					c.Skipped = len(preview.Sources[0].Skipped)
				}
			}
			if u, err := url.Parse(remote); err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == "" {
				c.URL = remote
				c.Refreshable = true
			}
			signConfig(&c, identity, key)
			return c, true, encrypted
		}
		add := func(file, providers, name, remote string, current bool) {
			if c, ok, _ := build(file, providers, name, remote, current); ok {
				out = append(out, c)
			}
		}
		switch loc.Layout {
		case OixCloud:
			var encryptedFiles []string
			var fallbackErrors []Config
			foundImportable := false
			for _, subdir := range []string{"", "profiles"} {
				files, err := listWithin(root, subdir)
				if err != nil {
					continue
				}
				count := 0
				for _, file := range files {
					if file.IsDir() {
						continue
					}
					ext := strings.ToLower(filepath.Ext(file.Name()))
					if ext != ".yaml" && ext != ".yml" && ext != ".json" && ext != ".age" {
						continue
					}
					if count >= 32 {
						break
					}
					count++
					rel := file.Name()
					if subdir != "" {
						rel = filepath.Join(subdir, file.Name())
					}
					c, ok, encrypted := build(rel, "", file.Name(), "", subdir == "" && (file.Name() == "config.yaml" || file.Name() == "config.json"))
					if !ok {
						continue
					}
					if encrypted {
						encryptedFiles = append(encryptedFiles, rel)
						continue
					}
					if ext == ".age" {
						continue
					}
					if c.Error == "" {
						foundImportable = true
						out = append(out, c)
					} else {
						fallbackErrors = append(fallbackErrors, c)
					}
				}
			}
			if len(encryptedFiles) > 0 {
				sort.Strings(encryptedFiles)
				notice := Config{
					Client: loc.Client,
					Name:   "加密配置",
					Error:  encryptedConfigError,
				}
				signConfig(&notice, filepath.Join(root, "profiles"), key, encryptedFiles...)
				out = append(out, notice)
			} else if !foundImportable {
				out = append(out, fallbackErrors...)
			}
		case Party:
			add(filepath.Join("work", "config.yaml"), "work", "当前生效配置", "", true)
			data, err := readWithin(root, "profile.yaml")
			if err != nil {
				continue
			}
			var index struct {
				Current string `yaml:"current"`
				Items   []struct {
					ID   string `yaml:"id"`
					Name string `yaml:"name"`
					Type string `yaml:"type"`
					URL  string `yaml:"url"`
				} `yaml:"items"`
			}
			if yaml.Unmarshal(data, &index) != nil {
				continue
			}
			for i, item := range index.Items {
				if i >= 64 {
					break
				}
				if item.Type != "remote" && item.Type != "local" {
					continue
				}
				if item.ID == "" || item.ID == "." || item.ID == ".." || filepath.Base(item.ID) != item.ID || strings.ContainsAny(item.ID, `/\\`) {
					continue
				}
				name := item.Name
				if name == "" {
					name = "已保存的配置"
				}
				add(filepath.Join("profiles", item.ID+".yaml"), "work", name, item.URL, item.ID == index.Current)
			}
		case Verge:
			// Prefer the latest generated runtime file, which includes local
			// overrides; never execute profile enhancement scripts ourselves.
			for _, file := range []string{"clash-verge.yaml", "config.yaml"} {
				if _, err := os.Stat(filepath.Join(root, file)); !os.IsNotExist(err) {
					add(file, "", "当前生效配置", "", true)
					break
				}
			}
			data, err := readWithin(root, "profiles.yaml")
			if err != nil {
				continue
			}
			var index struct {
				Current string `yaml:"current"`
				Items   []struct {
					UID  string `yaml:"uid"`
					Name string `yaml:"name"`
					File string `yaml:"file"`
					Type string `yaml:"type"`
					URL  string `yaml:"url"`
				} `yaml:"items"`
			}
			if yaml.Unmarshal(data, &index) != nil {
				continue
			}
			for i, item := range index.Items {
				if i >= 64 {
					break
				}
				if item.Type != "remote" && item.Type != "local" {
					continue
				}
				if item.File == "" || filepath.Base(item.File) != item.File || strings.ContainsAny(item.File, `/\\`) {
					continue
				}
				name := item.Name
				if name == "" {
					name = "已保存的配置"
				}
				add(filepath.Join("profiles", item.File), "", name, item.URL, item.UID == index.Current)
			}
		case Nyanpasu:
			// 2.x writes runtime/clash-config.yaml and keeps core settings in the
			// root clash-config.yaml, which 1.6 used as its runtime file; earlier
			// releases wrote clash-verge.yaml. Report a failure only when no
			// candidate yields nodes.
			var fallback *Config
			for _, file := range []string{filepath.Join("runtime", "clash-config.yaml"), "clash-config.yaml", "clash-verge.yaml"} {
				c, ok, _ := build(file, "", "当前生效配置", "", true)
				if !ok {
					continue
				}
				if c.Error == "" {
					out = append(out, c)
					fallback = nil
					break
				}
				if fallback == nil {
					fallback = &c
				}
			}
			if fallback != nil {
				out = append(out, *fallback)
			}
			data, err := readWithin(root, "profiles.yaml")
			if err != nil {
				continue
			}
			for _, p := range nyanpasuProfiles(data) {
				add(filepath.Join("profiles", p.file), "", p.name, p.url, p.current)
			}
		case Surge:
			files, err := os.ReadDir(root)
			if err != nil {
				continue
			}
			count := 0
			for _, file := range files {
				if file.IsDir() || !strings.EqualFold(filepath.Ext(file.Name()), ".conf") {
					continue
				}
				if count >= 32 {
					break
				}
				count++
				add(file.Name(), "", strings.TrimSuffix(file.Name(), filepath.Ext(file.Name())), "", file.Name() == loc.Selected)
			}
		default:
			files, err := os.ReadDir(root)
			if err != nil {
				continue
			}
			count := 0
			for _, file := range files {
				ext := strings.ToLower(filepath.Ext(file.Name()))
				if file.IsDir() || (ext != ".yaml" && ext != ".yml" && ext != ".json") {
					continue
				}
				if count >= 32 {
					break
				}
				count++
				add(file.Name(), "", file.Name(), "", file.Name() == "config.yaml" || file.Name() == "config.json" || file.Name() == "gui-config.json")
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Error == "") != (out[j].Error == "") {
			return out[i].Error == ""
		}
		return out[i].Current && !out[j].Current
	})
	return out
}

type nyanpasuProfile struct {
	file, name, url string
	current         bool
}

// nyanpasuProfiles reads both profiles.yaml schemas: 1.x keeps file and url on
// each item and may list several current items; 2.x nests them under
// config.source. Script, merge, transform and composition items have no
// standalone node file and are skipped.
func nyanpasuProfiles(data []byte) []nyanpasuProfile {
	var index struct {
		Current yaml.Node   `yaml:"current"`
		Items   []yaml.Node `yaml:"items"`
	}
	if yaml.Unmarshal(data, &index) != nil {
		return nil
	}
	current := map[string]bool{}
	var one string
	var many []string
	if index.Current.Decode(&one) == nil {
		current[one] = one != ""
	} else if index.Current.Decode(&many) == nil {
		for _, uid := range many {
			current[uid] = uid != ""
		}
	}
	out := []nyanpasuProfile{}
	for i, node := range index.Items {
		if i >= 64 {
			break
		}
		var item struct {
			UID    string `yaml:"uid"`
			Name   string `yaml:"name"`
			Type   string `yaml:"type"`
			File   string `yaml:"file"`
			URL    string `yaml:"url"`
			Config struct {
				Type   string `yaml:"type"`
				Source struct {
					Type    string `yaml:"type"`
					File    string `yaml:"file"`
					URL     string `yaml:"url"`
					Binding struct {
						File string `yaml:"file"`
					} `yaml:"binding"`
				} `yaml:"source"`
			} `yaml:"config"`
		}
		// Script items carry custom YAML tags such as "!script javascript".
		if node.Decode(&item) != nil {
			continue
		}
		file, remote := "", ""
		switch {
		case item.Type == "remote":
			file, remote = item.File, item.URL
		case item.Type == "local":
			file = item.File
		case item.Type == "config" && item.Config.Type == "file" && item.Config.Source.Type == "remote":
			file, remote = item.Config.Source.File, item.Config.Source.URL
		case item.Type == "config" && item.Config.Type == "file" && item.Config.Source.Type == "local":
			file = item.Config.Source.Binding.File
		default:
			continue
		}
		if file == "" || filepath.Base(file) != file || strings.ContainsAny(file, `/\\`) {
			continue
		}
		name := item.Name
		if name == "" {
			name = "已保存的配置"
		}
		out = append(out, nyanpasuProfile{file: file, name: name, url: remote, current: current[item.UID]})
	}
	return out
}

func signConfig(c *Config, identity string, key []byte, extra ...string) {
	h := sha256.Sum256([]byte(identity))
	c.SourceID = "local-" + hex.EncodeToString(h[:12])
	mac := hmac.New(sha256.New, key)
	for _, value := range []string{identity, c.Client, c.Name, c.URL, c.Warning, c.Error, fmt.Sprint(c.Current), string(c.Data)} {
		mac.Write([]byte(value))
		mac.Write([]byte{0})
	}
	for _, value := range extra {
		mac.Write([]byte(value))
		mac.Write([]byte{0})
	}
	c.ID = hex.EncodeToString(mac.Sum(nil))
}

// isAgeEncrypted inspects only the public age format header; Cage never
// decrypts client profiles or reads private keys.
func isAgeEncrypted(data []byte) bool {
	trimmed := bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if bytes.HasPrefix(trimmed, []byte("age-encryption.org/v1\n")) ||
		bytes.HasPrefix(trimmed, []byte("age-encryption.org/v1\r\n")) {
		return true
	}
	trimmed = bytes.TrimLeft(trimmed, " \t\r\n")
	return bytes.HasPrefix(trimmed, []byte("-----BEGIN AGE ENCRYPTED FILE-----\n")) ||
		bytes.HasPrefix(trimmed, []byte("-----BEGIN AGE ENCRYPTED FILE-----\r\n"))
}

func listWithin(root, subdir string) ([]os.DirEntry, error) {
	if subdir == "" {
		return os.ReadDir(root)
	}
	if !filepath.IsLocal(subdir) {
		return nil, errors.New("配置路径不受支持")
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	f, err := dir.Open(subdir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.IsDir() {
		return nil, errors.New("配置目录不可读取")
	}
	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	return entries, nil
}

func displayName(name string) string {
	name = strings.TrimSpace(name)
	for len(name) > 60 {
		r := []rune(name)
		name = string(r[:len(r)-1])
	}
	if name == "" {
		name = "本机配置"
	}
	return name
}

func readWithin(root, relative string) ([]byte, error) {
	if !filepath.IsLocal(relative) {
		return nil, errors.New("配置路径不受支持")
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	f, err := dir.Open(relative)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > managedproxy.MaxImport {
		return nil, errors.New("配置文件不可读取或超过 8 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, managedproxy.MaxImport+1))
	if err != nil || len(data) > managedproxy.MaxImport {
		return nil, errors.New("无法完整读取配置文件")
	}
	return data, nil
}

func nodeData(root, providers string, data []byte) ([]byte, string, error) {
	var input struct {
		DNS       yaml.Node        `yaml:"dns"`
		Proxies   []map[string]any `yaml:"proxies"`
		Payload   []map[string]any `yaml:"payload"`
		Providers map[string]struct {
			Path string `yaml:"path"`
		} `yaml:"proxy-providers"`
	}
	if yaml.Unmarshal(data, &input) != nil {
		return nil, "", errors.New("配置格式无效")
	}
	var nodeDNS []string
	if input.DNS.Kind == yaml.MappingNode {
		var dnsCfg struct {
			Servers []string `yaml:"proxy-server-nameserver"`
		}
		if input.DNS.Decode(&dnsCfg) == nil {
			nodeDNS = managedproxy.FilterNodeDNS(dnsCfg.Servers)
		}
	}
	if len(input.Proxies) == 0 {
		input.Proxies = input.Payload
	}
	missing, total := 0, len(data)
	names := make([]string, 0, len(input.Providers))
	for name := range input.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		provider := input.Providers[name]
		if i >= 32 || !filepath.IsLocal(provider.Path) {
			missing++
			continue
		}
		cached, err := readWithin(root, filepath.Join(providers, provider.Path))
		total += len(cached)
		if err != nil || total > managedproxy.MaxImport {
			missing++
			continue
		}
		var nodes struct {
			Proxies []map[string]any `yaml:"proxies"`
			Payload []map[string]any `yaml:"payload"`
		}
		if yaml.Unmarshal(cached, &nodes) != nil {
			missing++
			continue
		}
		if len(nodes.Proxies) == 0 {
			nodes.Proxies = nodes.Payload
		}
		if len(nodes.Proxies) == 0 {
			missing++
			continue
		}
		input.Proxies = append(input.Proxies, nodes.Proxies...)
	}
	if len(input.Proxies) == 0 {
		var err error
		input.Proxies, err = shadowsocksNodes(data)
		if err != nil {
			return nil, "", err
		}
	}
	// Keep identical nodes found in both the runtime and provider cache once.
	seen := map[string]bool{}
	unique := []map[string]any{}
	for _, n := range input.Proxies {
		b, err := json.Marshal(n)
		if err != nil {
			return nil, "", err
		}
		if !seen[string(b)] {
			seen[string(b)] = true
			unique = append(unique, n)
		}
	}
	output := map[string]any{"proxies": unique}
	if len(nodeDNS) > 0 {
		output["dns"] = map[string]any{"proxy-server-nameserver": nodeDNS}
	}
	result, err := json.Marshal(output)
	warning := ""
	if missing > 0 {
		warning = fmt.Sprintf("有 %d 份节点列表未能从本机缓存读取，这些节点不会导入。可先在原软件中更新配置，再重新检测。", missing)
	}
	return result, warning, err
}

func shadowsocksNodes(data []byte) ([]map[string]any, error) {
	type server struct {
		Server           string `json:"server"`
		Port             int    `json:"server_port"`
		Password         string `json:"password"`
		Method           string `json:"method"`
		Protocol         string `json:"protocol"`
		ProtocolParam    string `json:"protocolparam"`
		ProtocolParamAlt string `json:"protocol_param"`
		Obfs             string `json:"obfs"`
		ObfsParam        string `json:"obfsparam"`
		ObfsParamAlt     string `json:"obfs_param"`
		Remarks          string `json:"remarks"`
		RemarksBase64    string `json:"remarks_base64"`
		Plugin           string `json:"plugin"`
	}
	var config struct {
		Configs []server `json:"configs"`
	}
	if json.Unmarshal(data, &config) != nil {
		return nil, errors.New("不是受支持的节点配置")
	}
	if len(config.Configs) == 0 {
		var single server
		if json.Unmarshal(data, &single) != nil || single.Server == "" {
			return nil, errors.New("没有节点")
		}
		config.Configs = []server{single}
	}
	if len(config.Configs) > 3000 {
		return nil, errors.New("节点过多")
	}
	out := []map[string]any{}
	names := map[string]int{}
	for i, n := range config.Configs {
		if n.Server == "" || n.Port < 1 || n.Port > 65535 || n.Method == "" || n.Plugin != "" {
			return nil, errors.New("节点不完整或插件暂不支持")
		}
		name := n.Remarks
		if name == "" && n.RemarksBase64 != "" {
			if raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(n.RemarksBase64, "=")); err == nil {
				name = string(raw)
			}
		}
		if name == "" {
			name = fmt.Sprintf("节点 %d", i+1)
		}
		names[name]++
		if names[name] > 1 {
			name = fmt.Sprintf("%s (%d)", name, names[name])
		}
		node := map[string]any{"name": name, "type": "ss", "server": n.Server, "port": n.Port, "cipher": n.Method, "password": n.Password}
		if n.Protocol != "" {
			if n.Obfs == "" {
				return nil, errors.New("SSR 混淆字段缺失")
			}
			node["type"], node["protocol"], node["obfs"] = "ssr", n.Protocol, n.Obfs
			if n.ProtocolParam == "" {
				n.ProtocolParam = n.ProtocolParamAlt
			}
			if n.ObfsParam == "" {
				n.ObfsParam = n.ObfsParamAlt
			}
			node["protocol-param"], node["obfs-param"] = n.ProtocolParam, n.ObfsParam
		}
		out = append(out, node)
	}
	return out, nil
}
