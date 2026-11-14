#!/bin/bash
# Executed as root over the authenticated provisioning channel of a fresh VM.
set -euo pipefail
export PATH=/usr/bin:/bin:/usr/sbin:/sbin
[[ $EUID == 0 && $(uname -s) == Darwin && ( $(sysctl -n hw.model) =~ ^VirtualMac[0-9]+,[0-9]+$ || $(/usr/local/libexec/cage-guest-check --check >/dev/null 2>&1; echo $?) == 0 ) ]] || exit 1
kit=$(cd "$1" && pwd -P)
[[ $(stat -f %u "$kit") == 0 ]] || exit 1
umask 022
marker=/var/db/cage-guest-os
if [[ ! -e $marker ]]; then echo macos > "$marker"; fi
[[ -f $marker && ! -L $marker && $(stat -f %u "$marker") == 0 && $(cat "$marker") == macos ]] || exit 1
# The password is supplied on stdin and is never a command line argument.
IFS= read -r login_password
[[ -n $login_password ]] || exit 1
cd /
provision=$3
[[ $provision == cageprovision ]] || exit 1
ordinary=${4:-cage}
[[ $ordinary =~ ^[a-z][a-z0-9_]{0,30}$ && $ordinary != root && $ordinary != cageprovision && $ordinary != cagesetup ]] || exit 1
full_name=$ordinary
/usr/bin/dscl . -create "/Users/$provision" IsHidden 1
claim=/var/db/cage-install-$ordinary
if [[ ! -e $claim ]]; then
    if id $ordinary >/dev/null 2>&1 || [[ -e /Users/$ordinary ]]; then echo 'An existing account uses the Cage guest account name.' >&2; exit 1; fi
    install -m 600 /dev/null "$claim"
fi
[[ -f $claim && ! -L $claim && $(stat -f %u "$claim") == 0 ]] || exit 1
if ! id $ordinary >/dev/null 2>&1; then
    [[ ! -e /Users/$ordinary ]] || exit 1
    printf '%s\n' "$login_password" | /usr/sbin/sysadminctl -addUser $ordinary -fullName "$full_name" -password - -home /Users/$ordinary
    id $ordinary >/dev/null
    if [[ ! -e /Users/$ordinary ]]; then /usr/sbin/createhomedir -c -u $ordinary; fi
fi
[[ $(dscl . -read /Users/$ordinary NFSHomeDirectory) == "NFSHomeDirectory: /Users/$ordinary" && " $(id -Gn $ordinary) " != *' admin '* ]] || exit 1
if ! printf '%s\n' "$login_password" | dscl /Local/Default -authonly $ordinary >/dev/null; then
    echo '保存的客机凭据与安装账户不匹配，请核对加密保险箱。' >&2
    exit 1
fi
install -d -m 755 /usr/local/libexec
install -m 755 "$kit/cage-finish-initial-setup" /usr/local/libexec/cage-finish-initial-setup
printf '%s\n' "$login_password" | /usr/local/libexec/cage-finish-initial-setup prepare "$2" "$ordinary"
install -m 755 "$kit/configure-macos-session.sh" /usr/local/libexec/cage-configure-session
printf '%s\n' "$login_password" | /usr/local/libexec/cage-configure-session "$ordinary" "$2"
unset login_password
[[ ! -L /Users/$ordinary && $(stat -f %u /Users/$ordinary) == "$(id -u $ordinary)" ]] || exit 1
# Account-controlled paths are opened with that account's privileges.
cat "/Users/$provision/.ssh/authorized_keys" | sudo -Hu $ordinary /bin/sh -ec '
    umask 077
    mkdir -p "$HOME/.ssh"
    file="$HOME/.ssh/authorized_keys"
    if [ -e "$file" ] || [ -L "$file" ]; then cmp -s - "$file"; else (set -C; cat > "$file"); fi'

# Clean Apple restores restrict remote login to the guest's SSH access group.
if dscl . -read /Groups/com.apple.access_ssh >/dev/null 2>&1; then
    /usr/sbin/dseditgroup -o edit -a $ordinary -t user com.apple.access_ssh
