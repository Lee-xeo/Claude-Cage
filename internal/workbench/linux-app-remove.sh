# Removes one plugin desktop application's files: its package or release,
# the sources and keyrings recorded at installation, and its record. The
# user's sign-in, settings and projects in the home folder stay.
set -euo pipefail
[[ $app_id =~ ^[0-9a-f]{24}$ ]]
record_dir="/usr/local/lib/desktop-apps/$app_id"
if systemctl is-active --quiet "desktop-$app_id.service"; then
  echo '请先保存工作并关闭应用，再卸载。' >&2
  exit 1
fi
sudo systemctl reset-failed "desktop-$app_id.service" 2>/dev/null || :
if sudo test -s "$record_dir/app.json"; then
  mapfile -t facts < <(python3 -I -c 'import json,sys
record = json.load(open(sys.argv[1]))
print(record.get("source", ""))
print(record.get("package", ""))
for path in record.get("vendor_files", []): print(path)' "$record_dir/app.json")
  source=${facts[0]:-}
  package=${facts[1]:-}
  # Purging removes the package's system configuration as well, never the
  # user's own files in the home folder.
  if [[ $source == apt || $source == deb ]] && [[ $package =~ ^[a-z0-9][a-z0-9+.-]{1,62}$ ]] && dpkg-query -W -f='${Status}' "$package" 2>/dev/null | grep -q ' installed$\| config-files$'; then
    printf '正在卸载应用文件…\n'
    sudo env DEBIAN_FRONTEND=noninteractive apt-get -o DPkg::Lock::Timeout=60 purge -y "$package" </dev/null
  fi
  for path in "${facts[@]:2}"; do
    case "$path" in
      /etc/apt/sources.list.d/*|/usr/share/keyrings/*|/etc/apt/keyrings/*|/etc/apt/trusted.gpg.d/*)
        [[ $path != *..* ]] && sudo rm -f -- "$path" ;;
    esac
  done
fi
sudo rm -f "/etc/apt/sources.list.d/desktop-app-$app_id.list" "/usr/share/keyrings/desktop-app-$app_id.gpg" "/usr/local/share/applications/desktop-app-$app_id.desktop"
# The browser no longer hands these links anywhere.
sudo -H -u cage-browser python3 -I - "desktop-app-$app_id.desktop" <<'MIMEAPPS' || :
import os, sys
from pathlib import Path
path = Path.home() / ".config/mimeapps.list"
if path.is_file():
    lines = path.read_text().splitlines(keepends=True)
    kept = [line for line in lines if line.split("=", 1)[-1].strip().rstrip(";") != sys.argv[1]]
    if kept != lines:
        temporary = path.with_name(path.name + ".new")
        temporary.write_text("".join(kept))
        os.replace(temporary, path)
MIMEAPPS
sudo rm -rf "/opt/desktop-apps/$app_id" "$record_dir"
sudo python3 -I - <<'SCHEMES'
import json, os
from pathlib import Path
root = Path("/usr/local/lib/desktop-apps")
if root.is_dir():
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
printf '应用已卸载，登录与设置保留在环境中。\n'
