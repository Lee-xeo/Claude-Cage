# Sourced by the macOS build scripts. Checks the host toolchain before the long
# build and keeps caches inside the workspace when the user cache locations are
# not writable (for example in an agent sandbox that only allows the checkout).
# Sets swift_flags for every xcrun swiftc / xcrun swift call; expand it as
# ${swift_flags[@]+"${swift_flags[@]}"} so an empty array works under set -u
# in the bash 3.2 that ships with macOS.

macos_build_cache="$PWD/_output/cache"
swift_flags=()

macos_writable_directory() {
  local directory="$1"
  [[ -n "$directory" ]] || return 1
  mkdir -p "$directory" 2>/dev/null || return 1
  local probe
  probe="$(mktemp "$directory/.cage-write-check.XXXXXX" 2>/dev/null)" || return 1
  rm -f "$probe"
}

macos_prepare_caches() {
  # macOS mktemp ignores TMPDIR for default templates; the scripts pass
  # templates under _output, so only tool caches need redirecting here.
  if ! macos_writable_directory "${npm_config_cache:-$HOME/.npm}"; then
    export npm_config_cache="$macos_build_cache/npm"
    mkdir -p "$npm_config_cache"
    printf 'npm cache is not writable; using %s\n' "$npm_config_cache" >&2
  fi
  if ! macos_writable_directory "${UV_CACHE_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/uv}"; then
    export UV_CACHE_DIR="$macos_build_cache/uv"
    mkdir -p "$UV_CACHE_DIR"
    printf 'uv cache is not writable; using %s\n' "$UV_CACHE_DIR" >&2
  fi
  local user_cache
  user_cache="$(getconf DARWIN_USER_CACHE_DIR 2>/dev/null || true)"
  if ! macos_writable_directory "${user_cache:+${user_cache%/}/clang/ModuleCache}"; then
    local modules="$macos_build_cache/swift-modules"
    mkdir -p "$modules"
    # swiftc needs both: its own module cache and the one ClangImporter uses.
    swift_flags=(-module-cache-path "$modules" -Xcc "-fmodules-cache-path=$modules")
    printf 'Swift module cache is not writable; using %s\n' "$modules" >&2
  fi
}

macos_link_probe() {
  local directory="$1"
  printf 'int main(void) { return 0; }\n' > "$directory/probe.c"
  xcrun clang -mmacosx-version-min=14.0 -framework CoreFoundation -framework Security \
    "$directory/probe.c" -o "$directory/probe" 2> "$directory/probe.log"
}

macos_swift_probe() {
  local directory="$1"
  printf 'import Cocoa\n' > "$directory/probe.swift"
  xcrun swiftc ${swift_flags[@]+"${swift_flags[@]}"} -typecheck "$directory/probe.swift" 2> "$directory/swift.log"
}

macos_swift_sdk_mismatch() {
  grep -Eq 'this SDK is not supported by the compiler|compiled with a newer version of the compiler' "$1"
}

macos_sdk_roots() {
  local developer
  developer="$(xcode-select -p 2>/dev/null || true)"
  if [[ -n "$developer" ]]; then
    printf '%s\n' "$developer/SDKs" "$developer/Platforms/MacOSX.platform/Developer/SDKs"
  fi
  printf '%s\n' /Library/Developer/CommandLineTools/SDKs
}

macos_list_installed_sdks() {
  local root path name
  while IFS= read -r root; do
    [[ -d "$root" ]] || continue
    (find "$root" -maxdepth 1 -name 'MacOSX[0-9]*.sdk' -type d 2>/dev/null || true) | while IFS= read -r path; do
      name="${path##*/MacOSX}"
      printf '%s\t%s\n' "${name%.sdk}" "$path"
    done
  done < <(macos_sdk_roots) | sort -V -r | awk -F '\t' '!seen[$2]++ { print $2 }'
}

