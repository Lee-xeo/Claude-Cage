#!/bin/bash
# Build the Intel Mac runtime from public source. No guest disk is an input.
set -euo pipefail
cd "$(dirname "$0")/.."
[[ $(uname -s) == Darwin && $(uname -m) == x86_64 && $# == 1 && $1 == /* && ! -e $1 ]] || exit 2
destination=$1
cache="$PWD/_output/intel-runtime"
mkdir -p "$cache/downloads" "$cache/source"
download() {
    local name=$1 url=$2 expected=$3 file="$cache/downloads/$1"
    if [[ ! -f $file ]] || [[ $(shasum -a 256 "$file" | cut -d' ' -f1) != "$expected" ]]; then
        curl --disable -fLsS --proto '=https' --proto-redir '=https' --retry 2 "$url" -o "$file.partial"
        [[ $(shasum -a 256 "$file.partial" | cut -d' ' -f1) == "$expected" ]] || exit 1
        mv "$file.partial" "$file"
    fi
}
download qemu-11.1.3.tar.xz https://download.qemu.org/qemu-11.1.3.tar.xz 5a8289c7940dc77d3873c81a6c29ba3afe1446fc9d36e77fa1658cdbecb84b22
download OpenCore-1.0.8-RELEASE.zip https://github.com/acidanthera/OpenCorePkg/releases/download/1.0.8/OpenCore-1.0.8-RELEASE.zip 2011e8b7216ecb2645d97ea710df965947edd406846e92050b9bc4190be6d27b
download Lilu-1.7.2-RELEASE.zip https://github.com/acidanthera/Lilu/releases/download/1.7.2/Lilu-1.7.2-RELEASE.zip 53967d7dcfaab01023a33df2e969a89522f13d6654a6a56ac4711b62dabf3ab8
download VirtualSMC-1.3.8-RELEASE.zip https://github.com/acidanthera/VirtualSMC/releases/download/1.3.8/VirtualSMC-1.3.8-RELEASE.zip 2e29a8aaf91b4eb0bcdf5913ed17e70f5cf76531122acd4132eff7813a54de8b
download glib-2.88.3-source.tar.xz https://download.gnome.org/sources/glib/2.88/glib-2.88.3.tar.xz ab24d24e698dfa1e408b7bcdb508f4aafc906185a8b8ce72fdf79bbbdc9b383b
download pcre2-10.47.tar.bz2 https://github.com/PCRE2Project/pcre2/releases/download/pcre2-10.47/pcre2-10.47.tar.bz2 47fe8c99461250d42f89e6e8fdaeba9da057855d06eb7fc08d9ca03fd08d7bc7
for component in OpenCore-1.0.8 Lilu-1.7.2 VirtualSMC-1.3.8; do
    if [[ ! -d $cache/source/$component ]]; then
        mkdir "$cache/source/$component"
        unzip -q "$cache/downloads/$component-RELEASE.zip" -d "$cache/source/$component"
    fi
done
if [[ ! -d $cache/source/qemu-11.1.3 ]]; then tar -xf "$cache/downloads/qemu-11.1.3.tar.xz" -C "$cache/source"; fi
python=${CAGE_BUILD_PYTHON:-python3}
"$python" -c 'import sys; assert sys.version_info >= (3, 11), "QEMU build needs Python 3.11 or newer"'
command -v ninja >/dev/null
command -v pkg-config >/dev/null
export MACOSX_DEPLOYMENT_TARGET=14.0
sdk=$(xcrun --show-sdk-path)
mkdir -p "$cache/pkgconfig"
ffi_pc=$(pkg-config --variable=pcfiledir libffi)/libffi.pc
sed "s#^homebrew_sdkroot=.*#homebrew_sdkroot=$sdk#" "$ffi_pc" > "$cache/pkgconfig/libffi.pc"
export PKG_CONFIG_PATH="$cache/pkgconfig:${PKG_CONFIG_PATH:-}"
if [[ ! -d $cache/source/pcre2-10.47 ]]; then tar -xf "$cache/downloads/pcre2-10.47.tar.bz2" -C "$cache/source"; fi
if [[ ! -f $cache/dependencies/lib/libpcre2-8.a ]]; then
    mkdir -p "$cache/pcre-build"
    (
        cd "$cache/pcre-build"
        ../source/pcre2-10.47/configure --prefix="$cache/dependencies" --enable-shared --enable-static \
            --enable-jit --disable-pcre2grep --disable-pcre2test
        make -j "${CAGE_BUILD_JOBS:-8}"
        make install
    )
fi
export PKG_CONFIG_PATH="$cache/dependencies/lib/pkgconfig:$PKG_CONFIG_PATH"
if [[ ! -d $cache/source/glib-2.88.3 ]]; then tar -xf "$cache/downloads/glib-2.88.3-source.tar.xz" -C "$cache/source"; fi
if [[ ! -f $cache/glib-build/.cage-sdk || $(cat "$cache/glib-build/.cage-sdk") != "$sdk pcre2-10.47 shared-static" ]]; then
    setup=(meson setup)
    if [[ -f $cache/glib-build/build.ninja ]]; then setup+=(--wipe); fi
    "${setup[@]}" "$cache/glib-build" "$cache/source/glib-2.88.3" --prefix="$cache/dependencies" \
        -Ddefault_library=both -Dintrospection=disabled -Dtests=false -Dinstalled_tests=false -Ddocumentation=false -Dnls=disabled
    printf '%s\n' "$sdk pcre2-10.47 shared-static" > "$cache/glib-build/.cage-sdk"
fi
ninja -C "$cache/glib-build" -j "${CAGE_BUILD_JOBS:-8}"
ninja -C "$cache/glib-build" install
export PKG_CONFIG_PATH="$cache/dependencies/lib/pkgconfig:${PKG_CONFIG_PATH:-}"
mkdir -p "$cache/build"
(
    cd "$cache/build"
    if [[ ! -f build.ninja || ! -f .cage-private-glib ]]; then
        ../source/qemu-11.1.3/configure --target-list=x86_64-softmmu \
            --prefix="$cache/prefix" --python="$(command -v "$python")" \
            --without-default-features --enable-system --enable-tools \
            --enable-hvf --enable-pvg --enable-pixman --enable-install-blobs \
            --disable-docs --disable-guest-agent --disable-werror
        touch .cage-private-glib
    fi
    ninja -j "${CAGE_BUILD_JOBS:-8}"
    ninja install
)
"$python" scripts/package-intel-macos-runtime.py "$cache" "$destination" "${CAGE_SIGNING_IDENTITY:--}"
