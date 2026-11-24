#!/bin/bash
# Build the Intel Mac runtime from public source. No guest disk is an input.
set -euo pipefail

# Pinned pure-Python wheels used only by QEMU's mkvenv bootstrap (MIT /
# Apache-2.0 OR BSD-2-Clause licensed, never bundled into Cage.app). Kept in
# sync with packaging/windows/dependencies.json.
QEMU_PYTHON_WHEELS=(
    "pip-26.3-py3-none-any.whl|https://files.pythonhosted.org/packages/07/25/ee8c38ae72d66b844ce8eb9711ba905dba21ca1a8a0c960057c41cd23337/pip-26.3-py3-none-any.whl|f3b60b20ffaadd4415ed7957212a38ac63fa4c5fa375bd0f4a797777940fad13"
    "setuptools-84.2.1-py3-none-any.whl|https://files.pythonhosted.org/packages/ef/55/6dc6d6bb953bdfc52519a0059592c52b6e3b8ad51cf8256dffceb55b0610/setuptools-84.2.1-py3-none-any.whl|ca140b45d5325a9478f880e351484f62fef15f336462e449ca0c03c873d589c2"
    "wheel-0.48.0-py3-none-any.whl|https://files.pythonhosted.org/packages/2e/29/69cfbb602cd91690c55d38ba9fe53e6a7e76a6fa647bf38f19c138d25449/wheel-0.48.0-py3-none-any.whl|3217dcc807155e45db462d7ef2431f5ddda0d7273b700d05a67b271ceb1287ab"
    "packaging-26.4-py3-none-any.whl|https://files.pythonhosted.org/packages/ba/7b/5504f91c9c2733f693d7759e50ede5d27119d4da7e5836c46d3b5ff81d56/packaging-26.4-py3-none-any.whl|81383611a09c1bc0b08b76e0fd5829d7c6824c65b27a6aee339da133f4233ea2"
)

download_to() {
    local dest_dir=$1 name=$2 url=$3 expected=$4
    local file="$dest_dir/$name"
    mkdir -p "$dest_dir" || return 1
    if [[ ! -f $file ]] || [[ $(shasum -a 256 "$file" | cut -d' ' -f1) != "$expected" ]]; then
        curl --disable -fLsS --proto '=https' --proto-redir '=https' --retry 2 "$url" -o "$file.partial" || {
            rm -f "$file.partial"
            return 1
        }
        if [[ $(shasum -a 256 "$file.partial" | cut -d' ' -f1) != "$expected" ]]; then
            rm -f "$file.partial"
            return 1
        fi
        mv "$file.partial" "$file" || return 1
    fi
}

prepare_qemu_python_wheels() {
    local python=$1 wheel_dir=$2
    export PIP_DISABLE_PIP_VERSION_CHECK=1
    if [[ -n "${PIP_FIND_LINKS:-}" ]]; then
        export PIP_NO_INDEX=1
        return 0
    fi
    case "${PIP_NO_INDEX:-}" in
        1|[Tt][Rr][Uu][Ee]|[Yy][Ee][Ss]|[Oo][Nn])
            if "$python" -c 'import pip, wheel, qemu.qmp' >/dev/null 2>&1; then
                export PIP_NO_INDEX=1
                return 0
            fi
            ;;
    esac
    local entry name url expected
    for entry in "${QEMU_PYTHON_WHEELS[@]}"; do
        IFS='|' read -r name url expected <<< "$entry"
        if ! download_to "$wheel_dir" "$name" "$url" "$expected"; then
            printf 'Failed to fetch QEMU Python build wheel %s. For offline or PyPI-restricted networks, place the verified wheels in %s, set PIP_FIND_LINKS, or pre-install pip/wheel/qemu.qmp in CAGE_BUILD_PYTHON with PIP_NO_INDEX=1 (see docs/intel-macos-development.md).\n' "$name" "$wheel_dir" >&2
            return 1
        fi
    done
    # pip splits PIP_FIND_LINKS on whitespace. A file URI keeps paths with
    # spaces or non-ASCII characters as one verified offline location.
    PIP_FIND_LINKS=$("$python" -c 'from pathlib import Path; import sys; print(Path(sys.argv[1]).resolve().as_uri())' "$wheel_dir")
    export PIP_NO_INDEX=1 PIP_FIND_LINKS
}

if [[ "${BASH_SOURCE[0]}" != "$0" ]]; then
    return 0
fi

cd "$(dirname "$0")/.."
[[ $(uname -s) == Darwin && $(uname -m) == x86_64 && $# == 1 && $1 == /* && ! -e $1 ]] || exit 2
destination=$1
cache="$PWD/_output/intel-runtime"
mkdir -p "$cache/downloads" "$cache/source"
download() {
    download_to "$cache/downloads" "$@"
}
download qemu-11.1.3.tar.xz https://download.qemu.org/qemu-11.1.3.tar.xz 5a8289c7940dc77d3873c81a6c29ba3afe1446fc9d36e77fa1658cdbecb84b22
download glib-2.88.3-source.tar.xz https://download.gnome.org/sources/glib/2.88/glib-2.88.3.tar.xz ab24d24e698dfa1e408b7bcdb508f4aafc906185a8b8ce72fdf79bbbdc9b383b
download pcre2-10.47.tar.bz2 https://github.com/PCRE2Project/pcre2/releases/download/pcre2-10.47/pcre2-10.47.tar.bz2 47fe8c99461250d42f89e6e8fdaeba9da057855d06eb7fc08d9ca03fd08d7bc7
if [[ ! -d $cache/source/qemu-11.1.3 ]]; then tar -xf "$cache/downloads/qemu-11.1.3.tar.xz" -C "$cache/source"; fi
python=${CAGE_BUILD_PYTHON:-python3}
"$python" -c 'import sys; assert sys.version_info >= (3, 11), "QEMU build needs Python 3.11 or newer"'
command -v ninja >/dev/null
command -v pkg-config >/dev/null
export MACOSX_DEPLOYMENT_TARGET=14.0
sdk=$(xcrun --show-sdk-path)
mkdir -p "$cache/pkgconfig"
# Homebrew keeps some formulae, such as pixman, outside the default pkg-config
# search path. Add the installed ones that QEMU links against.
if command -v brew >/dev/null; then
    for formula in pixman libffi; do
        if prefix=$(brew --prefix "$formula" 2>/dev/null) && [[ -d $prefix/lib/pkgconfig ]]; then
            PKG_CONFIG_PATH="$prefix/lib/pkgconfig${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}"
        fi
    done
    export PKG_CONFIG_PATH
fi
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
        prepare_qemu_python_wheels "$python" "$cache/python-wheels"
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