# Probes installed SDKs newest-to-oldest in an isolated subdirectory so failing
# candidates never overwrite the original default-SDK error logs.
macos_select_compatible_sdk() {
  local probe="$1" reason="$2" require_swift="$3"
  local candidate="$probe/candidate"
  mkdir -p "$candidate"
  local sdk
  while IFS= read -r sdk; do
    [[ -n "$sdk" ]] || continue
    if ! SDKROOT="$sdk" macos_link_probe "$candidate"; then
      continue
    fi
    if [[ "$require_swift" == true ]] && ! SDKROOT="$sdk" macos_swift_probe "$candidate"; then
      continue
    fi
    export SDKROOT="$sdk"
    printf 'The default macOS SDK is newer than this %s; using %s\n' "$reason" "$SDKROOT" >&2
    return 0
  done < <(macos_list_installed_sdks)
  return 1
}

macos_check_toolchain() {
  mkdir -p "$PWD/_output"
  local probe
  probe="$(mktemp -d "$PWD/_output/toolchain-check.XXXXXX")"
  # cgo links against the default SDK. Command Line Tools can install an SDK
  # newer than their clang understands (tapi: unknown architecture arm64e.x1) or
  # newer than their swiftc can import (this SDK is not supported by the compiler).
  if ! macos_link_probe "$probe"; then
    if [[ -z "${SDKROOT:-}" ]] && grep -q 'unknown architecture' "$probe/probe.log"; then
      if macos_select_compatible_sdk "$probe" clang true; then
        rm -rf "$probe"
        return 0
      fi
      # No installed SDK passed both clang and swiftc; if one can still link C,
      # select it so the Swift probe below reports the real Swift failure.
      macos_select_compatible_sdk "$probe" clang false || true
    fi
    if [[ -z "${SDKROOT:-}" ]] || ! macos_link_probe "$probe"; then
      cat "$probe/probe.log" >&2
      echo 'The macOS C toolchain cannot link a minimal program. If the error mentions an unknown architecture, the default SDK is newer than clang: update Xcode or the Command Line Tools, or set SDKROOT to an older SDK from xcrun --show-sdk-path.' >&2
      rm -rf "$probe"
      exit 1
    fi
  fi
  if ! macos_swift_probe "$probe"; then
    if [[ -z "${SDKROOT:-}" ]] && macos_swift_sdk_mismatch "$probe/swift.log"; then
      if macos_select_compatible_sdk "$probe" swiftc true; then
        rm -rf "$probe"
        return 0
      fi
    fi
    cat "$probe/swift.log" >&2
    if grep -q "redefinition of module 'SwiftBridging'" "$probe/swift.log"; then
      echo 'The Command Line Tools contain leftover module maps from an older version (both usr/include/swift/module.modulemap and bridging.modulemap define SwiftBridging). Reinstall the Command Line Tools, or select Xcode with xcode-select, then retry.' >&2
    elif macos_swift_sdk_mismatch "$probe/swift.log"; then
      echo 'The selected macOS SDK is newer than xcrun swiftc. Update Xcode or the Command Line Tools, or set SDKROOT to an older installed SDK that matches this swiftc (check xcrun --show-sdk-path and the SDKs directory).' >&2
    else
      echo 'xcrun swiftc cannot import Cocoa. Check xcode-select -p and the installed Xcode or Command Line Tools.' >&2
    fi
    rm -rf "$probe"
    exit 1
  fi
  rm -rf "$probe"
}

# File Provider (iCloud Drive, other sync clients) adds Finder information and
# provider attributes that codesign rejects as detritus. Signing covers only
# the bundle's contents, so clear extended attributes first. Licenses copied
# from the Go module cache are 0444, and xattr cannot remove attributes such
# as com.apple.provenance from a file its owner cannot write.
macos_clear_bundle_attributes() {
  chmod -R u+w "$1" 2>/dev/null || true
  if ! xattr -cr "$1" 2>/dev/null; then
    echo "Could not clear extended attributes in $1. If codesign reports detritus, build from a directory that is not managed by a file sync client." >&2
  fi
}
