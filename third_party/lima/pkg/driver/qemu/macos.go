// SPDX-FileCopyrightText: Copyright The Lima Authors
// SPDX-License-Identifier: Apache-2.0

package qemu

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/coreos/go-semver/semver"
	"github.com/docker/go-units"
	"github.com/google/uuid"
	"github.com/lima-vm/lima/v2/pkg/limatype"
	"github.com/lima-vm/lima/v2/pkg/limatype/filenames"
	"github.com/lima-vm/lima/v2/pkg/limayaml"
)

const MacOSBootDisk = filenames.MacOSBootDisk
const MacOSVariables = "qemu-efi-vars.fd"
const MacOSInstaller = "cage-install.img"
const MacOSInstallPackage = "cage-install-assistant.pkg"

func validateMacOSConfig(y *limatype.LimaYAML, options *limatype.QEMUMacOSOpts) error {
	if y.OS == nil || *y.OS != limatype.DARWIN || y.Arch == nil || *y.Arch != limatype.X8664 ||
		y.VMType == nil || *y.VMType != limatype.QEMU || y.Video.Display == nil || *y.Video.Display != "offscreen" {
		return errors.New("Intel macOS requires the explicit x86_64 QEMU offscreen configuration")
	}
	id, err := uuid.Parse(options.UUID)
	if err != nil || id == uuid.Nil || id.String() != options.UUID {
		return errors.New("Intel macOS requires a persistent canonical machine UUID")
	}
	if len(y.Mounts) != 0 || len(y.AdditionalDisks) != 0 || len(y.Networks) != 0 ||
		(y.Firmware.LegacyBIOS != nil && *y.Firmware.LegacyBIOS) || len(y.Firmware.Images) != 0 ||
		(y.NestedVirtualization != nil && *y.NestedVirtualization) {
		return errors.New("Intel macOS does not permit host shares, external disks, extra networks, kernels or firmware overrides")
	}
	for _, image := range y.Images {
		if image.Kernel != nil || image.Initrd != nil {
			return errors.New("Intel macOS does not permit kernel or initrd overrides")
		}
	}
	for _, port := range y.PortForwards {
		if !port.Ignore {
			return errors.New("Intel macOS does not permit unmanaged forwarded ports")
		}
	}
	return nil
}

