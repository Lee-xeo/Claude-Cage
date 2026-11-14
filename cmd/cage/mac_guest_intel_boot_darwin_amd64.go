package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sylvinus/agent-vm/internal/vm"
)

type intelInstallReceipt struct {
	UUID         string `json:"uuid"`
	Installation string `json:"installation"`
	Bytes        int64  `json:"bytes"`
	SHA256       string `json:"sha256"`
}

func prepareIntelBootResources(ctx context.Context, dir, image, archive, assets string, media vm.IntelMacOSMedia) error {
	file, err := os.Open(image)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	receipt := intelInstallReceipt{media.UUID, media.Installation.String(), info.Size(), hex.EncodeToString(hash.Sum(nil))}
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "cage-intel-install.json"), data, 0600); err != nil {
		return err
	}
	for _, item := range []struct{ source, target string }{{image, "cage-install-assistant.pkg"}, {archive, "cage-installer-app.tar.gz"}} {
		if err := exec.CommandContext(ctx, "/bin/cp", "-c", item.source, filepath.Join(dir, item.target)).Run(); err != nil {
			return err
		}
	}
	// IDE disks expose complete sectors. The guest copies only receipt.Bytes,
	// preserving the signed pkgdmg footer before its original end-of-file.
	if err := os.Truncate(filepath.Join(dir, "cage-install-assistant.pkg"), (info.Size()+2047)/2048*2048); err != nil {
		return err
	}
	helper := filepath.Join(filepath.Dir(assets), "guest-os", "cage-guest-check")
	if err := prepareIntelBootstrapPackage(ctx, dir, helper, media); err != nil {
		return err
	}
	if err := exec.CommandContext(ctx, "/usr/bin/install", "-m", "755", helper, filepath.Join(dir, "cage-guest-check")).Run(); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "cage-intel-install.sh"), []byte(intelRecoveryInstallScript), 0700); err != nil {
		return err
	}
	return prepareIntelRecoveryBootstrap(ctx, dir)
}

