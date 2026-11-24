# Installs or updates one plugin desktop application to the vendor's latest
# release. Runs inside the maintenance operation as the workspace user, after
# the caller has set the app_* variables from a validated plugin manifest.
# Every privileged step is here; the plugin's resolver, when there is one,
# runs as the ordinary user and only names the release.
set -euo pipefail
[[ $app_id =~ ^[0-9a-f]{24}$ ]]
case "$(dpkg --print-architecture)" in
  amd64) arch=amd64 ;;
  arm64) arch=arm64 ;;
  *) echo '此处理器暂不支持这个应用。' >&2; exit 1 ;;
esac
record_dir="/usr/local/lib/desktop-apps/$app_id"
lock_dir="$HOME/.local/share/desktop-apps/$app_id"
mkdir -p "$lock_dir"
exec 8>"$lock_dir/install.lock"
flock -n 8 || { echo '这个应用正在安装或更新，请稍候。' >&2; exit 1; }
if systemctl is-active --quiet "desktop-$app_id.service"; then
  echo '请先保存工作并关闭应用，再更新或修复安装。' >&2
  exit 1
fi
apt_get() {
  local status
  if sudo env DEBIAN_FRONTEND=noninteractive APT_CONFIG="${CAGE_APT_CONFIG:-/dev/null}" apt-get -o DPkg::Lock::Timeout=60 "$@" </dev/null; then
    return 0
  else
    status=$?
  fi
  printf 'APT_FAILED exit=%s command=apt-get %s\n' "$status" "$*" >&2
  return "$status"
}
temporary=$(mktemp -d)
trap 'rm -rf "$temporary"' EXIT
chmod 0755 "$temporary"
previous_version=
if sudo test -s "$record_dir/app.json"; then
  previous_version=$(python3 -I -c 'import json,sys;print(json.load(open(sys.argv[1])).get("version",""))' "$record_dir/app.json" 2>/dev/null || :)
fi
printf '正在准备安装依赖…\n'
apt_get update
# shellcheck disable=SC2086
apt_get install -y --no-install-recommends $app_dependencies ${app_packages:-}

# The sources and keyrings a vendor's package adds for itself are recorded so
# that removing the application removes them too.
apt_files() {
  sudo find /etc/apt/sources.list.d /usr/share/keyrings /etc/apt/keyrings /etc/apt/trusted.gpg.d -maxdepth 1 -type f 2>/dev/null | sort || :
}
apt_files > "$temporary/apt-before"