func macOSCmdline(ctx context.Context, cfg Config, options *limatype.QEMUMacOSOpts) (string, []string, error) {
	if err := validateMacOSConfig(cfg.LimaYAML, options); err != nil {
		return "", nil, err
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "amd64" || Accel(limatype.X8664) != "hvf" {
		return "", nil, errors.New("Intel macOS requires native Hypervisor.framework acceleration on an Intel Mac")
	}
	exe, extra, err := Exe(limatype.X8664)
	if err != nil {
		return "", nil, err
	}
	if len(extra) != 0 {
		return "", nil, errors.New("Intel macOS does not accept extra QEMU arguments from the environment")
	}
	version, err := getQemuVersion(ctx, exe)
	if err != nil || version.LessThan(*semver.New("11.1.3")) {
		return "", nil, errors.New("Intel macOS requires QEMU 11.1.3 or newer with Apple Paravirtualized Graphics")
	}
	features, err := inspectFeatures(ctx, exe, "q35")
	if err != nil {
		return "", nil, err
	}
	devices, err := exec.CommandContext(ctx, exe, "-machine", "q35", "-device", "help").Output()
	if err != nil || !strings.Contains(string(devices), "apple-gfx-pci") || !strings.Contains(string(features.AccelHelp), "hvf") ||
		!strings.Contains(string(features.NetdevHelp), "dgram") {
		return "", nil, errors.New("QEMU lacks the required Intel macOS GPU, HVF or isolated datagram network support")
	}
	args, err := macOSArguments(cfg, options)
	return exe, args, err
}

// Keep this separate from capability probing so tests can inspect the actual
// complete argument list, including the absence of every fallback network.
func macOSArguments(cfg Config, options *limatype.QEMUMacOSOpts) ([]string, error) {
	if cfg.UsernetDgram == nil {
		return nil, errors.New("Intel macOS cannot start without its own controlled datagram network")
	}
	y := cfg.LimaYAML
	if err := validateMacOSConfig(y, options); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(cfg.InstanceDir) || y.CPUs == nil || *y.CPUs < 2 || y.Memory == nil {
		return nil, errors.New("invalid Intel macOS instance resources")
	}
	memory, err := units.RAMInBytes(*y.Memory)
	if err != nil || memory < 4<<30 {
		return nil, errors.New("Intel macOS requires at least 4 GiB RAM")
	}
	files := []string{filenames.QemuEfiCodeFD, MacOSVariables, MacOSBootDisk, filenames.Disk, filenames.CIDataISO}
	if options.Installer {
		files = append(files, MacOSInstaller, MacOSInstallPackage)
	}
	for _, name := range files {
		info, err := os.Lstat(filepath.Join(cfg.InstanceDir, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return nil, fmt.Errorf("Intel macOS requires a private regular instance file: %s", name)
		}
	}
	file := func(name string) string { return strings.ReplaceAll(filepath.Join(cfg.InstanceDir, name), ",", ",,") }
	args := []string{
		"-no-user-config", "-nodefaults", "-name", "lima-" + cfg.Name,
		"-pidfile", file(filenames.PIDFile(limatype.QEMU)),
		"-machine", "q35,accel=hvf,smm=off",
		"-global", "ICH9-LPC.disable_s3=1", "-global", "ICH9-LPC.disable_s4=1",
		"-cpu", "host,-pdpe1gb,+hypervisor,vmware-cpuid-freq=on",
		"-smp", fmt.Sprintf("%d,sockets=1,cores=%d,threads=1", *y.CPUs, *y.CPUs),
		"-m", strconv.FormatInt(memory>>20, 10), "-uuid", options.UUID, "-rtc", "base=utc",
		"-drive", "if=pflash,format=raw,readonly=on,file=" + file(filenames.QemuEfiCodeFD),
		"-drive", "if=pflash,format=raw,file=" + file(MacOSVariables),
		"-smbios", "type=2", "-device", "qemu-xhci,id=xhci",
		"-device", "usb-kbd,bus=xhci.0", "-device", "usb-tablet,bus=xhci.0",
		"-device", "ich9-ahci,id=sata",
		// IDE hard disks require a writable frontend. An ephemeral overlay keeps
		// the verified boot image immutable while satisfying the IDE device.
		"-drive", "id=OpenCore,if=none,format=raw,snapshot=on,file=" + file(MacOSBootDisk),
		"-device", "ide-hd,bus=sata.0,drive=OpenCore,bootindex=0",
		"-drive", "id=System,if=none,format=qcow2,discard=unmap,file=" + file(filenames.Disk),
		"-device", "ide-hd,bus=sata.2,drive=System,rotation_rate=1",
		"-drive", "id=cidata,if=none,format=raw,readonly=on,file=" + file(filenames.CIDataISO),
		"-device", "ide-cd,bus=sata.3,drive=cidata",
		"-netdev", "dgram,id=net0,local.type=fd,local.str={{ fd_dgram }}",
		"-device", "virtio-net-pci,netdev=net0,mac=" + limayaml.MACAddress(cfg.InstanceDir),
		"-display", "none", "-monitor", "none", "-parallel", "none",
		"-chardev", "file,id=serial,path=" + file(filenames.SerialLog) + ",append=on",
		"-serial", "chardev:serial", "-qmp", "unix:" + file(filenames.QMPSock) + ",server=on,wait=off",
	}
	if options.Installer {
		args = append(args, "-vga", "std", "-drive", "id=Installer,if=none,format=raw,snapshot=on,file="+file(MacOSInstaller),
			"-device", "ide-hd,bus=sata.1,drive=Installer", "-drive", "id=InstallPackage,if=none,format=raw,snapshot=on,file="+file(MacOSInstallPackage),
			"-device", "ide-hd,bus=sata.4,drive=InstallPackage,serial=CAGEINSTALLPKG")
	} else {
		args = append(args, "-vga", "none", "-device", `{"driver":"apple-gfx-pci","display-modes":["1600x1000@60"]}`)
	}
	return args, nil
}
