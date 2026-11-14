package workbench

import (
	"context"
	_ "embed"
	"errors"
	"github.com/sylvinus/agent-vm/internal/guestos"
	"strings"
	"time"
)

//go:embed macos_extension_probe.pl
var macOSProbeFSKit string

func (s *Server) checkMacOSSetupProjects(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	probe := "exec /usr/bin/perl -"
	if s.macosGuest != nil && s.macosGuest.Backend == guestos.MacOSQEMU {
		probe += " server-readonly"
	}
	cmd := s.macOSSSHCommand(ctx, "-T", s.sshTarget(), "--", probe)
	cmd.Stdin = strings.NewReader(macOSProbeFSKit)
	err := cmd.Run()
	s.mu.Lock()
	if err != nil {
		s.macSetupProjectsChecked = time.Time{}
	} else {
		s.macSetupProjectsChecked = time.Now()
	}
	s.mu.Unlock()
	if err != nil {
		return errors.New("文件共享权限尚未通过。请点击「文件系统扩展」，按引导完成 macFUSE 安装，并打开 macFUSE 和 macFUSE (local) 两个开关，Cage 会自动检查")
	}
	return nil
}
