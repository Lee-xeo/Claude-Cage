package vm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	diskfs "github.com/diskfs/go-diskfs"
	diskpkg "github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/partition/gpt"
	"github.com/google/uuid"
	"github.com/lima-vm/lima/v2/pkg/instance"
	"github.com/lima-vm/lima/v2/pkg/limatype"
	"github.com/lima-vm/lima/v2/pkg/limatype/dirnames"
	"github.com/lima-vm/lima/v2/pkg/limatype/filenames"
	"github.com/lima-vm/lima/v2/pkg/limayaml"
	"github.com/sylvinus/agent-vm/internal/guestos"
)

type IntelMacOSMedia struct {
	Installation uuid.UUID
	UUID         string
	EFI          string
	Firmware     string
	Variables    string
	Installer    string
}

// Apple's installer reboots several times without any channel to the host
// other than the serial port and the disk image. Progress is therefore judged
// by activity, not by a fixed deadline: a slow Mac may take far longer than a
// fast one, while a hung installer stops writing to both.
const (
	intelInstallStallLimit = 25 * time.Minute
	intelInstallHardLimit  = 3 * time.Hour
	intelInstallHeartbeat  = 2 * time.Minute
)

// InstallIntelMacOS waits through Apple's installer reboots, then leaves the
// stopped instance switched to its accelerated display. The caller starts it.
func (l *Lima) InstallIntelMacOS(ctx context.Context, profile, name string, log io.Writer) error {
	if err := l.CheckMacOSGuest(ctx, name); err != nil {
		return err
	}
	inst, err := l.inspect(ctx, name)
	if err != nil {
		return err
	}
	var options struct {
		MacOS struct {
			Installer bool   `yaml:"installer"`
			UUID      string `yaml:"uuid"`
		} `yaml:"macOS"`
	}
	if err := limayaml.Convert(inst.Config.VMOpts["qemu"], &options, "vmOpts.qemu"); err != nil {
		return err
	}
	if !options.MacOS.Installer {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, intelInstallHardLimit)
	defer cancel()
	serial := filepath.Join(inst.Dir, "serial.log")
	if data, err := os.ReadFile(serial); err == nil && inst.Status == limatype.StatusRunning && intelMacOSInstallStage(data) == "ERROR" {
		if err := l.StopIntelMacOS(ctx, profile, name); err != nil {
			return err
		}
		inst.Status = limatype.StatusStopped
	}
	var offset int64
	if info, err := os.Stat(serial); err == nil && inst.Status != limatype.StatusRunning {
		offset = info.Size()
	}
	disk := filepath.Join(inst.Dir, filenames.Disk)
	fmt.Fprintln(log, "正在启动虚拟 Mac 的安装环境…整个系统安装通常需要 20–40 分钟，期间虚拟 Mac 会自动重启数次。")
	started := make(chan error, 1)
	go func() { started <- l.Start(instance.WithWatchHostAgentTimeout(ctx, intelInstallHardLimit), name, log) }()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	begun := time.Now()
	activity := intelInstallActivity{}
	lastActive, lastBeat := begun, begun
	startFinished := false
	stage, progress, boots := "", "", 0
	for {
		select {
		case err := <-started:
			if err != nil {
				return err
			}
			startFinished = true
			started = nil
		case <-ctx.Done():
			return fmt.Errorf("Intel macOS 安装超过 %d 小时仍未完成，现有磁盘已保留：%w", int(intelInstallHardLimit.Hours()), ctx.Err())
		case <-ticker.C:
		}
		now := time.Now()
		if activity.update(serial, disk) {
			lastActive = now
		}
		if now.Sub(lastActive) > intelInstallStallLimit {
			return fmt.Errorf("Intel macOS 安装已 %d 分钟没有磁盘或串口活动，判定为停滞；现有磁盘已保留，可继续原任务", int(intelInstallStallLimit.Minutes()))
		}
		if data, err := os.ReadFile(serial); err == nil && int64(len(data)) >= offset {
			data = data[offset:]
			if next := intelMacOSInstallStage(data); next != "" && next != stage {
				stage = next
				switch stage {
				case "PREPARE":
					fmt.Fprintln(log, "正在将安装映像写入虚拟 Mac 并校验…")
				case "START":
					fmt.Fprintln(log, "Apple 安装程序已启动，正在准备安装…")
				case "ERROR":
					return errors.New("虚拟 Mac 安装准备失败，现有磁盘已保留；请查看客机安装日志")
				}
			}
			if phase, percent, ok := intelMacOSInstallProgress(data); ok {
				label := map[string]string{"copy": "写入并校验安装映像", "prepare": "Apple 安装程序正在准备"}[phase]
				// Five-percent steps keep the operation log readable.
				if next := fmt.Sprintf("%s %d", phase, percent/5*5); label != "" && next != progress {
					progress = next
					fmt.Fprintf(log, "%s：%d%%\n", label, percent/5*5)
				}
			}
			if count := intelMacOSBootCount(data); count > boots {
				boots = count
				if boots > 1 {
					fmt.Fprintf(log, "虚拟 Mac 已第 %d 次启动（安装过程中会自动重启数次）…\n", boots)
				}
			}
		}
		if now.Sub(lastBeat) >= intelInstallHeartbeat {
			lastBeat = now
			fmt.Fprintf(log, "系统安装进行中：已用时 %d 分钟，系统盘已写入 %.1f GB。\n", int(now.Sub(begun).Minutes()), float64(activity.diskBytes)/1e9)
		}
		if !startFinished {
			continue
		}
		if _, err := guestos.MacOSInstallHostKey(profile); err != nil {
			continue
		}
		if err := guestos.PinMacOSInstallHostKey(profile); err != nil {
			return err
		}
		args, err := guestos.MacOSSSHArgs(profile, "cageprovision")
		if err != nil {
			return err
		}
		check, stop := context.WithTimeout(ctx, 8*time.Second)
		cmd := exec.CommandContext(check, "/usr/bin/ssh", append(args, "-T", "cage-macos", "--", "/usr/local/libexec/cage-guest-check --expect "+options.MacOS.UUID+" && test -s /var/run/lima-boot-done")...)
		err = cmd.Run()
		stop()
		if err == nil {
			break
		}
	}
	fmt.Fprintf(log, "macOS 系统安装完成，用时 %d 分钟。正在切换到正式运行配置…\n", int(time.Since(begun).Minutes()))
	if err := l.StopIntelMacOS(ctx, profile, name); err != nil {
		return err
	}
	finished := false
	// The caller boots the finished guest once, with the user's allocation.
	return l.Edit(ctx, name, Settings{macOSInstaller: &finished})
}

