package managedproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

var (
	runDarwinNetworksetup = func(ctx context.Context) ([]byte, error) {
		return exec.CommandContext(ctx, "/usr/sbin/networksetup", "-listallhardwareports").Output()
	}
	runDarwinRoute = func(ctx context.Context) ([]byte, error) {
		return exec.CommandContext(ctx, "/sbin/route", "-n", "get", "default").Output()
	}
	runDarwinScutilNWI = func(ctx context.Context) ([]byte, error) {
		return exec.CommandContext(ctx, "/usr/sbin/scutil", "--nwi").Output()
	}
	listNetInterfaces = net.Interfaces
	netInterfaceAddrs = func(iface net.Interface) ([]net.Addr, error) {
		return iface.Addrs()
	}
)

// PhysicalInterface uses the system's preferred reachable IPv4 hardware port.
// A route through utun is not a physical uplink, even when it is the default.
func PhysicalInterface(ctx context.Context) (string, error) {
	if runtime.GOOS == "windows" {
		return windowsPhysicalInterface(ctx)
	}
	if runtime.GOOS == "linux" {
		return linuxPhysicalInterface(ctx)
	}
	if runtime.GOOS != "darwin" {
		return "", errors.New("物理网卡识别当前支持 macOS")
	}
	return darwinPhysicalInterface(ctx)
}

func darwinPhysicalInterface(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	hardware, err := runDarwinNetworksetup(ctx)
	if err != nil {
		return "", errors.New("无法读取物理网卡，请检查主机网络后重试")
	}
	route, _ := runDarwinRoute(ctx)
	nwi, err := runDarwinScutilNWI(ctx)
	if err != nil {
		return "", errors.New("无法读取系统网络状态，请检查主机网络或重启 Cage 后重试")
	}
	usable := map[string]bool{}
	interfaces, err := listNetInterfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && ip.To4() != nil && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified() {
				usable[iface.Name] = true
			}
		}
	}
	return selectPhysicalInterface(string(route), string(hardware), string(nwi), usable)
}

// macOS also exposes packet tunnels, bridges, VM links, AWDL and other
// service interfaces through network tools. They can carry an IPv4 address,
// but they are not a physical uplink and must not become the proxy's binding.
var macVirtualInterface = regexp.MustCompile(`(?i)^(?:utun|bridge|vmen|vmenet|vmnet|tap|lima|awdl|llw|p2p|gif|stf|anpi|ap)[0-9]+$`)

func isVirtualMACInterface(name string) bool {
	return macVirtualInterface.MatchString(name)
}

func selectPhysicalInterface(route, hardware, nwi string, usable map[string]bool) (string, error) {
	if strings.TrimSpace(nwi) == "" {
		return "", errors.New("无法读取系统网络状态，请检查主机网络或重启 Cage 后重试")
	}
	physical := map[string]bool{}
	for _, line := range strings.Split(hardware, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "Device:" && usable[fields[1]] && !isVirtualMACInterface(fields[1]) {
			physical[fields[1]] = true
		}
	}
	// scutil lists interfaces in network priority order. Only its reachable
	// IPv4 section is relevant to the proxy's IPv4 infrastructure transport.
	inIPv4, candidate := false, ""
	reachable := []string{}
	for _, line := range strings.Split(nwi, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "IPv4 network interface information") {
			inIPv4 = true
		} else if strings.HasPrefix(line, "IPv6 network interface information") {
			break
		}
		if !inIPv4 {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[1] == ":" && fields[2] == "flags" {
			candidate = fields[0]
		}
		if strings.HasPrefix(line, "reach") && strings.Contains(line, "(Reachable)") && physical[candidate] {
			reachable = append(reachable, candidate)
		}
	}
	for _, line := range strings.Split(route, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "interface:" {
			for _, name := range reachable {
				if fields[1] == name {
					return name, nil
				}
			}
		}
	}
	if len(reachable) > 0 {
		return reachable[0], nil
	}
	return "", errors.New("未找到可用的物理 IPv4 网卡；Cage 外网保持关闭，请连接网络后重试")
}

