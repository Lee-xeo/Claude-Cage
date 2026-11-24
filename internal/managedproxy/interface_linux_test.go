package managedproxy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxPhysicalRouteSkipsTUNAndUsesLowestMetric(t *testing.T) {
	const routes = `Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT
tun0 00000000 00000000 0001 0 0 0 00000000 0 0 0
ens5 00000000 0100000A 0003 0 0 100 00000000 0 0 0
eth1 00000000 0100000A 0003 0 0 20 00000000 0 0 0
eth2 00000000 0100000A 0203 0 0 1 00000000 0 0 0
eth3 00000000 0100000A 0002 0 0 1 00000000 0 0 0
eth4 00000000 0100000A 0003 0 0 1 0000FFFF 0 0 0
`
	for _, tc := range []struct {
		name   string
		usable map[string]bool
		want   string
	}{
		{"physical metric", map[string]bool{"ens5": true, "eth1": true, "eth2": true, "eth3": true, "eth4": true}, "eth1"},
		{"cloud virtio", map[string]bool{"ens5": true}, "ens5"},
		{"disconnected physical interfaces", map[string]bool{}, ""},
		{"reject and inactive routes", map[string]bool{"eth2": true, "eth3": true, "eth4": true}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectLinuxPhysicalInterface(routes, tc.usable)
			if got != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// 用临时目录模拟 /sys/class/net：device 目录表示带父设备的网卡，
// lower_<name> 链接指向下层网卡，与内核的呈现方式一致。
func TestIsPhysicalLinkFollowsLowerLinks(t *testing.T) {
	root := t.TempDir()
	link := func(name string, device bool, lowers ...string) {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if device {
			if err := os.Mkdir(filepath.Join(dir, "device"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		for _, lower := range lowers {
			if err := os.Symlink(filepath.Join("..", lower), filepath.Join(dir, "lower_"+lower)); err != nil {
				t.Fatal(err)
			}
		}
	}
	link("eth0", true)
	link("eth1", true)
	link("eth2", true)
	link("bond0", false, "eth0", "eth1")
	link("bond1", false)
	link("bond0.100", false, "bond0")
	link("br0", false, "eth2", "vnet0")
	link("vnet0", false)
	link("veth1a2b", false)
	link("docker0", false, "veth1a2b")
	link("tun0", false)
	link("loopa", false, "loopb")
	link("loopb", false, "loopa")

	for name, want := range map[string]bool{
		"eth0":      true,
		"bond0":     true,
		"bond1":     false,
		"bond0.100": true,
		"br0":       true,
		"docker0":   false,
		"veth1a2b":  false,
		"tun0":      false,
		"loopa":     false,
		"missing":   false,
	} {
		if got := isPhysicalLink(root, name); got != want {
			t.Errorf("isPhysicalLink(%q) = %v, want %v", name, got, want)
		}
	}
}
