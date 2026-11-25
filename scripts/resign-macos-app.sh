#!/usr/bin/env bash
# Sign an already built Cage.app with the Developer ID identity, notarize it and
# place it at _output/Cage.app for `scripts/package-macos.sh --from-notarized-app`.
#
# The Intel App is built natively on an Intel Mac because it carries its QEMU
# runtime. When the release identity lives on another Mac, that build is signed
# here instead. Signing is independent of the host architecture; nothing is
# compiled, and every executable keeps the entitlements its build gave it.
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ "$(uname -s)" != Darwin ]]; then echo 'Signing a Cage.app requires macOS.' >&2; exit 1; fi
if [[ $# != 1 || ! -d "$1" || "$1" != *.app ]]; then echo 'Usage: scripts/resign-macos-app.sh BUILT_CAGE_APP' >&2; exit 2; fi
: "${CAGE_SIGNING_IDENTITY:?Set CAGE_SIGNING_IDENTITY to your Developer ID Application identity.}"
: "${CAGE_NOTARY_PROFILE:?Set CAGE_NOTARY_PROFILE to a notarytool keychain profile.}"
identity="$CAGE_SIGNING_IDENTITY"
team="$(printf '%s' "$identity" | sed -n 's/^Developer ID Application: .* (\([A-Z0-9]\{10\}\))$/\1/p')"
if [[ -z "$team" ]]; then echo 'Release packages require a Developer ID Application identity.' >&2; exit 1; fi
notary_credentials=(--keychain-profile "$CAGE_NOTARY_PROFILE")
if [[ -n "${CAGE_NOTARY_KEYCHAIN:-}" ]]; then notary_credentials+=(--keychain "$CAGE_NOTARY_KEYCHAIN"); fi

source_app="$(cd "$1" && pwd -P)"
version="$(tr -d '\n\r' < VERSION)"
destination="$PWD/_output/Cage.app"
if [[ -e "$destination" ]]; then echo "$destination already exists. Move it away before signing another build." >&2; exit 1; fi
plist="$source_app/Contents/Info.plist"
if [[ "$(/usr/libexec/PlistBuddy -c 'Print :CFBundleIdentifier' "$plist")" != io.github.lee-xeo.claude-cage ||
      "$(/usr/libexec/PlistBuddy -c 'Print :CFBundleExecutable' "$plist")" != Cage ]]; then
  echo 'The source is not a Cage application.' >&2; exit 1
fi
if [[ "$(/usr/libexec/PlistBuddy -c 'Print :CageVersion' "$plist")" != "$version" ]]; then
  echo 'App version differs from VERSION; sign the build of this exact source.' >&2; exit 1
fi
# The build's own signature shows the App arrived complete and unchanged.
codesign --verify --deep --strict "$source_app"
if [[ -n "$(find "$source_app" -type l -print -quit)" ]]; then echo 'The App contains symbolic links; review it before signing.' >&2; exit 1; fi

mkdir -p _output
stage="$(mktemp -d "$PWD/_output/resign.XXXXXX")"
trap 'rm -rf "$stage"' EXIT
bundle="$stage/Cage.app"
ditto "$source_app" "$bundle"
chmod -R u+w "$bundle"
xattr -cr "$bundle"

# Only entitlements the build scripts grant may be carried to the release
# signature: third_party/lima/vz.entitlements, packaging/macos/cage-app.entitlements
# and QEMU's accel/hvf/entitlements.plist.
allowed=' com.apple.security.network.server com.apple.security.network.client com.apple.security.virtualization com.apple.security.device.audio-input com.apple.security.hypervisor '
entitlement_keys() {
  # Prints the sorted keys of an entitlements plist; fails on any value but true.
  /usr/libexec/PlistBuddy -c Print "$1" | awk '
    /^Dict \{$/ || /^\}$/ { next }
    /^    [^ ]+ = true$/ { print $1; next }
    { exit 1 }' | sort
}
main="$bundle/Contents/MacOS/Cage"
architecture="$(lipo -archs "$main")"
case "$architecture" in arm64|x86_64) ;; *) echo 'The App must be built for one architecture.' >&2; exit 1 ;; esac
sign=(codesign --force --sign "$identity" --timestamp --options runtime)
: > "$stage/executables"
count=0
while IFS= read -r -d '' file; do
  archs="$(lipo -archs "$file" 2>/dev/null)" || continue
  if [[ "$archs" != "$architecture" ]]; then echo "Unexpected architecture $archs: ${file#"$bundle"/}" >&2; exit 1; fi
  count=$((count + 1))
  entitlements="$stage/entitlements.$count.plist"
  codesign -d --entitlements - --xml "$file" > "$entitlements" 2>/dev/null || true
  keys=''
  if [[ -s "$entitlements" ]]; then
    keys="$(entitlement_keys "$entitlements")" || { echo "Unreadable entitlements: ${file#"$bundle"/}" >&2; exit 1; }
    for key in $keys; do
      case "$allowed" in *" $key "*) ;; *) echo "Unexpected entitlement $key: ${file#"$bundle"/}" >&2; exit 1 ;; esac
    done
  fi
  printf '%s\t%s\t%s\n' "$file" "$entitlements" "$(printf '%s' "$keys" | tr '\n' ' ')" >> "$stage/executables"
