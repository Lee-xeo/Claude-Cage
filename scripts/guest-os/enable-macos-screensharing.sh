#!/bin/bash
# Enable the macOS Screen Sharing service for the ordinary Cage guest account.
# Idempotent. Must run as root inside a VirtualMac guest.
# Intended for first install (provision-macos-workbench.sh) while the guest is
# still isolated. Later repair must be started from the host only after the
# egress gate is closed; this script cannot see the host gate.
# Does not use ARD kickstart and does not set a VNC legacy password.
set -euo pipefail
export PATH=/usr/bin:/bin:/usr/sbin:/sbin
[[ $# == 1 && $EUID == 0 && $(uname -sm) == 'Darwin arm64' && $(sysctl -n hw.model) =~ ^VirtualMac[0-9]+,[0-9]+$ ]]
user=$1
[[ $user =~ ^[a-z][a-z0-9_]{0,30}$ && $user != root && $user != cageprovision && $user != cagesetup ]]
id "$user" >/dev/null
[[ -f /var/db/cage-guest-os && ! -L /var/db/cage-guest-os && $(stat -f %u /var/db/cage-guest-os) == 0 && $(cat /var/db/cage-guest-os) == macos ]]

plist=/System/Library/LaunchDaemons/com.apple.screensharing.plist
[[ -f $plist ]]
label=system/com.apple.screensharing
launchctl enable "$label"
if ! launchctl print "$label" >/dev/null 2>&1; then
    if ! launchctl bootstrap system "$plist"; then
        launchctl load -w "$plist"
    fi
fi
if ! launchctl print "$label" >/dev/null 2>&1; then
    echo '无法加载 com.apple.screensharing 服务。' >&2
    exit 1
fi

group=com.apple.access_screensharing
if ! dscl . -read "/Groups/$group" >/dev/null 2>&1; then
    dseditgroup -o create -q "$group"
fi
if ! dseditgroup -o checkmember -m "$user" "$group" >/dev/null 2>&1; then
    dseditgroup -o edit -a "$user" -t user "$group"
fi
dseditgroup -o checkmember -m "$user" "$group" >/dev/null

# Prefer a TCP probe: lsof can miss launchd sockets without extra rights,
# and macfuse project mounts can make lsof exit 1 even when 5900 is open.
ok=0
for _ in $(seq 20); do
    if nc -z 127.0.0.1 5900 >/dev/null 2>&1 || nc -z ::1 5900 >/dev/null 2>&1; then
        ok=1
        break
    fi
    sleep 0.25
done
if (( ok == 0 )); then
    echo '屏幕共享服务已加载，但尚未在 5900 端口监听。' >&2
    exit 1
fi
echo "已启用客机屏幕共享，并允许 $user 连接。"
