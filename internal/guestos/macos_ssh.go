package guestos

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Lima chooses a new loopback port on each boot. Read only that number from
// its generated config; use the provisioned VM key, user and identity file.
func MacOSSSHPort(config []byte) (int, error) {
	var port int
	for _, line := range strings.Split(string(config), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.EqualFold(fields[0], "port") {
			continue
		}
		if len(fields) != 2 || port != 0 {
			return 0, errors.New("macOS 客机 SSH 端口配置无效")
		}
		value, err := strconv.Atoi(fields[1])
		if err != nil || value < 1 || value > 65535 {
			return 0, errors.New("macOS 客机 SSH 端口配置无效")
		}
		port = value
	}
	if port == 0 {
		return 0, errors.New("macOS 客机 SSH 端口尚未就绪")
	}
	return port, nil
}

func macOSSSHBaseArgs(profile, user string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(profile, "state", "lima", "mac", "ssh.config"))
	if err != nil {
		return nil, err
	}
	port, err := MacOSSSHPort(data)
	if err != nil {
		return nil, err
	}
	known := filepath.Join(profile, "macos-known-hosts")
	// ssh -o parses each option as a config line; quote paths with spaces.
	// Lima's ssh.config is not used: the port is reread each call because it
	// changes between boots.
	return []string{"-q", "-e", "none", "-F", os.DevNull, "-p", strconv.Itoa(port),
		"-l", user, "-i", filepath.Join(profile, "state", "lima", "_config", "user"),
		"-o", "Hostname=127.0.0.1", "-o", "HostKeyAlias=cage-macos",
		"-o", "StrictHostKeyChecking=yes", "-o", "HostKeyAlgorithms=ssh-ed25519",
		"-o", "UserKnownHostsFile=" + strconv.Quote(known), "-o", "GlobalKnownHostsFile=" + os.DevNull,
		"-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none",
		"-o", "ForwardAgent=no", "-o", "ControlPath=none",
		"-o", "ConnectTimeout=3", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2"}, nil
}

func MacOSSSHArgs(profile, user string) ([]string, error) {
	base, err := macOSSSHBaseArgs(profile, user)
	if err != nil {
		return nil, err
	}
	return append(append([]string{}, base...), "-o", "ClearAllForwardings=yes"), nil
}

// MacOSScreenSharingSSHArgs builds a loopback-only local forward to the guest
// Screen Sharing port. The host port must already be reserved. Do not reuse
// MacOSSSHArgs: that set clears all forwards.
func MacOSScreenSharingSSHArgs(profile, user string, hostPort int) ([]string, error) {
	if hostPort < 1 || hostPort > 65535 {
		return nil, errors.New("本机屏幕共享转发端口无效")
	}
	base, err := macOSSSHBaseArgs(profile, user)
	if err != nil {
		return nil, err
	}
	return append(append([]string{}, base...),
		"-N",
		"-o", "ExitOnForwardFailure=yes",
		"-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:5900", hostPort)), nil
}

// MacOSProvisionUser reads Lima's installation identity, including older VMs.
// Only these two dedicated identities may run privileged maintenance.
func MacOSProvisionUser(profile string) (string, error) {
	data, err := os.ReadFile(filepath.Join(profile, "state", "lima", "mac", "ssh.config"))
	if err != nil {
		return "", err
	}
	var user string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.EqualFold(fields[0], "User") {
			continue
		}
		if len(fields) != 2 || user != "" || (fields[1] != "cage" && fields[1] != "cageprovision") {
			return "", errors.New("macOS 客机安装账户配置无效")
		}
		user = fields[1]
	}
	if user == "" {
		return "", errors.New("macOS 客机安装账户尚未就绪")
	}
	return user, nil
}