func prepareIntelBootstrapPackage(ctx context.Context, dir, helper string, media vm.IntelMacOSMedia) error {
	work, err := os.MkdirTemp(dir, ".bootstrap-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	payload := filepath.Join(work, "payload")
	for _, folder := range []string{"Library/LaunchDaemons", "Library/User Template", "private/var/db", "usr/local/sbin", "usr/local/libexec"} {
		if err := os.MkdirAll(filepath.Join(payload, folder), 0755); err != nil {
			return err
		}
	}
	if err := exec.CommandContext(ctx, "/usr/bin/install", "-m", "755", helper, filepath.Join(payload, "usr/local/libexec/cage-guest-check")).Run(); err != nil {
		return err
	}
	init := `#!/bin/sh
set -eu
trap 'status=$?; if test "$status" != 0; then /usr/local/libexec/cage-guest-check --install-stage error || true; fi' EXIT
` + intelMountCIDataScript + `
/usr/local/libexec/cage-guest-check --prepare ` + media.UUID + ` ` + media.Installation.String() + `
/usr/bin/pmset -a sleep 0 disksleep 0 displaysleep 0
/bin/echo macos >/var/db/cage-guest-os
/bin/chmod 644 /var/db/cage-guest-os
for attempt in $(/usr/bin/jot 120); do
    /usr/sbin/pkgutil --pkg-info dev.cage.intel-bootstrap >/dev/null 2>&1 && break
    /bin/sleep 1
done
/usr/sbin/pkgutil --pkg-info dev.cage.intel-bootstrap >/dev/null
for attempt in $(/usr/bin/jot 120); do
    mount_cidata || true
    if test -x /Volumes/cidata/lima-guestagent; then
        /usr/bin/ssh-keygen -A
        /usr/local/libexec/cage-guest-check --publish-host-key
        exec /Volumes/cidata/lima-guestagent fake-cloud-init
    fi
    /bin/sleep 1
done
exit 1
`
	if err := os.WriteFile(filepath.Join(payload, "usr/local/sbin/cage-intel-init"), []byte(init), 0755); err != nil {
		return err
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>Label</key><string>dev.cage.intel-init</string><key>ProgramArguments</key><array><string>/bin/sh</string><string>/usr/local/sbin/cage-intel-init</string></array><key>RunAtLoad</key><true/><key>StandardOutPath</key><string>/var/log/cage-intel-init.log</string><key>StandardErrorPath</key><string>/var/log/cage-intel-init.log</string></dict></plist>`
	for name, content := range map[string][]byte{"Library/LaunchDaemons/dev.cage.intel-init.plist": []byte(plist), "Library/User Template/.skipbuddy": {}, "private/var/db/.AppleSetupDone": {}} {
		if err := os.WriteFile(filepath.Join(payload, name), content, 0644); err != nil {
			return err
		}
	}
	scripts := filepath.Join(work, "scripts")
	if err := os.Mkdir(scripts, 0755); err != nil {
		return err
	}
	// Additional products can run after launchd has scanned its startup jobs.
	// Activate our job on a live target; an offline target loads it on boot.
	postinstall := `#!/bin/sh
set -eu
if test "${3:-}" = /; then
    test "$(/usr/sbin/sysctl -n kern.hv_vmm_present)" = 1
    test "$(/usr/local/libexec/cage-guest-check --hardware-uuid)" = ` + media.UUID + `
    /bin/launchctl bootstrap system /Library/LaunchDaemons/dev.cage.intel-init.plist ||
        /bin/launchctl print system/dev.cage.intel-init >/dev/null
fi
`
	if err := os.WriteFile(filepath.Join(scripts, "postinstall"), []byte(postinstall), 0755); err != nil {
		return err
	}
	component := filepath.Join(work, "bootstrap-component.pkg")
	if err := exec.CommandContext(ctx, "/usr/bin/pkgbuild", "--root", payload, "--scripts", scripts, "--ownership", "recommended", "--identifier", "dev.cage.intel-bootstrap", "--version", "1", "--install-location", "/", component).Run(); err != nil {
		return err
	}
	// startosinstall requires a product identity in the Distribution, in
	// addition to each component's package identifier.
	packages := []string{"--identifier", "dev.cage.intel-bootstrap.product", "--version", "1", "--package", component}
	packages = append(packages, filepath.Join(dir, "cage-bootstrap.pkg"))
	if err := exec.CommandContext(ctx, "/usr/bin/productbuild", packages...).Run(); err != nil {
		return err
	}
	return nil
}

const intelMountCIDataScript = `mount_cidata() {
    test -f /Volumes/cidata/meta-data && return 0
    for volume in /dev/disk[0-9]*; do
        name=$(/usr/sbin/diskutil info -plist "$volume" 2>/dev/null | /usr/bin/plutil -extract VolumeName raw -o - - 2>/dev/null) || continue
        if test "$name" = cidata; then /usr/sbin/diskutil mount readOnly "$volume" >/dev/null; fi
    done
    test -f /Volumes/cidata/meta-data
}
`

// Add only Cage's installer launcher to the private copy of Apple's verified
// Recovery image. It executes on the virtual disk, without host root access.
func prepareIntelRecoveryBootstrap(ctx context.Context, dir string) (err error) {
	work, err := os.MkdirTemp(dir, ".recovery-bootstrap-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	mount := filepath.Join(work, "volume")
	if err := os.Mkdir(mount, 0700); err != nil {
		return err
	}
	image := filepath.Join(dir, "cage-install.img")
	if output, err := exec.CommandContext(ctx, "/usr/bin/hdiutil", "attach", "-readwrite", "-nobrowse", "-noautoopen", "-owners", "off", "-mountpoint", mount, image).CombinedOutput(); err != nil {
		return fmt.Errorf("prepare private Recovery image: %w: %s", err, output)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err = errors.Join(err, exec.CommandContext(cleanup, "/usr/bin/hdiutil", "detach", mount).Run())
	}()
	script := "#!/bin/bash\nset -eu\nexport PATH=/usr/bin:/bin:/usr/sbin:/sbin\nexport __OSINSTALL_ENVIRONMENT=1\n" + intelMountCIDataScript + `
for attempt in $(/usr/bin/jot 180); do
    if mount_cidata && test -f /Volumes/cidata/boot.Cage/cage-intel-install.sh &&
        /bin/launchctl asuser 0 /usr/sbin/diskutil list -plist >/dev/null 2>&1; then
        exec /bin/launchctl asuser 0 /bin/bash /Volumes/cidata/boot.Cage/cage-intel-install.sh
    fi
    /bin/sleep 2
done
if test -x /Volumes/cidata/boot.Cage/cage-guest-check; then /Volumes/cidata/boot.Cage/cage-guest-check --install-stage error || true; fi
exit 1
`
	if err := os.WriteFile(filepath.Join(mount, "cage-intel-recovery-init.sh"), []byte(script), 0755); err != nil {
		return err
	}
	// Preserve the on-disk root owner by renaming Recovery's already disabled
	// SMB-sharing job. No file-sharing service is enabled. Creating a new plist
	// as the host user would fail launchd's ownership check inside the VM.
	folder := filepath.Join(mount, "System/Library/LaunchDaemons")
	original := filepath.Join(folder, "com.apple.smbd.mac-sharing-mode.plist")
	target := filepath.Join(folder, "dev.cage.intel-recovery.plist")
	disabled, err := exec.CommandContext(ctx, "/usr/bin/plutil", "-extract", "Disabled", "raw", "-o", "-", original).Output()
	if err != nil || strings.TrimSpace(string(disabled)) != "true" {
		return errors.New("Recovery image has no supported disabled bootstrap job")
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		return errors.New("Recovery bootstrap job already exists")
	}
	if err := os.Chmod(original, 0644); err != nil {
		return err
	}
	if err := os.Rename(original, target); err != nil {
		return err
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>Label</key><string>dev.cage.intel-recovery</string><key>ProgramArguments</key><array><string>/bin/bash</string><string>/cage-intel-recovery-init.sh</string></array><key>RunAtLoad</key><true/><key>StandardOutPath</key><string>/var/log/cage-intel-recovery.log</string><key>StandardErrorPath</key><string>/var/log/cage-intel-recovery.log</string></dict></plist>`
	return os.WriteFile(target, []byte(plist), 0644)
}

func matchIntelBootSource(ctx context.Context, dir, image string) error {
	data, err := os.ReadFile(filepath.Join(dir, "cage-intel-install.json"))
	if err != nil {
		return err
	}
	var expected intelInstallReceipt
	if json.Unmarshal(data, &expected) != nil {
		return errors.New("invalid Intel macOS source receipt")
	}
	file, err := os.Open(image)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected.SHA256 {
		return errors.New("Intel macOS installation media changed")
	}
	return ctx.Err()
}

func runIntelMacOSInstallation(ctx context.Context, profile string, l *vm.Lima, name string) error {
	return l.InstallIntelMacOS(ctx, profile, name, os.Stderr)
}

const intelRecoveryInstallScript = `#!/bin/bash
set -euo pipefail
export PATH=/usr/bin:/bin:/usr/sbin:/sbin
export __OSINSTALL_ENVIRONMENT=1
# macOS Bash 3.2 needs explicit exits for failed [[ ... ]] assertions.
[[ $(id -u) == 0 && $(sysctl -n kern.hv_vmm_present) == 1 ]] || exit 1
exec >/var/log/cage-intel-install.log 2>&1
cd /Volumes/cidata/boot.Cage
trap 'status=$?; if (( status != 0 )); then ./cage-guest-check --install-stage error || true; fi' EXIT
uuid=$(plutil -extract uuid raw -o - cage-intel-install.json)
expected=$(./cage-guest-check --hardware-uuid)
[[ $uuid == "$expected" ]] || exit 1
./cage-guest-check --install-stage prepare || true
size=$(plutil -extract bytes raw -o - cage-intel-install.json)
digest=$(plutil -extract sha256 raw -o - cage-intel-install.json)
package_size=$(( (size+2047)/2048*2048 ))
target=''; package=''
for candidate in /dev/disk[0-9]*; do
    plist=$(diskutil info -plist "$candidate" 2>/dev/null) || continue
    whole=$(printf '%s' "$plist" | plutil -extract WholeDisk raw -o - - 2>/dev/null) || continue
    [[ $whole == true ]] || continue
    bytes=$(printf '%s' "$plist" | plutil -extract TotalSize raw -o - -)
    if [[ $bytes == "$package_size" ]]; then [[ -z $package ]] || exit 1; package=$candidate; fi
    if [[ $bytes == 68719476736 ]]; then [[ -z $target ]] || exit 1; target=$candidate; fi
done
[[ -n $target && -n $package && $target != "$package" ]] || exit 1
# Erase only an empty disk. An interrupted preparation may reuse only its
# own unassigned APFS volume and the exact installation receipt it published.
diskutil list -plist "$target" >/tmp/cage-target.plist
root='/Volumes/Cage System'
if plutil -extract AllDisksAndPartitions.0.Partitions.0 xml1 -o - /tmp/cage-target.plist >/dev/null 2>&1; then
    diskutil apfs list -plist >/tmp/cage-apfs.plist
    volume=''
    for ((c=0; c<32; c++)); do
        store=$(plutil -extract "Containers.$c.PhysicalStores.0.DeviceIdentifier" raw -o - /tmp/cage-apfs.plist 2>/dev/null) || break
        [[ $store == "${target#/dev/}s2" ]] || continue
        if plutil -extract "Containers.$c.PhysicalStores.1" xml1 -o - /tmp/cage-apfs.plist >/dev/null 2>&1; then exit 1; fi
        for ((v=0; v<32; v++)); do
            name=$(plutil -extract "Containers.$c.Volumes.$v.Name" raw -o - /tmp/cage-apfs.plist 2>/dev/null) || break
            [[ $name == 'Cage System' ]] || continue
            if plutil -extract "Containers.$c.Volumes.$v.Roles.0" raw -o - /tmp/cage-apfs.plist >/dev/null 2>&1; then continue; fi
            [[ -z $volume ]] || exit 1
            volume=$(plutil -extract "Containers.$c.Volumes.$v.DeviceIdentifier" raw -o - /tmp/cage-apfs.plist)
        done
    done
    [[ $volume =~ ^disk[0-9]+s[0-9]+$ ]] || exit 1
    diskutil mount "$volume"
    point=$(diskutil info -plist "$volume" | plutil -extract MountPoint raw -o - -)
    [[ $point == "$root" && -f $root/.cage-intel-install.json && ! -L $root/.cage-intel-install.json && $(stat -f %u "$root/.cage-intel-install.json") == 0 ]] || exit 1
    cmp -s cage-intel-install.json "$root/.cage-intel-install.json"
else
    diskutil eraseDisk APFS 'Cage System' GPT "$target"
    cp cage-intel-install.json "$root/.cage-intel-install.json"
    chmod 600 "$root/.cage-intel-install.json"
fi
mkdir -p "$root/Applications"
tar -xzf cage-installer-app.tar.gz --no-same-owner -C "$root/Applications"
apps=("$root"/Applications/Install\ macOS*.app)
[[ ${#apps[@]} == 1 && -d ${apps[0]} ]] || exit 1
app=${apps[0]}
mkdir -p "$app/Contents/SharedSupport"
if ! ./cage-guest-check --verify-install-image "$app/Contents/SharedSupport/SharedSupport.dmg" "$size" "$digest"; then
    head -c "$size" "/dev/r${package#/dev/}" > "$app/Contents/SharedSupport/SharedSupport.dmg"
    ./cage-guest-check --verify-install-image "$app/Contents/SharedSupport/SharedSupport.dmg" "$size" "$digest"
fi
./cage-guest-check --install-stage start || true
"$app/Contents/Resources/startosinstall" --agreetolicense --volume "$root" --installpackage /Volumes/cidata/boot.Cage/cage-bootstrap.pkg --nointeraction --rebootdelay 0
`
