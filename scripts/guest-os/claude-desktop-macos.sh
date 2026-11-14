#!/bin/bash
# Run inside a Cage macOS guest. Install accepts a local official DMG.
set -euo pipefail
export PATH=/usr/bin:/bin:/usr/sbin:/sbin

fail() { printf '%s\n' "$*" >&2; exit 1; }

case "${1:-}" in
  inspect|launch) [[ $# == 1 ]] || fail "Usage: $0 $1" ;;
  install) [[ $# == 2 ]] || fail "Usage: $0 install /path/to/Claude.dmg" ;;
  verify-media) [[ $# == 3 && $3 =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "Usage: $0 verify-media /path/to/Claude.dmg version" ;;
  migrate) [[ $# == 2 && $2 =~ ^[a-z][a-z0-9_]{0,30}$ ]] || fail "Usage: $0 migrate username" ;;
  *) fail "Usage: $0 inspect | install /path/to/Claude.dmg | migrate username | launch" ;;
esac

if [[ $1 != verify-media ]]; then
  [[ $(uname -s) == Darwin ]] || fail "This helper requires a Cage macOS guest."
  model=$(sysctl -n hw.model)
  { [[ $model =~ ^VirtualMac[0-9]+,[0-9]+$ ]] || /usr/local/libexec/cage-guest-check --check; } || fail "This Mac is not a virtual Mac. Run this helper inside the Cage guest."
  marker=/var/db/cage-guest-os
  [[ -f $marker && ! -L $marker && $(stat -f '%u' "$marker") == 0 ]] ||
    fail "Missing root-owned Cage guest marker."
  mode=$(stat -f '%OLp' "$marker")
  (( (8#$mode & 022) == 0 )) && [[ $(cat "$marker") == macos ]] ||
    fail "Invalid Cage guest marker."
fi

app=/Applications/Claude.app
bundle_id=com.anthropic.claudefordesktop
team=Q6L2SF6YDW
requirement="anchor apple generic and identifier \"$bundle_id\" and certificate leaf[subject.OU] = \"$team\" and certificate 1[field.1.2.840.113635.100.6.2.6] exists and certificate leaf[field.1.2.840.113635.100.6.1.13] exists"

info() {
  plutil -extract "$2" raw -o - "$1/Contents/Info.plist"
}

verify_app() {
  [[ -d $1 && ! -L $1 ]] || fail "Expected a Claude.app directory at $1."
  [[ $(info "$1" CFBundleIdentifier) == "$bundle_id" ]] || fail "Unexpected Claude bundle identifier."
  codesign --verify --deep --strict "$1"
  codesign --verify --strict -R "=$requirement" "$1"
}

if [[ $1 == verify-media ]]; then
  [[ $2 == /* && -f $2 && ! -L $2 ]] || fail 'Supply a downloaded Claude DMG.'
  stage=$(mktemp -d "${TMPDIR:-/tmp}/claude-media.XXXXXXXX")
  mountdir="$stage/media"
  mkdir "$mountdir"
  mounted=false
  cleanup() {
    if [[ $mounted == true ]]; then
      hdiutil detach "$mountdir" >/dev/null || return
    fi
    rm -rf "$stage"
  }
  trap cleanup EXIT
  trap 'exit 1' HUP INT TERM
  hdiutil attach -readonly -nobrowse -noautoopen -mountpoint "$mountdir" "$2" >/dev/null
  mounted=true
  verify_app "$mountdir/Claude.app"
  [[ $(info "$mountdir/Claude.app" CFBundleShortVersionString) == "$3" ]] || fail 'Claude media version differs from the official release.'
  exit 0
fi

version_ge() {
  local i left right
  local -a a b
  [[ $1 =~ ^[0-9]+\.[0-9]+\.[0-9]+$ && $2 =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || return 1
  IFS=. read -r -a a <<< "$1"
  IFS=. read -r -a b <<< "$2"
  for ((i=0; i<3; i++)); do
    left=$((10#${a[i]})); right=$((10#${b[i]}))
    (( left == right )) || { (( left > right )); return; }
  done
}

# A dedicated writable application directory lets the ordinary account use
# Anthropic's updater without write access to unrelated /Applications entries.
# Copy as that account: root never writes through user-controlled paths.
if [[ $1 == migrate ]]; then
  [[ $EUID == 0 ]] || fail "Run migrate as root."
  user=$2
  uid=$(id -u "$user")
  [[ $uid -ge 500 && $(dscl . -read "/Users/$user" NFSHomeDirectory) == "NFSHomeDirectory: /Users/$user" ]] || fail "Invalid application account."
  destination=/Applications/Claude/Claude.app
  if [[ -e $app || -L $app ]]; then
    verify_app "$app"
    if pgrep -f '^/Applications/Claude\.app/Contents/MacOS/Claude([[:space:]]|$)' >/dev/null; then
      fail "请先保存工作并关闭 Claude Desktop，再更新环境组件。"
    fi
    parent=$(dirname "$destination")
    [[ ! -L $parent ]] || fail "Invalid application directory."
    if [[ ! -e $parent ]]; then
      install -d -m 0755 -o "$uid" -g "$(id -g "$user")" "$parent"
    fi
    [[ -d $parent && $(stat -f %u "$parent") == "$uid" ]] || fail "Unexpected application directory owner."
    sudo -Hu "$user" /bin/bash -se -- "$app" "$destination" "$requirement" <<'USER_INSTALL'
source_app=$1 destination=$2 requirement=$3
parent=$(dirname "$destination")
replace=yes
rename_flag=4 # RENAME_EXCL
if [[ -e $destination || -L $destination ]]; then
  [[ -d $destination && ! -L $destination ]] || exit 1
  codesign --verify --deep --strict -R "=$requirement" "$destination"
  source_version=$(plutil -extract CFBundleVersion raw -o - "$source_app/Contents/Info.plist")
  current_version=$(plutil -extract CFBundleVersion raw -o - "$destination/Contents/Info.plist")
  # Official bundle builds are dotted decimal versions. Never downgrade a
  # user's self-updated copy when provisioning media contains an older build.
  [[ $source_version =~ ^[0-9]+(\.[0-9]+)*$ && $current_version =~ ^[0-9]+(\.[0-9]+)*$ ]] || exit 1
  if ! awk -v incoming="$source_version" -v current="$current_version" 'BEGIN {
      n=split(incoming,a,"."); m=split(current,b,".");
      for(i=1;i<=n || i<=m;i++) { if(a[i]+0>b[i]+0) exit 0; if(a[i]+0<b[i]+0) exit 1; }
      exit 1
  }'; then replace=no; fi
  rename_flag=2 # RENAME_SWAP
fi
if [[ $replace == yes ]]; then
  if pgrep -f "^${destination//./\\.}/Contents/MacOS/Claude([[:space:]]|$)" >/dev/null; then
    echo '请先保存工作并关闭 Claude Desktop，再安装其他版本。' >&2
    exit 1
  fi
  stage=$(mktemp -d "$parent/.claude-install.XXXXXXXX")
  trap 'rm -rf "$stage"' EXIT
  ditto "$source_app" "$stage/Claude.app"
  chmod -R u+rwX "$stage/Claude.app"
  codesign --verify --deep --strict -R "=$requirement" "$stage/Claude.app"
  /usr/bin/osascript -l JavaScript - "$stage/Claude.app" "$destination" "$rename_flag" <<'JXA_MIGRATE'
ObjC.bindFunction("renamex_np", ["int", ["char *", "char *", "uint32_t"]]);
function run(args) {
  if ($.renamex_np(args[0], args[1], Number(args[2])) !== 0) {
    throw Error("Could not publish Claude.app; previous installation preserved.");
  }
}
JXA_MIGRATE
fi
codesign --verify --deep --strict -R "=$requirement" "$destination"
[[ -w $parent && -w $destination ]] || exit 1
USER_INSTALL
    verify_app "$destination"
    # A verified user copy is published before the old application is removed.
    # Application Support, Keychain and all other personal files stay in place.
    rm -rf "$app"
  elif [[ -e $destination || -L $destination ]]; then
    verify_app "$destination"
  fi
  exit 0
fi

if [[ $1 != install && -d /Applications/Claude/Claude.app ]]; then
  app=/Applications/Claude/Claude.app
fi

report() {
  printf 'status=%s\nversion=%s\nbuild=%s\nbundle_id=%s\nteam=%s\n' \
    "$1" "$(info "$app" CFBundleShortVersionString)" "$(info "$app" CFBundleVersion)" "$bundle_id" "$team"
}

case "$1" in
  inspect)
    [[ -e $app || -L $app ]] || { printf 'status=absent\n'; exit 3; }
    verify_app "$app"
    report installed
    ;;
  install)
    [[ $EUID == 0 ]] || fail "Run install as root inside the guest."
    dmg=$2
    [[ $dmg == /* && -f $dmg ]] || fail "Supply the absolute path of the downloaded Claude DMG."
    exec 9>/var/run/cage-claude-install.lock
    lockf -s -t 0 9 || fail "A Claude installation is in progress."
    stage=$(mktemp -d /Applications/.cage-claude.XXXXXXXX)
    mountdir="$stage/media"
    mkdir "$mountdir"
    cleanup() {
      # Mounts are read-only. Keep the directory if the system cannot detach.
      if [[ $(stat -f '%d' "$mountdir") != "$(stat -f '%d' "$stage")" ]]; then
        hdiutil detach "$mountdir" >/dev/null || { printf 'Unable to detach %s\n' "$mountdir" >&2; return; }
      fi
      rm -rf "$stage"
    }
    trap cleanup EXIT
    trap 'exit 1' HUP INT TERM
    hdiutil attach -readonly -nobrowse -noautoopen -mountpoint "$mountdir" "$dmg" >/dev/null
    source_app="$mountdir/Claude.app"
    verify_app "$source_app"
    # renamex_np publishes the verified copy on the same filesystem. With an
    # existing app, RENAME_SWAP retains it in staging until the swap succeeds.
    rename_flag=4 # RENAME_EXCL
    if [[ -e $app || -L $app ]]; then
      verify_app "$app"
      if version_ge "$(info "$app" CFBundleShortVersionString)" "$(info "$source_app" CFBundleShortVersionString)"; then
        report installed
        exit 0
      fi
      if pgrep -f '^/Applications/Claude\.app/Contents/MacOS/Claude([[:space:]]|$)' >/dev/null; then
        fail "Quit the running guest Claude app before installing another version."
      fi
      rename_flag=2 # RENAME_SWAP
    fi
    staged_app="$stage/Claude.app"
    ditto "$source_app" "$staged_app"
    chown -R -P root:wheel "$staged_app"
    verify_app "$staged_app"
    /usr/bin/osascript -l JavaScript - "$staged_app" "$app" "$rename_flag" <<'JXA'
ObjC.bindFunction("renamex_np", ["int", ["char *", "char *", "uint32_t"]]);
function run(args) {
  if ($.renamex_np(args[0], args[1], Number(args[2])) !== 0) {
    throw Error("Could not publish Claude.app; previous installation preserved.");
  }
}
JXA
    report installed
    ;;
  launch)
    verify_app "$app"
    gui_uid=$(stat -f '%u' /dev/console)
    if (( gui_uid < 500 )) || ! launchctl print "gui/$gui_uid" >/dev/null 2>&1; then
      printf 'status=requires_gui_login\nLog in to the guest macOS desktop first.\n' >&2
      exit 4
    fi
    if [[ $EUID == 0 ]]; then
      launchctl asuser "$gui_uid" sudo -n -H -u "#$gui_uid" -- /usr/bin/open "$app"
    elif [[ $EUID == "$gui_uid" ]]; then
      /usr/bin/open "$app"
    else
      fail "Run launch as root or the guest console user."
    fi
    for ((attempt=0; attempt<20; attempt++)); do
      if pid=$(pgrep -o -U "$gui_uid" -f "^${app//./\\.}/Contents/MacOS/Claude([[:space:]]|$)"); then
        report running
        printf 'pid=%s\n' "$pid"
        exit 0
      fi
      sleep 1
    done
    fail "The Claude application process did not start in the guest GUI session."
    ;;
esac