// BindInterface changes only the core's host transport. MATCH routing and
// proxy credentials remain intact; this does not create a guest direct route.
// Direct proxy nodes whose literal server IP is routed via Tailscale or a
// directly attached private subnet on another host interface get a per-node
// interface-name so they follow the host's route to that node while DNS and
// public proxy nodes remain pinned to the physical uplink.
func BindInterface(data []byte, name string) ([]byte, error) {
	if name == "" {
		return nil, errors.New("缺少物理网卡，不能启动代理")
	}
	var config map[string]any
	if err := yaml.Unmarshal(data, &config); err != nil || config == nil {
		return nil, errors.New("代理运行配置无效")
	}
	config["interface-name"] = name
	// Older imports may carry a node-level binding, which overrides the global.
	// This pass writes the runtime copy of every node, so nodes imported by
	// earlier versions also get their SSH host key algorithms here.
	var resolver *nodeInterfaceResolver
	if nodes, ok := config["proxies"].([]any); ok {
		for _, item := range nodes {
			if node, ok := item.(map[string]any); ok {
				server, _ := node["server"].(string)
				dialerProxy, _ := node["dialer-proxy"].(string)
				if routed := resolveNodeInterface(server, dialerProxy, name, &resolver); routed != "" {
					node["interface-name"] = routed
				} else if _, bound := node["interface-name"]; bound {
					node["interface-name"] = name
				}
				pinSSHHostKeyAlgorithms(node)
			}
		}
	}
	return yaml.Marshal(config)
}

func BoundInterface(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var config struct {
		Interface string `yaml:"interface-name"`
		Proxies   []struct {
			Server string `yaml:"server"`
		} `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return "", err
	}
	local := newLocalServers()
	for _, proxy := range config.Proxies {
		if local.contains(proxy.Server) {
			return "", ErrLocalProxyNode
		}
	}
	return config.Interface, nil
}

// BindingMatches reports whether the runtime config at path matches both the
// expected physical uplink and any per-node host route interfaces (such as
// Tailscale or directly attached private subnets) for direct proxy nodes.
func BindingMatches(path, physical string) bool {
	if physical == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var config struct {
		Interface string `yaml:"interface-name"`
		Proxies   []struct {
			Server      string `yaml:"server"`
			DialerProxy string `yaml:"dialer-proxy"`
			Interface   string `yaml:"interface-name"`
		} `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil || config.Interface != physical {
		return false
	}
	local := newLocalServers()
	var resolver *nodeInterfaceResolver
	for _, proxy := range config.Proxies {
		if local.contains(proxy.Server) {
			return false
		}
		want := resolveNodeInterface(proxy.Server, proxy.DialerProxy, physical, &resolver)
		have := proxy.Interface
		if have == physical {
			have = ""
		}
		if have != want {
			return false
		}
	}
	return true
}

// InterfaceAvailable reports whether the named host interface currently exists,
// is up and running (carrier active), is not loopback, and holds at least one
// routable IPv4 address. It uses in-process OS calls only (no subprocesses).
func InterfaceAvailable(name string) bool {
	if name == "" {
		return false
	}
	interfaces, err := listNetInterfaces()
	if err != nil {
		return false
	}
	for _, iface := range interfaces {
		if iface.Name != name || iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagRunning == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, err := netInterfaceAddrs(iface)
		if err != nil {
			return false
		}
		for _, address := range addresses {
			ip, _, err := net.ParseCIDR(address.String())
			if err == nil && ip.To4() != nil && !ip.IsLinkLocalUnicast() && !ip.IsLoopback() && !ip.IsUnspecified() {
				return true
			}
		}
		return false
	}
	return false
}

// StaleInterfaceError reports that the proxy core's runtime config is bound to
// an interface or per-node host route that no longer matches the host's current
// network uplink.
type StaleInterfaceError struct {
	Bound   string
	Current string
}

func (e *StaleInterfaceError) Error() string {
	if e.Bound != "" && e.Current != "" && e.Bound != e.Current {
		return fmt.Sprintf("代理核心当前绑定的出口网卡（%s）已失效或不再是默认物理网卡（当前为 %s）；请重启代理核心以重新绑定", e.Bound, e.Current)
	}
	return "代理核心当前绑定的出口网卡或节点路由接口已变化；请重启代理核心以重新绑定"
}

// CheckInterfaceBinding verifies that the runtime config at configPath still
// matches the host's current physical uplink and per-node host routes.
func CheckInterfaceBinding(ctx context.Context, configPath string) error {
	return checkInterfaceBindingWith(ctx, configPath, PhysicalInterface, BindingMatches)
}

func checkInterfaceBindingWith(
	ctx context.Context,
	configPath string,
	physical func(context.Context) (string, error),
	matches func(string, string) bool,
) error {
	bound, err := BoundInterface(configPath)
	if err != nil {
		return err
	}
	current, err := physical(ctx)
	if err != nil {
		return err
	}
	if !matches(configPath, current) {
		return &StaleInterfaceError{Bound: bound, Current: current}
	}
	return nil
}
