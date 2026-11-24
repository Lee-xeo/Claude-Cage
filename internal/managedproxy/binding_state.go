package managedproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

const maxBindingStateSize = 64 << 10

type interfaceBindingState struct {
	ConfigSHA256 string   `json:"config_sha256"`
	Name         string   `json:"name"`
	Index        int      `json:"index,omitempty"`
	IPv4Addrs    []string `json:"ipv4_addrs,omitempty"`
}

var (
	snapshotInterfaceState = systemInterfaceSnapshot
	currentPhysicalUplink  = PhysicalInterface
)

func bindingStatePath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "interface-binding.json")
}

func saveInterfaceBindingState(configPath string, configData []byte, name string) {
	idx, addrs, ok := snapshotInterfaceState(name)
	if !ok {
		_ = os.Remove(bindingStatePath(configPath))
		return
	}
	sum := sha256.Sum256(configData)
	state := interfaceBindingState{
		ConfigSHA256: hex.EncodeToString(sum[:]),
		Name:         name,
		Index:        idx,
		IPv4Addrs:    addrs,
	}
	b, err := json.Marshal(state)
	if err != nil {
		return
	}
	_ = Write(bindingStatePath(configPath), b)
}

func removeInterfaceBindingState(profile string) {
	_ = os.Remove(bindingStatePath(filepath.Join(Dir(profile), "config.yaml")))
}

func interfaceBindingMatches(configPath string, configData []byte, physical string) (matched bool, addressChanged bool) {
	data, err := os.ReadFile(bindingStatePath(configPath))
	if err != nil {
		if os.IsNotExist(err) {
			return true, false
		}
		return false, true
	}
	var state interfaceBindingState
	if len(data) > maxBindingStateSize || json.Unmarshal(data, &state) != nil || state.ConfigSHA256 == "" || state.Name == "" {
		return false, true
	}
	sum := sha256.Sum256(configData)
	if state.ConfigSHA256 != hex.EncodeToString(sum[:]) {
		return true, false
	}
	if state.Name != physical {
		return false, false
	}
	idx, addrs, ok := snapshotInterfaceState(physical)
	if !ok || (state.Index != 0 && idx != state.Index) || !slices.Equal(state.IPv4Addrs, addrs) {
		return false, true
	}
	return true, false
}

// BindingAddressChanged reports whether configPath has a matching runtime
// binding state whose bound interface name matches physical, yet the host
// interface's IPv4 addresses, interface index, or up status has changed.
func BindingAddressChanged(path, physical string) bool {
	if physical == "" {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	_, changed := interfaceBindingMatches(path, data, physical)
	return changed
}

// BindingStatus describes whether the runtime proxy configuration still matches
// the host's active physical uplink, interface address/index, and per-node routes.
type BindingStatus struct {
	Bound     string
	Current   string
	Reason    string // "", "missing_config", "local_node", "uplink_unavailable", "interface_changed", "address_changed", "route_changed"
	UplinkErr error
}

// Drifted reports whether the runtime binding no longer matches the host uplink.
func (s BindingStatus) Drifted() bool {
	return s.Reason != ""
}

// Message returns a user-facing diagnostic description without exposing secrets or host paths.
func (s BindingStatus) Message() string {
	switch s.Reason {
	case "":
		return ""
	case "missing_config":
		return "代理核心运行配置缺失，请运行 cage proxy-start 或重新应用代理节点"
	case "local_node":
		return ErrLocalProxyNode.Error()
	case "uplink_unavailable":
		return "代理核心绑定的出口网卡已失效，且当前未找到可用的物理 IPv4 网卡；请恢复网络连接后重试"
	case "interface_changed":
		return fmt.Sprintf("代理核心绑定的出口网卡（%s）已失效或不再是当前默认物理网卡（当前为 %s）；请运行 cage proxy-start 或在工作台重启代理核心重新绑定", s.Bound, s.Current)
	case "address_changed":
		return fmt.Sprintf("代理核心绑定的物理网卡（%s）IP 地址或接口状态已变化；请运行 cage proxy-start 或在工作台重启代理核心重新绑定", s.Bound)
	case "route_changed":
		return "代理节点的路由接口已变化；请运行 cage proxy-start 或在工作台重启代理核心重新绑定"
	default:
		return "代理核心绑定状态已变化，请运行 cage proxy-start 或重启代理核心重新绑定"
	}
}

// CheckRuntimeBinding inspects the saved runtime config, interface snapshot,
// current physical uplink, and per-node host routes for drift.
func CheckRuntimeBinding(ctx context.Context, profile string) BindingStatus {
	configPath := filepath.Join(Dir(profile), "config.yaml")
	bound, err := BoundInterface(configPath)
	if errors.Is(err, ErrLocalProxyNode) {
		return BindingStatus{Reason: "local_node"}
	}
	if err != nil || bound == "" {
		return BindingStatus{Reason: "missing_config"}
	}
	current, err := currentPhysicalUplink(ctx)
	if err != nil {
		return BindingStatus{Bound: bound, Reason: "uplink_unavailable", UplinkErr: err}
	}
	if bound != current {
		return BindingStatus{Bound: bound, Current: current, Reason: "interface_changed"}
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return BindingStatus{Reason: "missing_config"}
	}
	if matched, addrChanged := interfaceBindingMatches(configPath, data, current); !matched {
		if addrChanged {
			return BindingStatus{Bound: bound, Current: current, Reason: "address_changed"}
		}
		return BindingStatus{Bound: bound, Current: current, Reason: "interface_changed"}
	}
	if !bindingConfigMatches(data, current) {
		return BindingStatus{Bound: bound, Current: current, Reason: "route_changed"}
	}
	return BindingStatus{Bound: bound, Current: current}
}