# resolve prints the latest release as JSON. It runs as the ordinary user,
# with only the download proxy and a clean environment, and must finish soon.
resolve() {
  printf '%s' "$app_resolver" | base64 -d > "$temporary/resolve"
  local runner=(/bin/bash)
  [[ $app_resolver_kind == py ]] && runner=(/usr/bin/python3 -I)
  mapfile -d '' -t arguments < <(python3 -I -c 'import json,sys
for value in json.loads(sys.argv[1]): sys.stdout.write(value + "\0")' "$app_resolver_args")
  (cd "$temporary" && timeout 90 env -i HOME="$HOME" PATH=/usr/bin:/bin LANG=C.UTF-8 \
    http_proxy="${http_proxy:-}" https_proxy="${https_proxy:-}" HTTP_PROXY="${HTTP_PROXY:-}" HTTPS_PROXY="${HTTPS_PROXY:-}" \
    "${runner[@]}" "$temporary/resolve" "$arch" ${arguments[@]+"${arguments[@]}"}) | head -c 65536 > "$temporary/release.json"
  python3 -I - "$temporary/release.json" "$app_hosts" > "$temporary/release" <<'CHECK'
import json, re, sys
from urllib.parse import urlsplit
try:
    release = json.load(open(sys.argv[1]))
except ValueError:
    sys.exit("插件未能给出最新版本：返回内容不是 JSON")
hosts = sys.argv[2].split()
if not isinstance(release, dict):
    sys.exit("插件未能给出最新版本")
version, url, digest = release.get("version"), release.get("url"), release.get("sha256", "")
if not isinstance(version, str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.+_~-]{0,63}", version):
    sys.exit("插件给出的版本号无效")
if not isinstance(url, str) or len(url) > 2048 or any(c in url for c in " \t\r\n\\\"'"):
    sys.exit("插件给出的下载地址无效")
parts = urlsplit(url)
if parts.scheme != "https" or parts.username or parts.password or parts.port not in (None, 443) or parts.hostname not in hosts:
    sys.exit("下载地址不在插件声明的官方下载站点内")
if not isinstance(digest, str) or digest and not re.fullmatch(r"[0-9a-fA-F]{64}", digest):
    sys.exit("插件给出的校验值无效")
print(version)
print(url)
print(digest.lower())
CHECK
  { read -r release_version; read -r release_url; read -r release_digest || :; } < "$temporary/release"
}

download() {
  printf '正在从官方站点下载 %s…\n' "$release_version"
  cage_curl_download "$1" --proto '=https' --proto-redir '=https' --max-redirs 5 -fSL --connect-timeout 15 "$release_url" -o "$1"
  if [[ -n $release_digest ]]; then
    printf '%s  %s\n' "$release_digest" "$1" | sha256sum -c --quiet - || { echo '下载文件校验失败，安装已停止。' >&2; exit 1; }
  fi
}

package=
command_json=$app_command
case "$app_source" in
  apt)
    package=$app_package
    list="/etc/apt/sources.list.d/desktop-app-$app_id.list"
    keyring="/usr/share/keyrings/desktop-app-$app_id.gpg"
    # Once the vendor's package has configured the same repository, it is the
    # one apt uses: two entries with different keyrings make apt refuse both.
    # That package came from the repository verified below, so its keyring is
    # the vendor's own.
    vendor_source=$(sudo grep -rlsF -- "$app_repository" /etc/apt/sources.list.d 2>/dev/null | grep -vxF "$list" || :)
    if [[ -z $vendor_source ]]; then
      printf '正在验证官方安装源…\n'
      cage_curl_download "$temporary/key" --proto '=https' --proto-redir '=https' -fSL --connect-timeout 15 "$app_key" -o "$temporary/key"
      mkdir -m 0700 "$temporary/gnupg"
      gpg --batch --quiet --homedir "$temporary/gnupg" --import "$temporary/key" 2>/dev/null
      fingerprints=$(gpg --batch --homedir "$temporary/gnupg" --with-colons --list-keys | awk -F: '$1 == "fpr" {print $10}')
      grep -qxF "$app_fingerprint" <<<"$fingerprints" || { echo '官方签名密钥与插件声明的指纹不符，安装已停止。' >&2; exit 1; }
      gpg --batch --homedir "$temporary/gnupg" --export "$app_fingerprint" > "$temporary/keyring.gpg"
      sudo install -m 0644 "$temporary/keyring.gpg" "$keyring"
      printf 'deb [arch=%s signed-by=%s] %s %s %s\n' "$arch" "$keyring" "$app_repository" "$app_suite" "$app_components" | sudo tee "$list" >/dev/null
    else
      sudo rm -f "$list" "$keyring"
    fi
    apt_get update
    printf '正在下载并安装最新版本…\n'
    apt_get install -y --no-install-recommends "$package"
    if sudo grep -rlsF -- "$app_repository" /etc/apt/sources.list.d 2>/dev/null | grep -vqxF "$list"; then
      if sudo test -e "$list"; then
        sudo rm -f "$list" "$keyring"
        apt_get update
      fi
    fi
    version=$(dpkg-query -W -f='${Version}' "$package")
    ;;
  deb)
    resolve
    download "$temporary/app.deb"
    package=$(dpkg-deb -f "$temporary/app.deb" Package)
    [[ $package =~ ^[a-z0-9][a-z0-9+.-]{1,62}$ ]] || { echo '下载的安装包无效。' >&2; exit 1; }
    printf '正在安装 %s…\n' "$release_version"
    apt_get install -y --no-install-recommends "$temporary/app.deb"
    version=$(dpkg-query -W -f='${Version}' "$package")
    ;;
  archive)
    resolve
    root="/opt/desktop-apps/$app_id"
    current=$(sudo readlink "$root/current" 2>/dev/null || :)
    # The release in use is already the latest: nothing to download.
    if [[ $current == "releases/$release_version" ]] && sudo test -d "$root/$current"; then
      :
    else
      download "$temporary/app.archive"
      printf '正在解压 %s…\n' "$release_version"
      sudo install -d -m 0755 /opt/desktop-apps "$root" "$root/releases"
      staging="$root/releases/.staging"
      sudo rm -rf "$staging"
      sudo install -d -m 0755 "$staging"
      sudo tar -xf "$temporary/app.archive" -C "$staging" --no-same-owner
      # A release whose files sit in one folder is used from inside it, so the
      # command does not depend on the folder's name for each processor.
      sudo python3 -I - "$staging" "$root/releases/$release_version" <<'PLACE'
import os, shutil, sys
staging, target = sys.argv[1], sys.argv[2]
entries = os.listdir(staging)
source = staging
if len(entries) == 1 and os.path.isdir(os.path.join(staging, entries[0])) and not os.path.islink(os.path.join(staging, entries[0])):
    source = os.path.join(staging, entries[0])
