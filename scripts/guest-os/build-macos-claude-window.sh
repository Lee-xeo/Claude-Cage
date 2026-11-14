#!/bin/bash
# Build the GUI-session helper for the selected native guest (macOS 14+).
set -euo pipefail
src=$(cd "$(dirname "$0")" && pwd)
. "$src/macos-guest-arch.sh"
app=${1:?Specify a new output .app path}
[[ $app == *.app && ! -e $app ]] || { echo 'Choose a new .app path.' >&2; exit 2; }
mkdir -p "$app/Contents/MacOS"
cat > "$app/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>dev.cage.GuestClaudeWindow</string>
<key>CFBundleName</key><string>Cage Guest Window</string>
<key>CFBundleExecutable</key><string>CageGuestWindow</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>1.0</string>
<key>CFBundleVersion</key><string>1</string>
<key>LSUIElement</key><true/>
<key>CFBundleDocumentTypes</key><array><dict>
<key>CFBundleTypeName</key><string>Cage project file</string>
<key>CFBundleTypeRole</key><string>Viewer</string>
<key>LSHandlerRank</key><string>None</string>
<key>LSItemContentTypes</key><array><string>public.data</string></array>
</dict></array>
</dict></plist>
PLIST
objects=$(mktemp -d)
trap 'rm -rf "$objects"' EXIT
xcrun clang -target "$CAGE_MACOS_NATIVE_ARCH-apple-macos14.0" -O2 -fobjc-arc -DCAGE_GUEST_IDENTITY_LIBRARY -c "$src/macos-guest-identity.m" -o "$objects/guest-identity.o"
xcrun clang -target "$CAGE_MACOS_NATIVE_ARCH-apple-macos14.0" -Wall -Wextra -Werror -O2 -c "$src/macos-file-writers.c" -o "$objects/file-writers.o"
xcrun swiftc -parse-as-library -D GUEST_WINDOW_APP -O -target "$CAGE_MACOS_NATIVE_ARCH-apple-macos14.0" "$src/../../desktop/MacKeyboard.swift" "$src/claude-window-macos.swift" "$src/macos-diagnostics.swift" "$src/macos-claude-window-app.swift" "$src/macos-chrome-window.swift" "$src/macos-chrome-window-selection.swift" "$src/macos-window-input.swift" "$src/macos-maintenance.swift" "$src/macos-maintenance-compositor.swift" "$src/macos-setup-password.swift" "$src/macos-project-panel.swift" "$src/macos-claude-canvas.swift" "$src/macos-window-stream.swift" "$src/macos-audio.swift" "$src/macos-gui-session.swift" "$src/macos-phone-desktop.swift" "$src/macos-desktop-files.swift" "$src/macos-desktop-downloads.swift" "$src/macos-native-open.swift" "$objects/file-writers.o" "$objects/guest-identity.o" -framework IOKit -lbsm -o "$app/Contents/MacOS/CageGuestWindow"
codesign --force --sign "${CAGE_GUEST_SIGN_IDENTITY:-${CAGE_SIGNING_IDENTITY:--}}" --identifier dev.cage.GuestClaudeWindow "$app"
codesign --verify --strict "$app"
