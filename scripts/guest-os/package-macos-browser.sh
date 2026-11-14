#!/bin/bash
# Assemble the guest-only workbench controller alongside the pinned browser media.
set -euo pipefail
[[ $# == 1 ]] || exit 1
src=$(cd "$(dirname "$0")" && pwd)
. "$src/macos-guest-arch.sh"
modules="$src/../../internal/cage"
dest="$1"
mkdir -p "$dest/modules"
install -m 644 "$src"/{macos-cli-browser,macos-native-browser,macos-browser-service,macos-browser-window,macos-phone-browser,browser-protocol}.mjs "$dest/"
install -m 644 "$modules"/browser-{phone,phone-network}.mjs "$dest/modules/"

install -m 644 "$modules/browser-check.html" "$dest/"
install -m 644 "$modules/tailnet-client.mjs" "$dest/"
bash "$src/../build-tailnet.sh" darwin "$CAGE_MACOS_GO_ARCH" "$dest/cage-tailnet"