done < <(find "$bundle" -type f -print0)

main_entitlements=''
while IFS=$'\t' read -r file entitlements keys; do
  if [[ "$file" == "$main" ]]; then main_entitlements="$entitlements"; continue; fi
  if [[ -s "$entitlements" ]]; then "${sign[@]}" --entitlements "$entitlements" "$file"
  else "${sign[@]}" "$file"; fi
done < "$stage/executables"
if [[ -z "$main_entitlements" ]]; then echo 'The App has no main executable.' >&2; exit 1; fi
# Signing the bundle signs its main executable with the same entitlements.
if [[ -s "$main_entitlements" ]]; then "${sign[@]}" --entitlements "$main_entitlements" "$bundle"
else "${sign[@]}" "$bundle"; fi

codesign --verify --deep --strict --verbose=2 "$bundle"
while IFS=$'\t' read -r file entitlements keys; do
  details="$(codesign -dv --verbose=4 "$file" 2>&1)"
  if ! grep -qx "TeamIdentifier=$team" <<<"$details" || ! grep -Eq '^CodeDirectory .*flags=0x[0-9a-f]+\([a-z,-]*runtime[a-z,-]*\)' <<<"$details"; then
    echo "Not signed for release: ${file#"$bundle"/}" >&2; exit 1
  fi
  signed="$stage/signed.plist"
  codesign -d --entitlements - --xml "$file" > "$signed" 2>/dev/null || true
  after=''
  if [[ -s "$signed" ]]; then after="$(entitlement_keys "$signed" | tr '\n' ' ')"; fi
  if [[ "${after% }" != "${keys% }" ]]; then echo "Entitlements changed while signing: ${file#"$bundle"/}" >&2; exit 1; fi
  printf '%s\t%s\n' "${file#"$bundle"/}" "${keys:--}"
done < "$stage/executables"

ditto -c -k --keepParent "$bundle" "$stage/Cage-notarization.zip"
xcrun notarytool submit "$stage/Cage-notarization.zip" "${notary_credentials[@]}" \
  --wait --timeout 20m --output-format json > "$stage/app-notarization.json"
status="$(/usr/bin/plutil -extract status raw -o - "$stage/app-notarization.json")"
if [[ "$status" != Accepted ]]; then
  echo "Apple notarization: $status; submission $(/usr/bin/plutil -extract id raw -o - "$stage/app-notarization.json")" >&2; exit 1
fi
xcrun stapler staple "$bundle"
xcrun stapler validate "$bundle"
spctl --assess --type execute --verbose=2 "$bundle"
mv "$bundle" "$destination"
printf 'Signed and notarized %s App for %s: %s\nPackage it with: scripts/package-macos.sh --from-notarized-app\n' "$architecture" "$version" "$destination"
