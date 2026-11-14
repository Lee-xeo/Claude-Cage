#!/bin/bash
# Run as root inside a Cage macOS guest. The supplied directory contains the
# verified macFUSE 5.5.0 installer and the three project tools from this build.
set -euo pipefail
export PATH=/usr/bin:/bin:/usr/sbin:/sbin
fail() { printf '%s\n' "$*" >&2; exit 1; }
[[ $# == 1 || $# == 2 ]] || fail 'Usage: install-macos-project-tools.sh /path/to/root-owned-guest-project-kit [--gui-authorize]'
kit_arg=$1
gui_authorize=no
[[ ${2:-} == "" || ${2:-} == --gui-authorize ]] || fail 'Unknown installer mode.'
[[ ${2:-} == --gui-authorize ]] && gui_authorize=yes
[[ $kit_arg == /* ]] || fail 'Usage: install-macos-project-tools.sh /path/to/root-owned-guest-project-kit'
[[ $(uname -s) == Darwin && ( $(sysctl -n hw.model) =~ ^VirtualMac[0-9]+,[0-9]+$ || $(/usr/local/libexec/cage-guest-check --check >/dev/null 2>&1; echo $?) == 0 ) && $(id -u) == 0 ]] ||
    fail 'Run this installer in the target Cage guest as administrator.'
marker=/var/db/cage-guest-os
[[ -f $marker && ! -L $marker && $(stat -f %u "$marker") == 0 && $(cat "$marker") == macos ]] ||
    fail 'Missing the root-owned Cage guest marker.'
mode=$(stat -f %OLp "$marker")
(( (8#$mode & 022) == 0 )) || fail 'The guest marker is writable by another account.'

# Copy before verification, so a source directory cannot change after checks.
# Root-owned inputs are required because unsigned development builds can use a
# local ad-hoc signature. No downloaded code is executed to verify identity.
kit=$(cd -P "$1" && pwd)
[[ $(stat -f %u "$kit") == 0 && -z $(find "$kit" \( -type l -o ! -user root -o -perm -0020 -o -perm -0002 \) -print -quit) ]] ||
    fail 'Use a root-owned kit with no symlinks or group/other-writable members.'
parent=$(dirname "$kit")
while [[ $parent != / ]]; do
    mode=$(stat -f %Op "$parent")
    [[ $(stat -f %u "$parent") == 0 ]] && (( (8#$mode & 022) == 0 || (8#$mode & 01000) != 0 )) ||
        fail 'The kit can be replaced through an untrusted parent directory.'
    parent=$(dirname "$parent")
done
for file in macfuse.pkg cage-sshfs cage-macos-terminal; do [[ -f $kit/$file ]] || fail "Missing $file"; done
[[ -d $kit/CageProjectAccess.app ]] || fail 'Missing CageProjectAccess.app.'
umask 077
work=$(mktemp -d /private/var/tmp/cage-project-kit.XXXXXXXX)
trap 'rm -rf "$work"' EXIT
for file in macfuse.pkg cage-sshfs cage-macos-terminal; do cp "$kit/$file" "$work/$file"; done
ditto "$kit/CageProjectAccess.app" "$work/CageProjectAccess.app"
pkg="$work/macfuse.pkg"
[[ $(shasum -a 256 "$pkg" | cut -d' ' -f1) == 5acf9e30bab227707de8d15eb28472a0cc9ed45709846b3e067d283294dc7f9e ]] ||
    fail 'The macFUSE installer SHA-256 does not match the pinned release.'
pkgutil --check-signature "$pkg" | grep -F 'Developer ID Installer: Benjamin Fleischer (3T5GSNBU6W)' >/dev/null ||
    fail 'Unexpected macFUSE installer signer.'
CAGE_MACOS_GUEST_ARCH=$(uname -m)
. "$(dirname "$0")/macos-guest-arch.sh"
for file in cage-sshfs cage-macos-terminal; do
    codesign --verify --strict --architecture "$CAGE_MACOS_NATIVE_ARCH" "$work/$file"
done
access_id=dev.cage.guest.project-access
codesign --verify --strict -R "=identifier \"$access_id\"" "$work/CageProjectAccess.app"
macfuse_requirement='anchor apple generic and identifier "io.macfuse.app" and certificate leaf[subject.OU] = "3T5GSNBU6W"'
driver=/Library/Filesystems/macfuse.fs
vendor_app="$driver/Contents/Resources/macfuse.app"
pinned_version='5.5.0'
installed=''
if [[ -e $driver ]]; then
    codesign --verify --strict -R "=$macfuse_requirement" "$vendor_app"
    installed=$(plutil -extract CFBundleShortVersionString raw -o - "$driver/Contents/Info.plist")
fi
# Read the receipt before deciding anything: a stale or missing receipt next
# to an up-to-date bundle is the state that used to report a retry that could
# never change anything.
receipt=$(pkgutil --pkg-info-plist io.macfuse.installer.components.core 2>/dev/null |
    plutil -extract pkg-version raw -o - - 2>/dev/null) || receipt=''
normalize_version() {
    local version=$1 major minor patch
    [[ $version =~ ^([0-9]+)\.([0-9]+)(\.([0-9]+))?$ ]] || return 1
    major=$((10#${BASH_REMATCH[1]}))
    minor=$((10#${BASH_REMATCH[2]}))
    patch=${BASH_REMATCH[4]:-0}
    patch=$((10#$patch))
    printf '%d.%d.%d' "$major" "$minor" "$patch"
}
is_supported_version() {
    local normalized major minor patch
    normalized=$(normalize_version "$1") || return 1
    IFS=. read -r major minor patch <<<"$normalized"
    (( major > 5 || (major == 5 && minor >= 5) ))
}
version_cmp() {
    local a=$1 b=$2 a_major a_minor a_patch b_major b_minor b_patch
    IFS=. read -r a_major a_minor a_patch <<<"$a"
    IFS=. read -r b_major b_minor b_patch <<<"$b"
    if (( a_major != b_major )); then (( a_major < b_major )) && printf '<\n' || printf '>\n'
    elif (( a_minor != b_minor )); then (( a_minor < b_minor )) && printf '<\n' || printf '>\n'
    elif (( a_patch != b_patch )); then (( a_patch < b_patch )) && printf '<\n' || printf '>\n'
    else printf '=\n'; fi
}

# Decide what to do with an existing macFUSE installation given the pinned
# installer: install fresh, upgrade an older one, repair a version/receipt
# mismatch, or nothing. Sets macfuse_action on success. For states the pinned
# installer cannot safely fix, sets decision_error and fails; the existing
# installation must be kept and diagnosed, never downgraded or removed.
decide_macfuse_action() {
    macfuse_action=''
    local installed=$1 receipt=$2
    local normalized_installed normalized_receipt comparison
    if [[ -z $installed ]]; then macfuse_action=install; return 0; fi
    normalized_installed=$(normalize_version "$installed") || {
        decision_error="The installed macFUSE version '$installed' is unrecognized. Keep the existing installation and report diagnostics from: plutil -p $driver/Contents/Info.plist and pkgutil --pkg-info io.macfuse.installer.components.core; this script will not replace a build it cannot identify."
        return 1
    }
    comparison=$(version_cmp "$normalized_installed" "$pinned_version")
    if [[ $comparison == '>' ]]; then
        # A newer installation whose receipt agrees is simply up to date.
        normalized_receipt=$(normalize_version "$receipt") &&
            [[ $normalized_receipt == "$normalized_installed" ]] &&
            { macfuse_action=none; return 0; }
        decision_error="The installed macFUSE $normalized_installed is newer than the pinned $pinned_version installer and its installation receipt ('$receipt') does not match, so repairing would downgrade macFUSE. Keep the existing installation and report diagnostics from: plutil -p $driver/Contents/Info.plist and pkgutil --pkg-info io.macfuse.installer.components.core."
        return 1
    fi
    if [[ $comparison == '<' ]]; then macfuse_action=upgrade; return 0; fi
    # The bundle matches the pinned release; its receipt must agree with it.
    [[ -z $receipt ]] && { macfuse_action=repair; return 0; }
    normalized_receipt=$(normalize_version "$receipt") || { macfuse_action=repair; return 0; }
    [[ $normalized_receipt == "$normalized_installed" ]] && { macfuse_action=none; return 0; }
    macfuse_action=repair
}

# The direct root installer is rejected by this guest's system policy, so an
# existing installation may only be upgraded or repaired through the Apple
# Installer GUI.
require_gui_install_mode() {
    [[ $1 == install ]] && return 0
    decision_error='An existing macFUSE installation must be upgraded or repaired through the Apple Installer GUI. Log in to the intended guest session and rerun with --gui-authorize.'
    return 1
}
decision_error=''
decide_macfuse_action "$installed" "$receipt" ||
    fail "${decision_error:-Cannot decide what to do with the installed macFUSE.}"
action=$macfuse_action
deferred=no
if [[ $action != none ]]; then
    if [[ $action == install && $CAGE_MACOS_NATIVE_ARCH == x86_64 && $gui_authorize != yes ]]; then
    destination=/usr/local/share/cage/setup
    for folder in /usr/local/share /usr/local/share/cage "$destination"; do
        [[ ! -L $folder ]] || fail 'Linked macFUSE setup directory.'
        if [[ -e $folder ]]; then
            mode=$(stat -f %OLp "$folder")
            [[ -d $folder && $(stat -f %u "$folder") == 0 ]] && (( (8#$mode & 022) == 0 )) || fail 'Untrusted macFUSE setup directory.'
        else
            install -d -o root -g wheel -m 755 "$folder"
        fi
    done
    [[ ! -L $destination/macfuse.pkg ]] || fail 'Linked macFUSE setup package.'
    staged=$(mktemp "$destination/.macfuse.XXXXXXXX")
    install -o root -g wheel -m 644 "$pkg" "$staged"
    mv -f "$staged" "$destination/macfuse.pkg"
    deferred=yes
    elif [[ $gui_authorize != yes ]]; then
        require_gui_install_mode "$action" || fail "$decision_error"
        installer -pkg "$pkg" -target /
    else
        gui_uid=$(stat -f '%u' /dev/console)
        (( gui_uid >= 500 )) || fail 'Log in to the intended guest account before authorizing macFUSE.'
        launchctl print "gui/$gui_uid" >/dev/null 2>&1 || fail 'The intended guest GUI session is unavailable.'
        [[ $(stat -f '%Su' /dev/console) == "${CAGE_MACOS_GUI_USER:-}" ]] ||
            fail 'The guest GUI account does not match the pending installation.'
        chmod 755 "$work"
        chmod 644 "$pkg"
        launchctl asuser "$gui_uid" sudo -n -H -u "#$gui_uid" -- /usr/bin/open -W -a /System/Library/CoreServices/Installer.app "$pkg" ||
            fail 'Apple Installer was cancelled or failed. Installation can be retried.'
    fi
fi
if [[ $deferred == no ]]; then
if ! codesign --verify --strict -R "=$macfuse_requirement" "$vendor_app"; then
    fail 'Apple Installer did not complete the verified macFUSE installation. Installation can be retried.'
fi
installed=$(plutil -extract CFBundleShortVersionString raw -o - "$driver/Contents/Info.plist") ||
    fail 'Apple Installer did not complete the verified macFUSE installation. Installation can be retried.'
is_supported_version "$installed" ||
    fail "Apple Installer left macFUSE '$installed', which is not a supported version. If the installer was cancelled, rerun the installation; otherwise inspect $driver."
normalized_installed=$(normalize_version "$installed") ||
    fail "Apple Installer left an unrecognized macFUSE version '$installed'. Inspect $driver instead of retrying."
receipt=$(pkgutil --pkg-info-plist io.macfuse.installer.components.core 2>/dev/null |
    plutil -extract pkg-version raw -o - - 2>/dev/null) || receipt=''
[[ -n $receipt ]] ||
    fail 'The macFUSE installation receipt is missing. Rerun the installation; if it keeps failing, keep the installation and report diagnostics from: pkgutil --pkg-info-plist io.macfuse.installer.components.core'
normalized_receipt=$(normalize_version "$receipt") ||
    fail "Unrecognized macFUSE installation receipt version '$receipt'. Rerun the installation; if it persists, keep the installation and report diagnostics from: pkgutil --pkg-info-plist io.macfuse.installer.components.core"
[[ $normalized_receipt == "$normalized_installed" ]] ||
    fail "The macFUSE installation receipt reports $normalized_receipt but macFUSE $normalized_installed is installed. Rerun the installation; if the mismatch persists, keep the installation and report diagnostics from: pkgutil --pkg-info-plist io.macfuse.installer.components.core and plutil -p $driver/Contents/Info.plist"

fi

# Move a verified staged bundle into place. The installed copy is moved aside
# first and rolls back if the new bundle cannot land, so a failed replacement
# never destroys the only usable bundle; an interruption between the two moves
# leaves the previous copy in the move-aside directory.
swap_dir() { # staged_dir target
    local new=$1 target=$2 old
    old=$(mktemp -d "$(dirname "$target")/.cage-project-old.XXXXXXXX")
    if mv "$target" "$old/app"; then
        if mv "$new" "$target"; then
            rm -rf "$old"
            return 0
        fi
        if mv "$old/app" "$target" 2>/dev/null; then rm -rf "$old"; fi
        return 1
    fi
    rm -rf "$old"
    return 1
}

install_app() {
    local source=$1 target=$2 requirement=$3 stage
    if [[ -e $target || -L $target ]]; then
        [[ ! -L $target && -d $target ]] || fail "Unexpected application at $target"
        codesign --verify --strict -R "=$requirement" "$target"
        # A resumed pending installation may carry a differently signed build
        # of the same verified identifier. Replace it with this kit's verified
        # copy so the published component manifest matches.
        stage=$(mktemp -d /Applications/.cage-project-install.XXXXXXXX)
        if ditto "$source" "$stage/app" && chmod -R a+rX,go-w "$stage/app" &&
            codesign --verify --strict -R "=$requirement" "$stage/app" &&
            swap_dir "$stage/app" "$target"; then
            rmdir "$stage"
            return
        fi
        rm -rf "$stage"
        fail "Could not refresh $target with this kit's verified build"
    fi
    stage=$(mktemp -d /Applications/.cage-project-install.XXXXXXXX)
    if ditto "$source" "$stage/app" && chmod -R a+rX,go-w "$stage/app" &&
        codesign --verify --strict -R "=$requirement" "$stage/app" && mv "$stage/app" "$target"; then
        rmdir "$stage"
    else
        rm -rf "$stage"
        fail "Could not install $target"
    fi
}
if [[ $deferred == no ]]; then install_app "$vendor_app" /Applications/macFUSE.app "$macfuse_requirement"; fi
install_app "$work/CageProjectAccess.app" /Applications/CageProjectAccess.app "identifier \"$access_id\""
# Staging and move-aside directories left by an interrupted earlier run are
# removed only now that both applications are verified in place.
rm -rf /Applications/.cage-project-install.* /Applications/.cage-project-old.*
libexec=/usr/local/libexec
mkdir -p "$libexec"
for file in cage-sshfs cage-macos-terminal; do
    [[ ! -L $libexec/$file ]] || fail "Refusing to replace a linked executable: $file"
    if [[ -f $libexec/$file ]] && cmp -s "$work/$file" "$libexec/$file"; then continue; fi
    stage=$(mktemp "$libexec/.cage-tool.XXXXXXXX")
    if install -o root -g wheel -m 755 "$work/$file" "$stage" && mv -f "$stage" "$libexec/$file"; then :; else
        rm -f "$stage"
        fail "Could not install $file"
    fi
done
printf '%s\n' 'Project tools are staged. Complete macFUSE installation and enable macFUSE and macFUSE (local) through Cage first setup.'
