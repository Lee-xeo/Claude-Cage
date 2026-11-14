//go:build darwin && arm64 && cgo && !no_vz

package main

import (
	"context"
	"fmt"
	vzdriver "github.com/lima-vm/lima/v2/pkg/driver/vz"
	"github.com/sylvinus/agent-vm/internal/guestos"
	"github.com/sylvinus/agent-vm/internal/vm"
	"os"
	"path/filepath"
)

const appleImageURL = "https://updates.cdn-apple.com/2026SpringFCS/fullrestores/140-24263/B95838F0-6815-4F0B-A039-156526C081AD/UniversalMac_26.5.2_25F84_Restore.ipsw"
const appleImageDigest = "065abd295a1a456a46c1155217eab92ee95816520ec9aeed83f249f074f68a04"
const appleImageBytes int64 = 19769902281

func macGuestLoginAvailable() bool { return vzdriver.OffscreenView != nil }
func createMacOSGuest(ctx context.Context, profile, state string, l *vm.Lima, name, image string) error {
	return l.CreateMacOSFromIPSW(ctx, name, image, vm.Settings{DiskGiB: 64}, os.Stderr)
}
func matchMacOSGuestSource(ctx context.Context, profile string, l *vm.Lima, name, image string) error {
	return l.MatchMacOSInstallSource(ctx, name, image)
}
func startInstallingMacOSGuest(ctx context.Context, profile string, l *vm.Lima, name string) error {
	return l.Start(ctx, name, os.Stderr)
}

func stopPreparedMacOSGuest(ctx context.Context, profile string, l *vm.Lima, name string) error {
	return l.Stop(ctx, name)
}
func prepareMacSystemMedia(ctx context.Context, profile string, i *guestos.Installation, cache, kit string) (string, error) {
	media := i.Media
	if media == "" {
		media = filepath.Join(cache, "MacOS.ipsw")
		fmt.Fprintln(os.Stdout, "正在从 Apple 下载并校验 macOS 26.5.2（19.77 GB）…")
		if err := prepareMacRestoreMedia(ctx, media, appleImageURL, appleImageDigest, appleImageBytes, os.Stdout); err != nil {
			return "", err
		}
	}
	return media, nil
}