// intelInstallActivity notices any growth or rewrite of the serial log and
// the system disk image. Either is evidence that the installer still runs.
type intelInstallActivity struct {
	serialSize, diskBytes int64
	diskModified          time.Time
}

func (a *intelInstallActivity) update(serial, disk string) bool {
	changed := false
	if info, err := os.Stat(serial); err == nil && info.Size() != a.serialSize {
		a.serialSize, changed = info.Size(), true
	}
	if info, err := os.Stat(disk); err == nil && (info.Size() != a.diskBytes || !info.ModTime().Equal(a.diskModified)) {
		a.diskBytes, a.diskModified, changed = info.Size(), info.ModTime(), true
	}
	return changed
}

// StopIntelMacOS asks the verified provisioning channel to save and shut down
// macOS. The emulated ACPI power button alone does not reliably stop macOS.
func (l *Lima) StopIntelMacOS(ctx context.Context, profile, name string) error {
	if err := l.CheckMacOSGuest(ctx, name); err != nil {
		return err
	}
	inst, err := l.inspect(ctx, name)
	if err != nil {
		return err
	}
	if inst.HostAgentPID > 0 {
		identity, identityErr := guestos.ReadIntelMacOSIdentity(inst.Dir)
		args, argsErr := guestos.MacOSSSHArgs(profile, "cageprovision")
		if identityErr == nil && identity != nil && argsErr == nil {
			request, cancel := context.WithTimeout(ctx, 8*time.Second)
			command := "/usr/local/libexec/cage-guest-check --expect " + identity.UUID + " && sudo -S -p '' /sbin/shutdown -h now < /Users/cageprovision/password"
			err := exec.CommandContext(request, "/usr/bin/ssh", append(args, "-T", "cage-macos", "--", command)...).Run()
			cancel()
			var exit *exec.ExitError
			if err == nil || errors.As(err, &exit) && exit.ExitCode() == 255 {
				wait, stop := context.WithTimeout(ctx, 30*time.Second)
				defer stop()
				for wait.Err() == nil {
					current, err := l.inspect(wait, name)
					if err == nil && current.Status == limatype.StatusStopped {
						return nil
					}
					select {
					case <-wait.Done():
					case <-time.After(200 * time.Millisecond):
					}
				}
			}
		}
	}
	return l.Stop(ctx, name)
}

func intelMacOSInstallStage(data []byte) string {
	// QEMU keeps the serial attachment across Apple's installer reboots. An
	// error from a previous boot must not stop a now-running installation.
	const boot = "======== End of efiboot serial output. ========"
	if index := bytes.LastIndex(data, []byte(boot)); index >= 0 {
		data = data[index+len(boot):]
	}
	// Progress lines share the prefix; the stage is the latest known marker.
	const prefix = "\nCAGE_INTEL_INSTALL_"
	for index := bytes.LastIndex(data, []byte(prefix)); index >= 0; index = bytes.LastIndex(data[:index], []byte(prefix)) {
		stage, _, _ := strings.Cut(string(data[index+len(prefix):]), "\n")
		switch strings.TrimSpace(stage) {
		case "WAIT_DISKS", "PREPARE", "START", "ERROR":
			return strings.TrimSpace(stage)
		}
	}
	return ""
}

