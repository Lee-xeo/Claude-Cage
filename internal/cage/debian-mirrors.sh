#!/bin/sh
# Only stock and Cage-owned lists are managed. Custom lists and direct URIs
# remain under the user's control. The preference survives guest cloning.
# A choice measured on the host arrives as a plain shell variable, never an
# exported one, so later installers do not inherit it.
sudo env APT_CONFIG="${CAGE_APT_CONFIG:-/dev/null}" sh -s -e -- "${debian_mirror_choice:-}" <<'CAGE_DEBIAN_MIRRORS'
set -eu
preference=auto
# The user's own preference. Before an earlier installation's settings are
# copied to the current directory, it is still found under its former name.
source=/etc/workbench/debian-mirror
for file in /etc/workbench/debian-mirror /etc/claude-cage/debian-mirror; do
  if [ -f "$file" ]; then
    preference=$(cat "$file")
    source=$file
    break
  fi
done
# The host's choice wins over the saved file and is kept for later
# maintenance runs, which do not repeat the measurement.
if [ -n "${1:-}" ]; then
  case "$1" in
    auto|official|nju|ustc) ;;
    *) echo 'Invalid Debian mirror choice: use auto, official, nju or ustc.' >&2; exit 1;;
  esac
  preference=$1
  source=/etc/workbench/debian-mirror
  mkdir -p "$(dirname /etc/workbench/debian-mirror)"
  printf '%s\n' "$preference" > /etc/workbench/debian-mirror
fi
case "$preference" in
  auto|official|nju) domestic="https://mirrors.nju.edu.cn https://mirrors.ustc.edu.cn";;
  ustc) domestic="https://mirrors.ustc.edu.cn https://mirrors.nju.edu.cn";;
  *) echo "Invalid $source: use auto, official, nju or ustc." >&2; exit 1;;
esac
for repository in debian debian-security; do
  list="/etc/apt/mirrors/$repository.list"
  [ -f "$list" ] || continue
  content=$(cat "$list")
  official="https://deb.debian.org/$repository"
  old=$(printf '%s\tpriority:%s\n' "https://mirrors.nju.edu.cn/$repository" 1 "https://mirrors.ustc.edu.cn/$repository" 2 "$official" 3)
  case "$content" in
    "$official"|"$old"|'# Cage managed Debian mirrors'*) ;;
    *) continue;;
  esac
  stage=$(mktemp "$list.XXXXXX")
  probe=$(mktemp)
  trap 'rm -f "$stage" "$probe"' EXIT
  printf '# Cage managed Debian mirrors\n' > "$stage"
  suite=$(. /etc/os-release; printf '%s' "${VERSION_CODENAME:-trixie}")
  [ "$repository" != debian-security ] || suite="$suite-security"
  priority=0
  # "auto" prefers the official CDN when it works through the guest's
  # configured network and probes domestic mirrors only after it fails.
  # "nju" and "ustc" put the mirror the host measured fastest first; an
  # unreachable mirror is skipped and the official source stays last.
  if [ "$preference" = nju ] || [ "$preference" = ustc ] || { [ "$preference" = auto ] && ! timeout 8 /usr/lib/apt/apt-helper -o Acquire::Retries=0 \
      -o Acquire::http::Timeout=5 -o Acquire::https::Timeout=5 \
      download-file "$official/dists/$suite/InRelease" "$probe" >/dev/null 2>&1; }; then
    for origin in $domestic; do
      mirror="$origin/$repository"
      rm -f "$probe"
      if timeout 8 /usr/lib/apt/apt-helper -o Acquire::Retries=0 \
          -o Acquire::http::Timeout=5 -o Acquire::https::Timeout=5 \
          download-file "$mirror/dists/$suite/InRelease" "$probe" >/dev/null 2>&1; then
        priority=$((priority + 1))
        printf '%s\tpriority:%s\n' "$mirror" "$priority" >> "$stage"
      else
        printf 'Debian mirror unavailable; using the next source: %s\n' "$mirror"
      fi
    done
  fi
  priority=$((priority + 1))
  printf '%s\tpriority:%s\n' "$official" "$priority" >> "$stage"
  chmod 0644 "$stage"
  mv -f "$stage" "$list"
  rm -f "$probe"
  trap - EXIT
done
CAGE_DEBIAN_MIRRORS
