package guestos

import (
	"context"
	"errors"
	"golang.org/x/mod/semver"
	"os/exec"
	"strings"
)

func probeMacHost(ctx context.Context) (MacRestore, error) {
	version, err := exec.CommandContext(ctx, "/usr/bin/sw_vers", "-productVersion").Output()
	if err != nil || !semver.IsValid("v"+strings.TrimSpace(string(version))) || semver.Compare("v"+strings.TrimSpace(string(version)), "v15.4") < 0 {
		return MacRestore{}, errors.New("Intel Mac 本机客机需要 macOS 15.4 或更新版本，以支持工作台项目共享")
	}
	data, err := exec.CommandContext(ctx, "/usr/sbin/sysctl", "-n", "kern.hv_support").Output()
	if err != nil || strings.TrimSpace(string(data)) != "1" {
		return MacRestore{}, errors.New("Intel Mac 的 Hypervisor.framework 不可用")
	}
	return MacRestore{MinimumCPUs: 2, MinimumMemoryBytes: 4 << 30}, nil
}