// intelMacOSInstallProgress returns the latest percentage reported by the
// guest for the current boot: "copy" while it writes the installation image,
// "prepare" while Apple's startosinstall prepares the target volume.
func intelMacOSInstallProgress(data []byte) (string, int, bool) {
	const boot = "======== End of efiboot serial output. ========"
	if index := bytes.LastIndex(data, []byte(boot)); index >= 0 {
		data = data[index+len(boot):]
	}
	const prefix = "\nCAGE_INTEL_INSTALL_PROGRESS "
	index := bytes.LastIndex(data, []byte(prefix))
	if index < 0 {
		return "", 0, false
	}
	line, _, complete := strings.Cut(string(data[index+len(prefix):]), "\n")
	fields := strings.Fields(line)
	if !complete || len(fields) != 2 || (fields[0] != "copy" && fields[0] != "prepare") {
		return "", 0, false
	}
	percent, err := strconv.Atoi(fields[1])
	if err != nil || percent < 0 || percent > 100 {
		return "", 0, false
	}
	return fields[0], percent, true
}

// intelMacOSBootCount counts kernel handoffs; Apple's installer reboots the
// guest between its phases.
func intelMacOSBootCount(data []byte) int {
	return bytes.Count(data, []byte("HANDOFF TO XNU"))
}

// CreateIntelMacOS creates only a stopped, private instance. The official
// offline installer and its first-boot package run through the same confined
// hostagent as the finished guest; no temporary NAT or host share is added.
func (l *Lima) CreateIntelMacOS(ctx context.Context, name string, media IntelMacOSMedia, settings Settings, log io.Writer) (resultErr error) {
	if media.Installation == uuid.Nil {
		return errors.New("missing Intel macOS installation identity")
	}
	id, err := uuid.Parse(media.UUID)
	if err != nil || id == uuid.Nil || id.String() != media.UUID {
		return errors.New("invalid Intel macOS machine UUID")
	}
	if settings.Mounts != nil && len(*settings.Mounts) != 0 {
		return errors.New("Intel macOS does not accept host mounts")
	}
	if settings.CPUs == 0 {
		settings.CPUs = 4
	}
	if settings.MemoryGiB == 0 {
		settings.MemoryGiB = 8
	}
	if settings.DiskGiB == 0 {
		settings.DiskGiB = 64
	}
	if settings.CPUs < 2 || settings.MemoryGiB < 4 || settings.DiskGiB != 64 {
		return errors.New("Intel macOS requires 2 CPUs, 4 GiB RAM and a 64 GiB system disk")
	}
	inputs := []string{media.Firmware, media.Variables}
	imageSource := media.Installer
	inputs = append(inputs, imageSource)
	for _, p := range inputs {
		info, err := os.Lstat(p)
		if !filepath.IsAbs(p) || err != nil || !info.Mode().IsRegular() {
			return errors.New("select regular, verified Intel macOS installation resources")
		}
	}
	if err := dirnames.ValidateInstName(name); err != nil {
		return err
	}
	template, err := os.CreateTemp("", "cage-intel-macos-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(template.Name())
	image, _ := json.Marshal(imageSource)
	installer, uid := true, 501
	_, err = fmt.Fprintf(template, intelMacOSInstallTemplate, image, media.UUID, installer, uid)
	if err = errors.Join(err, template.Close()); err != nil {
		return err
	}
	settings.NoContainerd = true
	if err := l.Create(ctx, name, template.Name(), settings, log); err != nil {
		return err
	}
	defer func() {
		if resultErr != nil {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			resultErr = errors.Join(resultErr, l.Delete(cleanup, name))
		}
	}()
	dir, err := dirnames.InstanceDir(name)
	if err != nil {
		return err
	}
	copies := []struct{ source, name string }{{media.Firmware, filenames.QemuEfiCodeFD}, {media.Variables, "qemu-efi-vars.fd"}}
	copies = append(copies, struct{ source, name string }{media.Installer, "cage-install.img"})
	for _, copy := range copies {
		if err := cloneIntelResource(ctx, copy.source, filepath.Join(dir, copy.name)); err != nil {
			return err
		}
	}
	boot := filepath.Join(dir, filenames.MacOSBootDisk)
	if err := makeIntelMacOSBootDisk(ctx, media.EFI, boot, media.UUID, limayaml.MACAddress(dir)); err != nil {
		return err
	}
	{
		qemuImg, err := exec.LookPath("qemu-img")
		if err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, qemuImg, "create", "-f", "qcow2", filepath.Join(dir, filenames.Disk), fmt.Sprintf("%dG", settings.DiskGiB))
		cmd.Stderr = log
		if err := cmd.Run(); err != nil {
			return err
		}
	}
	file, err := os.Open(boot)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	identity := guestos.IntelMacOSIdentity{Version: 1, Backend: guestos.MacOSQEMU, UUID: media.UUID, Installation: media.Installation, BootSHA256: hex.EncodeToString(hash.Sum(nil))}
	if err := guestos.WriteIntelMacOSIdentity(dir, identity); err != nil {
		return err
	}
	return saveMacOSResourceRequirements(dir, MacOSResourceRequirements{Version: 1, MemoryGiB: 4, CPUs: 2, Source: "existing-configuration"})
}

