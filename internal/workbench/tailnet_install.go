package workbench

import (
	"al.essio.dev/pkg/shellescape"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"github.com/sylvinus/agent-vm/internal/tailnetassets"
	"io"
	"strings"
	"time"
)

// Stage beside the installed executable so a running node can finish on its old
// inode. Identity and user data live elsewhere and are never part of installation.
// The program was once installed under an older name; that copy and its sudo
// rule go once the new one is in place, and the node writes its SSH settings
// with its own path when it starts again.
const tailnetInstallScript = `set -eu; tmp=$(mktemp /tmp/tailnet.XXXXXX); stage=; trap 'rm -f "$tmp"; if test -n "$stage"; then sudo rm -f "$stage"; fi' EXIT; cat > "$tmp"; stage=$(sudo mktemp /usr/local/bin/.tailnet.XXXXXX); sudo install -m 0755 "$tmp" "$stage"; sudo mv -f "$stage" ` + tailnetProgram + `; printf 'cage-browser ALL=(cage) NOPASSWD: ` + tailnetProgram + `\n' | sudo tee /etc/sudoers.d/tailnet >/dev/null; sudo chmod 0440 /etc/sudoers.d/tailnet; sudo rm -f ` + tailnetFormerProgram + ` /etc/sudoers.d/cage-tailnet`

// tailnetProgram is where the program is installed in the environment.
const tailnetProgram = "/usr/local/bin/tailnet"

// tailnetFormerProgram is where earlier versions installed it; it is only
// looked for, to bring such an installation up to date.
const tailnetFormerProgram = "/usr/local/bin/cage-tailnet"

func (s *Server) refreshInstalledTailnet() error {
	ctx, cancel := context.WithTimeout(s.operationContext(), 15*time.Second)
	defer cancel()
	installed, err := s.guest(ctx, "if test -x "+tailnetProgram+" || test -x "+tailnetFormerProgram+"; then printf installed; fi")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(installed)) == "installed" {
		return s.installTailnet()
	}
	return nil
}

// installTailnetNow installs Tailscale at the user's request and starts it.
func (s *Server) installTailnetNow() error {
	if err := s.installTailnet(); err != nil {
		return err
	}
	s.mu.Lock()
	ready := s.ready
	s.mu.Unlock()
	if ready {
		s.ensureHostTools()
	}
	return nil
}

func (s *Server) installTailnet() error {
	if !s.vmRunning() {
		return errors.New("请先启动 Cage 环境")
	}
	ctx, cancel := context.WithTimeout(s.operationContext(), 2*time.Minute)
	defer cancel()
	arch, err := s.guest(ctx, "uname -m")
	if err != nil {
		return err
	}
	name := "amd64"
	if string(bytes.TrimSpace(arch)) == "aarch64" {
		name = "arm64"
	} else if string(bytes.TrimSpace(arch)) != "x86_64" {
		return errors.New("当前客机架构尚不支持 Tailscale")
	}
	payload, err := tailnetassets.Linux(name)
	if err != nil {
		return errors.New("此构建缺少 Tailscale 客机组件，请安装完整的 Cage 构建")
	}
	defer payload.Close()
	reader, err := gzip.NewReader(payload)
	if err != nil {
		return err
	}
	defer reader.Close()
	script, err := s.linuxTemplate(tailnetInstallScript)
	if err != nil {
		return err
	}
	cmd := s.sshCommand(ctx, "-T", s.sshTarget(), "--", "/bin/bash -c "+shellescape.Quote(script))
	cmd.Stdin = reader
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	s.setMessage("正在安装 Tailscale，已有终端和浏览器保持运行…")
	if err := cmd.Run(); err != nil {
		return errors.New("Tailscale 安装未完成，请检查客机连接后重试")
	}
	// A node started from the program that was there before makes way for
	// the new one; its identity and settings are in its own folder. The
	// plugins are started again by whoever called: at once for an
	// installation the user asked for, after the environment's components
	// are back for an update of them.
	s.closeHostTools()
	if _, err := s.ensureTailnetPlugin(); err != nil {
		return errors.New("Tailscale 已安装，但未能登记为插件：" + err.Error())
	}
	return nil
}
