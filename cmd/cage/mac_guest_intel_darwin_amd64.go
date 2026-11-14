package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	qemu "github.com/lima-vm/lima/v2/pkg/driver/qemu"
	"github.com/lima-vm/lima/v2/pkg/limatype/dirnames"
	"github.com/sylvinus/agent-vm/internal/guestos"
	"github.com/sylvinus/agent-vm/internal/hostimage"
	"github.com/sylvinus/agent-vm/internal/hostproxy"
	"github.com/sylvinus/agent-vm/internal/vm"
)

func macGuestLoginAvailable() bool { return qemu.MacOSOffscreenView != nil }

func prepareMacSystemMedia(ctx context.Context, profile string, install *guestos.Installation, cache, kit string) (string, error) {
	media := install.Media
	if install.Source.Format == "pkgdmg" {
		transport := hostproxy.NewTransport()
		defer transport.CloseIdleConnections()
		ready, err := hostimage.Prepare(ctx, cache, install.Source, os.Stdout, transport)
		if err != nil {
			return "", err
		}
		if err := guestos.RecordPreparedMedia(profile, install.ID, ready); err != nil {
			return "", err
		}
		return ready.Path, nil
	}
	if !filepath.IsAbs(media) || !strings.EqualFold(filepath.Ext(media), ".pkg") || len(install.SHA256) != 64 {
		return "", errors.New("请选择已验证的 Apple 完整 Intel macOS 安装包")
	}
	file, err := os.Open(media)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	if hex.EncodeToString(hash.Sum(nil)) != install.SHA256 {
		return "", errors.New("Intel macOS 安装包已变化")
	}
	return media, nil
}

func createMacOSGuest(ctx context.Context, profile, state string, l *vm.Lima, name, image string) error {
	install, err := guestos.LoadInstallation(profile)
	if err != nil || install == nil || install.System != guestos.MacOS {
		return errors.New("缺少 Intel macOS 安装记录")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	assets := filepath.Join(filepath.Dir(exe), "intel-macos")
	cache := filepath.Join(state, "guest-media", "macos", "intel-"+install.ID.String())
	version := install.Source.Version
	if version == "" {
		version = "15.8.3"
	}
	transport := hostproxy.NewTransport()
	defer transport.CloseIdleConnections()
	fmt.Fprintln(os.Stdout, "正在准备 Intel Mac 的离线安装介质…")
	recovery, archive, err := hostimage.PrepareIntelInstaller(ctx, image, cache, version, transport)
	if err != nil {
		return err
	}
	machineID := uuid.NewString()
	media := vm.IntelMacOSMedia{Installation: install.ID, UUID: machineID, EFI: filepath.Join(assets, "EFI"), Firmware: filepath.Join(assets, "runtime", "share", "qemu", "edk2-x86_64-code.fd"), Variables: filepath.Join(assets, "runtime", "share", "qemu", "edk2-i386-vars.fd"), Installer: recovery}
	if err := l.CreateIntelMacOS(ctx, name, media, vm.Settings{DiskGiB: 64}, os.Stderr); err != nil {
		return err
	}
	dir, err := dirnames.InstanceDir(name)
	if err != nil {
		return err
	}
	if err := prepareIntelBootResources(ctx, dir, image, archive, assets, media); err != nil {
		// This newly created instance has never started or accepted user data.
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return errors.Join(err, l.Delete(cleanup, name))
	}
	return nil
}

func matchMacOSGuestSource(ctx context.Context, profile string, l *vm.Lima, name, image string) error {
	if err := l.CheckMacOSGuest(ctx, name); err != nil {
		return err
	}
	install, err := guestos.LoadInstallation(profile)
	if err != nil || install == nil {
		return errors.New("Intel macOS 安装记录缺失")
	}
	dir, err := dirnames.InstanceDir(name)
	if err != nil {
		return err
	}
	identity, err := guestos.ReadIntelMacOSIdentity(dir)
	if err != nil || identity == nil || identity.Installation != install.ID {
		return errors.New("Intel macOS 安装身份变化")
	}
	return matchIntelBootSource(ctx, dir, image)
}

func startInstallingMacOSGuest(ctx context.Context, profile string, l *vm.Lima, name string) error {
	return runIntelMacOSInstallation(ctx, profile, l, name)
}

func stopPreparedMacOSGuest(ctx context.Context, profile string, l *vm.Lima, name string) error {
	return l.StopIntelMacOS(ctx, profile, name)
}