func cloneIntelResource(ctx context.Context, source, dest string) error {
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		return errors.New("refusing to replace existing Intel macOS resource")
	}
	if err := exec.CommandContext(ctx, "/bin/cp", "-c", source, dest).Run(); err == nil {
		return os.Chmod(dest, 0600)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// cp can leave its own partial destination when cloning is unsupported.
	if err := os.Remove(dest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	return errors.Join(copyErr, out.Close())
}

func makeIntelMacOSBootDisk(ctx context.Context, source, target, machineID, mac string) error {
	work, err := os.MkdirTemp("", "cage-intel-efi-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	config := filepath.Join(work, "config.plist")
	if err := cloneIntelResource(ctx, filepath.Join(source, "OC", "config.plist"), config); err != nil {
		return err
	}
	address, err := net.ParseMAC(mac)
	if err != nil {
		return err
	}
	for _, edit := range []struct{ key, kind, value string }{{"PlatformInfo.Generic.SystemUUID", "-string", strings.ToUpper(machineID)}, {"PlatformInfo.Generic.ROM", "-data", base64.StdEncoding.EncodeToString(address)}, {"PlatformInfo.Generic.SystemSerialNumber", "-string", "CG" + strings.ToUpper(strings.ReplaceAll(machineID, "-", "")[:10])}, {"PlatformInfo.Generic.MLB", "-string", "CAGE" + strings.ToUpper(strings.ReplaceAll(machineID, "-", "")[:13])}} {
		if err := exec.CommandContext(ctx, "/usr/bin/plutil", "-replace", edit.key, edit.kind, edit.value, config).Run(); err != nil {
			return err
		}
	}
	disk, err := diskfs.Create(target, 128<<20, diskfs.SectorSize512)
	if err != nil {
		return err
	}
	defer disk.Close()
	table := &gpt.Table{LogicalSectorSize: 512, PhysicalSectorSize: 512, ProtectiveMBR: true, Partitions: []*gpt.Partition{{Index: 1, Start: 2048, End: (128<<20)/512 - 34, Type: gpt.EFISystemPartition, Name: "Cage EFI"}}}
	if err := disk.Partition(table); err != nil {
		return err
	}
	fat, err := disk.CreateFilesystem(diskpkg.FilesystemSpec{Partition: 1, FSType: filesystem.TypeFat32, VolumeLabel: "CAGEBOOT"})
	if err != nil {
		return err
	}
	defer fat.Close()
	var total int64
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return fat.Mkdir("/EFI")
		}
		name := "/EFI/" + filepath.ToSlash(rel)
		if entry.IsDir() {
			return fat.Mkdir(name)
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 32<<20 {
			return errors.New("invalid Intel EFI resource")
		}
		total += info.Size()
		if total > 96<<20 {
			return errors.New("Intel EFI resources exceed the boot disk")
		}
		if filepath.ToSlash(rel) == "OC/config.plist" {
			path = config
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := fat.OpenFile(name, os.O_CREATE|os.O_RDWR)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(data)
		return errors.Join(writeErr, file.Close())
	})
}

const intelMacOSInstallTemplate = `os: Darwin
arch: x86_64
vmType: qemu
images:
  - location: %s
    arch: x86_64
vmOpts:
  qemu:
    minimumVersion: "11.1.3"
    macOS:
      uuid: %s
      installer: %t
user:
  name: cageprovision
  home: /Users/cageprovision
  comment: Cage Provisioning
  uid: %d
osOpts:
  Darwin:
    suppressFirstLoginSetup: true
video:
  display: offscreen
mounts: []
networks: []
ssh:
  loadDotSSHPubKeys: false
  forwardAgent: false
  forwardX11: false
portForwards:
  - guestIP: 0.0.0.0
    guestIPMustBeZero: false
    guestPortRange: [1, 65535]
    proto: any
    ignore: true
containerd:
  system: false
  user: false
`
