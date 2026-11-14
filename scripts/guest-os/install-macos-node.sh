#!/bin/bash
# The runtime used by ordinary terminal and debugger sessions in the macOS guest.
set -euo pipefail
export PATH=/usr/bin:/bin:/usr/sbin:/sbin
[[ $# == 1 && $EUID == 0 && ( $(sysctl -n hw.model) =~ ^VirtualMac[0-9]+,[0-9]+$ || $(/usr/local/libexec/cage-guest-check --check >/dev/null 2>&1; echo $?) == 0 ) ]] || exit 1
[[ -f /var/db/cage-guest-os && ! -L /var/db/cage-guest-os && $(stat -f %u /var/db/cage-guest-os) == 0 && $(cat /var/db/cage-guest-os) == macos ]] || exit 1
CAGE_MACOS_GUEST_ARCH=$(uname -m)
. "$(dirname "$0")/macos-guest-arch.sh"
base=/usr/local/libexec
target="$base/cage-node-v24.22.2"
mkdir -p "$base" /usr/local/bin
stage=$(mktemp -d "$base/.cage-node.XXXXXXXX")
trap 'rm -rf "$stage"' EXIT
if [[ ! -e $target ]]; then
  cp "$1" "$stage/node.tgz"
  [[ $(shasum -a 256 "$stage/node.tgz" | cut -d' ' -f1) == "$CAGE_MACOS_NODE_SHA256" ]] || exit 1
  tar -xzf "$stage/node.tgz" -C "$stage" --no-same-owner
  [[ $("$stage/node-v24.22.2-darwin-$CAGE_MACOS_NODE_ARCH/bin/node" -p 'process.platform + "/" + process.arch + "/" + process.version') == "darwin/$CAGE_MACOS_NODE_ARCH/v24.22.2" ]] || exit 1
  mv "$stage/node-v24.22.2-darwin-$CAGE_MACOS_NODE_ARCH" "$target"
fi
for tool in node npm npx; do
  if [[ ! -e /usr/local/bin/$tool && ! -L /usr/local/bin/$tool ]]; then
    ln -s "$target/bin/$tool" "/usr/local/bin/$tool"
  fi
done
