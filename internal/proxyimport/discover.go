// Package proxyimport reads node configurations from known local client data
// directories. It never starts a client, runs its scripts, or changes its files.
package proxyimport

import (
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

type Location struct {
	Client, Directory string
	Verge             bool
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
	out := []Location{
		{"FlClash", filepath.Join(dataHome, "com.follow.clash"), false},
		{"FlClash", filepath.Join(dataHome, "com.follow.clash", "profiles"), false},
		{"Clash Verge Rev", filepath.Join(dataHome, "io.github.clash-verge-rev.clash-verge-rev"), true},
		{"Clash Verge", filepath.Join(configHome, "clash-verge"), true},
		{"Clash / ClashX", filepath.Join(configHome, "clash"), false},
		{"Mihomo", filepath.Join(configHome, "mihomo"), false},
		{"ShadowsocksR", filepath.Join(configHome, "shadowsocksr"), false},
		{"Shadowsocks", filepath.Join(configHome, "shadowsocks"), false},
	}
	if platform == "darwin" {
		out = append(out, Location{"ShadowsocksX-NG", filepath.Join(home, ".ShadowsocksX-NG"), false})
	}
	if platform == "windows" {
		out = append(out,
			Location{"FlClash", filepath.Join(dataHome, "com.follow", "FlClash"), false},
			Location{"FlClash", filepath.Join(dataHome, "com.follow", "FlClash", "profiles"), false},
			Location{"Clash / Mihomo", filepath.Join(home, ".config", "clash"), false},
			Location{"Clash Verge", filepath.Join(home, ".config", "clash-verge"), true},
			Location{"ShadowsocksR", filepath.Join(dataHome, "ShadowsocksR"), false})
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
		add := func(file, name, remote string, current bool) {
			data, err := readWithin(root, file)
			if os.IsNotExist(err) {
				return
			}
			c := Config{Client: loc.Client, Name: displayName(name), Current: current}
			identity := filepath.Join(root, file)
			if err != nil {
				c.Error = "无法读取这份配置，请检查文件权限或改为手动导入"
			} else {
				c.Data, c.Warning, err = nodeData(root, data)
				if err != nil {
					c.Error = "这份配置没有可导入的节点，或格式暂不支持；请从原软件导出 Clash/Mihomo 配置"
				}
			}
			if c.Error == "" {
				var preview managedproxy.Store
				if err := preview.Import(c.Name, "", c.Data, "preview", 0, ""); err != nil {
					c.Error = "这份配置没有可导入的节点；请从原软件导出含节点的 Clash/Mihomo 配置"
				} else {
					c.Nodes = len(preview.Sources[0].Nodes)
					c.Skipped = len(preview.Sources[0].Skipped)
				}
			}
			if u, err := url.Parse(remote); err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == "" {
				c.URL = remote
				c.Refreshable = true
			}
			h := sha256.Sum256([]byte(identity))
			c.SourceID = "local-" + hex.EncodeToString(h[:12])
			mac := hmac.New(sha256.New, key)
			for _, value := range []string{identity, c.Name, c.URL, c.Warning, c.Error, fmt.Sprint(c.Current), string(c.Data)} {
				mac.Write([]byte(value))
				mac.Write([]byte{0})
			}
			c.ID = hex.EncodeToString(mac.Sum(nil))
			out = append(out, c)
		}
		if loc.Verge {
			// Prefer the latest generated runtime file, which includes local
			// overrides; never execute profile enhancement scripts ourselves.
			for _, file := range []string{"clash-verge.yaml", "config.yaml"} {
				if _, err := os.Stat(filepath.Join(root, file)); !os.IsNotExist(err) {
					add(file, "当前生效配置", "", true)
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
				add(filepath.Join("profiles", item.File), name, item.URL, item.UID == index.Current)
			}
		} else {
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
				add(file.Name(), file.Name(), "", file.Name() == "config.yaml" || file.Name() == "config.json" || file.Name() == "gui-config.json")
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

func nodeData(root string, data []byte) ([]byte, string, error) {
	var input struct {
		Proxies   []map[string]any `yaml:"proxies"`
		Payload   []map[string]any `yaml:"payload"`
		Providers map[string]struct {
			Path string `yaml:"path"`
		} `yaml:"proxy-providers"`
	}
	if yaml.Unmarshal(data, &input) != nil {
		return nil, "", errors.New("配置格式无效")
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
		if i >= 32 || provider.Path == "" {
			missing++
			continue
		}
		cached, err := readWithin(root, provider.Path)
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
	result, err := json.Marshal(map[string]any{"proxies": unique})
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