fi
cd "$kit"
bash install-macos-node.sh "$kit/node.tgz"
bash install-macos-browser.sh "$kit"
bash cli/install-macos-cli.sh "$kit/cli"
bash claude-desktop-macos.sh install "$kit/claude.dmg"
bash claude-desktop-macos.sh migrate "$ordinary"
install -m 755 cage-macos-ipv6-check /usr/local/libexec/cage-macos-ipv6-check
# Move a verified staged bundle into place. The installed copy is moved aside
# first and rolls back if the new bundle cannot land, so a failed replacement
# never destroys the only usable bundle; an interruption between the two moves
# leaves the previous copy in the move-aside directory.
swap_dir() { # staged_dir target
    local new=$1 target=$2 old
    old=$(mktemp -d "$(dirname "$target")/.cage-workbench-old.XXXXXXXX")
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

# A resumed installation may carry a differently signed build of the same
# verified identifier, and ditto onto an existing app would merge bundles,
# leaving stale resources that fail signature verification. Stage and verify
# this kit's copy first, then replace the installed app in one move.
replace_app() { # source target requirement [deep]
    local source=$1 target=$2 requirement=$3 deep=${4:-} verify stage
    verify=(codesign --verify --strict)
    [[ -z $deep ]] || verify=(codesign --verify --deep --strict)
    if [[ -e $target || -L $target ]]; then
        [[ ! -L $target && -d $target ]] || { echo "Unexpected application at $target" >&2; exit 1; }
        codesign --verify --strict -R "=$requirement" "$target"
    fi
    stage=$(mktemp -d /Applications/.cage-workbench-install.XXXXXXXX)
    if ! ditto "$source" "$stage/app" || ! chmod -R a+rX,go-w "$stage/app" ||
        ! "${verify[@]}" -R "=$requirement" "$stage/app"; then
        rm -rf "$stage"
        echo "Could not verify this kit's build of ${target##*/}" >&2
        exit 1
    fi
    if [[ -e $target || -L $target ]]; then
        if ! swap_dir "$stage/app" "$target"; then
            rm -rf "$stage"
            echo "Could not refresh $target" >&2
            exit 1
        fi
    elif ! mv "$stage/app" "$target"; then
        rm -rf "$stage"
        echo "Could not install $target" >&2
        exit 1
    fi
    rmdir "$stage"
}
replace_app 'Cage Guest Window.app' '/Applications/Cage Guest Window.app' 'identifier "dev.cage.GuestClaudeWindow"'
replace_app 'Cage Guest Clipboard.app' '/Applications/Cage Guest Clipboard.app' 'identifier "dev.cage.GuestClipboard"' deep
# Staging and move-aside directories left by an interrupted earlier run are
# removed only now that both applications are verified in place.
rm -rf /Applications/.cage-workbench-install.* /Applications/.cage-workbench-old.*
install -m 755 register-macos-claude-window.sh /usr/local/libexec/cage-register-claude-window
install -m 755 register-macos-clipboard.sh /usr/local/libexec/cage-register-clipboard
install -m 755 register-macos-browser.sh /usr/local/libexec/cage-register-browser
# Install the user's actual socket-activated LaunchAgent before the first Aqua
# login. launchd reads it during login, including while Setup Assistant runs.
cd /
sudo -Hu $ordinary /usr/local/libexec/cage-register-claude-window --prepare
sudo -Hu $ordinary /usr/local/libexec/cage-register-clipboard --prepare
sudo -Hu $ordinary /usr/local/libexec/cage-register-browser --prepare
cd "$kit"
project_args=("$kit/project")
if [[ ${CAGE_MACOS_GUI_AUTHORIZE:-0} == 1 ]]; then
    project_args+=(--gui-authorize)
    export CAGE_MACOS_GUI_USER=$ordinary
fi
bash install-macos-project-tools.sh "${project_args[@]}"
