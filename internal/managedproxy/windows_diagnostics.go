package managedproxy

type windowsDiagnosticAdapter struct {
	index             uint32
	name              string
	description       string
	metric            uint32
	hardware          bool
	ipv4              bool
	mac               []byte
	forwardingEnabled bool
}

type windowsDiagnosticRoute struct {
	interfaceIndex uint32
	prefixLen      uint8
	prefix         [4]byte
	loopback       bool
}

type windowsNetworkConflict struct {
	ActiveHostTUN      bool
	PhysicalForwarding bool
}

func detectWindowsHostTUNConflict(adapters []windowsDiagnosticAdapter, routes []windowsDiagnosticRoute, boundName string) windowsNetworkConflict {
	adapterInfos := make([]windowsAdapterInfo, 0, len(adapters))
	nonUplinkVirtual := make(map[uint32]bool, len(adapters))
	var physicalForwarding bool
	for _, a := range adapters {
		adapterInfos = append(adapterInfos, windowsAdapterInfo{
			index:    a.index,
			name:     a.name,
			metric:   a.metric,
			hardware: a.hardware,
			ipv4:     a.ipv4,
			mac:      a.mac,
		})
		if boundName != "" && a.name == boundName && a.forwardingEnabled {
			physicalForwarding = true
		}
	}
	uplinks := windowsUplinkAdapters(adapterInfos)
	for _, a := range adapters {
		if a.index == 0 || a.hardware {
			continue
		}
		if _, isUplink := uplinks[a.index]; !isUplink {
			nonUplinkVirtual[a.index] = true
		}
	}
	hasLowHalf := make(map[uint32]bool)
	hasHighHalf := make(map[uint32]bool)
	var activeTUN bool
	for _, r := range routes {
		if r.loopback || !nonUplinkVirtual[r.interfaceIndex] {
			continue
		}
		switch {
		case r.prefixLen == 0 && r.prefix == [4]byte{}:
			activeTUN = true
		case r.prefixLen == 1 && r.prefix == [4]byte{0, 0, 0, 0}:
			hasLowHalf[r.interfaceIndex] = true
			if hasHighHalf[r.interfaceIndex] {
				activeTUN = true
			}
		case r.prefixLen == 1 && r.prefix == [4]byte{128, 0, 0, 0}:
			hasHighHalf[r.interfaceIndex] = true
			if hasLowHalf[r.interfaceIndex] {
				activeTUN = true
			}
		}
		if activeTUN {
			break
		}
	}
	return windowsNetworkConflict{
		ActiveHostTUN:      activeTUN,
		PhysicalForwarding: physicalForwarding,
	}
}

func (c windowsNetworkConflict) FormatHint() string {
	if !c.ActiveHostTUN {
		return ""
	}
	if c.PhysicalForwarding {
		return "检测到宿主机存在活跃的 TUN/VPN 默认路由，且当前绑定的物理网卡开启了 IPv4 转发，可能导致 Windows 下绑定物理网卡的 TCP 节点拨号超时（UDP 类节点可能不受影响）。建议关闭物理网卡的 IPv4 转发或移动热点、暂时关闭宿主 TUN 模式，或在 Cage 中改用 Hysteria2/TUIC 等节点或将其作为前置中转。"
	}
	return "检测到宿主机存在活跃的 TUN/VPN 默认路由（如 Clash TUN）。Cage 为隔离外网会强制绑定物理网卡绕过宿主 TUN，在部分 Windows 环境下可能与宿主 TUN 规则冲突导致 TCP 类节点超时（而 Hysteria2/TUIC 等 UDP 节点正常）。建议暂时关闭宿主 TUN 模式、检查宿主 TUN 路由与防火墙设置，或在 Cage 中改用 UDP 协议节点或将其作为前置中转。"
}