if os.path.lexists(target):
    shutil.rmtree(target)
os.rename(source, target)
if os.path.exists(staging):
    shutil.rmtree(staging)
PLACE
      release="$root/releases/$release_version"
      sudo chown -R root:root "$release"
      sudo chmod -R u+rwX,go+rX,go-w "$release"
      # Chromium-based applications need their sandbox helper set up as root.
      sudo find "$release" -maxdepth 3 -type f -name chrome-sandbox -exec chmod 4755 {} +
      program=$(python3 -I -c 'import json,sys;print(json.loads(sys.argv[1])[0])' "$app_command")
      sudo test -x "$release/$program" || { echo '新版本中没有插件声明的启动程序，已保留原版本。' >&2; sudo rm -rf "$release"; exit 1; }
      sudo ln -sfn "releases/$release_version" "$root/current.new"
      sudo mv -Tf "$root/current.new" "$root/current"
      # Keep the release before this one to return to; remove older ones.
      for old in $(sudo find "$root/releases" -mindepth 1 -maxdepth 1 -printf '%f\n'); do
        [[ "releases/$old" == "$current" || $old == "$release_version" ]] && continue
        sudo rm -rf -- "${root:?}/releases/$old"
      done
    fi
    command_json=$(python3 -I -c 'import json,sys
command = json.loads(sys.argv[1])
command[0] = sys.argv[2] + "/current/" + command[0]
print(json.dumps(command))' "$app_command" "$root")
    version=$release_version
    ;;
  *) exit 2 ;;
esac
apt_files > "$temporary/apt-after"
added=$(comm -13 "$temporary/apt-before" "$temporary/apt-after" | grep -vxF "/etc/apt/sources.list.d/desktop-app-$app_id.list" | grep -vxF "/usr/share/keyrings/desktop-app-$app_id.gpg" || :)

# The record is root's: what the session runs comes only from here.
sudo install -d -m 0755 "$record_dir"
sudo python3 -I - "$record_dir/app.json" "$app_source" "$version" "$package" "$command_json" "$app_schemes" "$added" <<'RECORD'
import json, os, sys
path, source, version, package, command, schemes, added = sys.argv[1:]
previous = {}
try:
    previous = json.load(open(path))
except (OSError, ValueError):
    pass
vendor = sorted(set(previous.get("vendor_files", [])) | {line for line in added.splitlines() if line})
record = {"source": source, "version": version, "command": json.loads(command), "schemes": schemes.split(), "vendor_files": vendor}
if package:
    record["package"] = package
if previous.get("version") and previous.get("version") != version:
    record["previous_version"] = previous["version"]
temporary = path + ".new"
with open(temporary, "w") as output:
    json.dump(record, output)
os.chmod(temporary, 0o644)
os.replace(temporary, path)
RECORD
# Rebuild the table of schemes from every installed application's record.
sudo python3 -I - <<'SCHEMES'
import json, os
from pathlib import Path
root = Path("/usr/local/lib/desktop-apps")
lines = []
for record in sorted(root.glob("*/app.json")):
    try:
        schemes = json.loads(record.read_text()).get("schemes", [])
    except ValueError:
        continue
    lines += [f"{scheme} {record.parent.name}\n" for scheme in schemes]
temporary = root / "schemes.new"
temporary.write_text("".join(lines))
os.chmod(temporary, 0o644)
os.replace(temporary, root / "schemes")
SCHEMES
# The browser hands the application's sign-in links to the opener.
if [[ -n $app_schemes ]]; then
  mimes=""
  for scheme in $app_schemes; do mimes+="x-scheme-handler/$scheme;"; done
  sudo install -d -m 0755 /usr/local/share/applications
  printf '%s\n' '[Desktop Entry]' 'Type=Application' 'Name=Sign-in' 'NoDisplay=true' 'Exec=/usr/local/bin/cage-open %u' "MimeType=$mimes" | sudo tee "/usr/local/share/applications/desktop-app-$app_id.desktop" >/dev/null
  # The browser's account reads it whatever the caller's umask.
  sudo chmod 0644 "/usr/local/share/applications/desktop-app-$app_id.desktop"
  for scheme in $app_schemes; do
    sudo -H -u cage-browser xdg-mime default "desktop-app-$app_id.desktop" "x-scheme-handler/$scheme"
  done
fi
if [[ -n $previous_version && $previous_version == "$version" ]]; then
  printf '已是最新版本 %s。\n' "$version"
else
  printf '已安装 %s。\n' "$version"
fi
