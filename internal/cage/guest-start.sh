#!/usr/bin/env bash
set -euo pipefail
missing_tool() {
  echo "Missing $1; this environment may have been created from an older base." >&2
  case "$1" in
    chromium|code-server) echo 'Run cage update-tools, then cage up to install the missing tool in this VM.' >&2 ;;
  esac
  echo 'To rebuild from the base, run cage setup --preinstall=default, then cage up --reset (files and logins on its disk are lost).' >&2
  exit 1
}
if [[ ! -x /usr/bin/chromium ]]; then
  missing_tool chromium
fi
for app in code-server Xtigervnc openbox websockify; do
  command -v "$app" >/dev/null || missing_tool "$app"
done
# English text keeps its requested fonts. For missing Han glyphs, prefer the
# Simplified Chinese face of the installed Noto collection.
sudo tee /etc/fonts/conf.d/71-english-cjk-fallback.conf >/dev/null <<'FONTCONFIG'
<?xml version="1.0"?>
<!DOCTYPE fontconfig SYSTEM "urn:fontconfig:fonts.dtd">
<fontconfig>
  <match target="pattern">
    <test name="lang" qual="first" compare="contains"><string>en</string></test>
    <test name="family"><string>sans-serif</string></test>
    <edit name="family" mode="append_last" binding="weak"><string>Noto Sans CJK SC</string></edit>
  </match>
  <match target="pattern">
    <test name="lang" qual="first" compare="contains"><string>en</string></test>
    <test name="family"><string>serif</string></test>
    <edit name="family" mode="append_last" binding="weak"><string>Noto Serif CJK SC</string></edit>
  </match>
  <match target="pattern">
    <test name="lang" qual="first" compare="contains"><string>en</string></test>
    <test name="family"><string>monospace</string></test>
    <edit name="family" mode="append_last" binding="weak"><string>Noto Sans Mono CJK SC</string></edit>
  </match>
</fontconfig>
FONTCONFIG
mkdir -p "$HOME/.config/claude-cage"
chmod 700 "$HOME/.config/claude-cage"
if ! grep -qE '^password:[[:space:]]*[^[:space:]]+' "$HOME/.config/claude-cage/code-server.yaml" 2>/dev/null; then
  (umask 077; tmp=$(mktemp "$HOME/.config/claude-cage/.code-server.yaml.XXXXXX"); printf 'auth: password\npassword: %s\ncert: false\n' "$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')" > "$tmp"; sync "$tmp" 2>/dev/null || true; mv -f "$tmp" "$HOME/.config/claude-cage/code-server.yaml"; sync "$HOME/.config/claude-cage" 2>/dev/null || true)
fi
if [ ! -s "$HOME/.config/claude-cage/browser-password" ]; then
  (umask 077; tmp=$(mktemp "$HOME/.config/claude-cage/.browser-password.XXXXXX"); od -An -N4 -tx1 /dev/urandom | tr -d ' \n' > "$tmp"; sync "$tmp" 2>/dev/null || true; mv -f "$tmp" "$HOME/.config/claude-cage/browser-password"; sync "$HOME/.config/claude-cage" 2>/dev/null || true)
fi
sudo install -d -o cage-browser -g cage-browser -m 0700 /home/cage-browser/.vnc
cat "$HOME/.config/claude-cage/browser-password" | sudo -u cage-browser sh -c 'umask 077; tigervncpasswd -f > "$HOME/.vnc/passwd"'
sudo systemctl stop cage-browser || true
# Existing images may predate the explicit locale dependency. The scoped
# maintenance channel can install it while business networking stays closed.
if ! dpkg-query -W -f='${Status}' chromium-l10n 2>/dev/null | grep -qx 'install ok installed'; then
  # APT can reuse an empty Packages file after an InRelease cache hit. Clear
  # empty package indexes under APT's lists lock so they are fetched again.
  sudo env DEBIAN_FRONTEND=noninteractive APT_CONFIG="${CAGE_APT_CONFIG:-/dev/null}" apt-get \
    -o 'APT::Update::Pre-Invoke::=find /var/lib/apt/lists -maxdepth 1 -type f -name "*_Packages*" -empty -delete' \
    -o APT::Update::Error-Mode=any update </dev/null
  sudo env DEBIAN_FRONTEND=noninteractive APT_CONFIG="${CAGE_APT_CONFIG:-/dev/null}" apt-get install -y --no-install-recommends --no-remove chromium-l10n </dev/null
fi
# Publish per-VM control credentials for the browser process and SSH owner only.
sudo env CAGE_DISPLAY_SCALE="${CAGE_DISPLAY_SCALE:-2}" CAGE_WORKSPACE="$PWD" CAGE_USER_HOME="$HOME" python3 - <<'PYCONTROL'
import json, os, secrets, grp, tempfile
from pathlib import Path
def atomic_write(target, text, mode, gid=None):
    fd, name = tempfile.mkstemp(prefix='.' + target.name + '-', dir=target.parent)
    try:
        with os.fdopen(fd, 'w') as f:
            f.write(text)
            f.flush()
            os.fsync(f.fileno())
        os.chmod(name, mode)
        if gid is not None:
            os.chown(name, 0, gid)
        os.replace(name, target)
        # Flush the rename too, or a power loss can bring the old file back.
        directory = os.open(target.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    except Exception:
        try:
            os.unlink(name)
        except OSError:
            pass
        raise
p=Path('/etc/claude-cage/browser-control.json')
try:
    loaded = json.loads(p.read_text()) if p.exists() else {}
    d = loaded if isinstance(loaded, dict) else {}
except (OSError, ValueError):
    d = {}
if not isinstance(d.get('token'), str) or not d['token']:
    d['token'] = secrets.token_hex(32)
scale = 1.5 if os.environ['CAGE_DISPLAY_SCALE'] == '1.5' else 2
d['display_scale'] = scale
# With a settings manager each viewer chooses the density of its own screen.
d['dynamic_scale'] = os.path.exists('/usr/bin/xsettingsd')
scale_file = Path('/etc/claude-cage/display-scale')
atomic_write(scale_file, str(scale) + '\n', 0o644)
d['workspace']=os.environ['CAGE_WORKSPACE']
yaml=Path(os.environ['CAGE_USER_HOME']) / '.config/claude-cage/code-server.yaml'
d['editorPassword']=next(line.split(':',1)[1].strip() for line in yaml.read_text().splitlines() if line.startswith('password:'))
atomic_write(p, json.dumps(d), 0o640, grp.getgrnam('cage-browser').gr_gid)
atomic_write(p.with_name('desktop-downloads.json'), json.dumps({'workspace': d['workspace']}), 0o644)
PYCONTROL
sudo -u cage-browser env CAGE_LANGUAGE="$CAGE_LANGUAGE" CAGE_GUEST_THEME="${CAGE_GUEST_THEME:-}" python3 - <<'PYCODE'
import json, os
from pathlib import Path
p = Path.home() / "profile/Default/Preferences"
import tempfile
p.parent.mkdir(parents=True, exist_ok=True)
try:
    loaded = json.loads(p.read_text()) if p.exists() else {}
except (OSError, ValueError):
    loaded = None
d = loaded if isinstance(loaded, dict) else {}
if p.exists() and not isinstance(loaded, dict) and p.stat().st_size > 0:
    # Keep a damaged file beside the new one instead of losing its settings.
    import time
    backup = p.with_name("%s.corrupt-%s-%d" % (p.name, time.strftime("%Y%m%d%H%M%S"), os.getpid()))
    os.replace(p, backup)
if not isinstance(d.get("intl"), dict):
    d["intl"] = {}
d["intl"]["accept_languages"] = os.environ["CAGE_LANGUAGE"]
d["intl"]["selected_languages"] = os.environ["CAGE_LANGUAGE"]
if not isinstance(d.get("translate"), dict):
    d["translate"] = {}
d["translate"]["enabled"] = False
d["credentials_enable_service"] = False
if not isinstance(d.get("profile"), dict):
    d["profile"] = {}
d["profile"]["password_manager_enabled"] = False
theme = os.environ.get("CAGE_GUEST_THEME", "")
if theme in ("light", "dark"):
    if not isinstance(d.get("browser"), dict):
        d["browser"] = {}
    if not isinstance(d["browser"].get("theme"), dict):
        d["browser"]["theme"] = {}
    d["browser"]["theme"]["color_scheme2"] = 2 if theme == "dark" else 1
fd, name = tempfile.mkstemp(prefix=".Preferences-", dir=p.parent)
try:
    with os.fdopen(fd, "w") as f:
        f.write(json.dumps(d))
        f.flush()
        os.fsync(f.fileno())
    os.replace(name, p)
    directory = os.open(p.parent, os.O_RDONLY)
    try:
        os.fsync(directory)
    finally:
        os.close(directory)
except Exception:
    try:
        os.unlink(name)
    except OSError:
        pass
    raise
PYCODE
if [ "${CAGE_GUEST_THEME:-}" = light ] || [ "${CAGE_GUEST_THEME:-}" = dark ]; then
  sudo -u cage-browser env CAGE_GUEST_THEME="$CAGE_GUEST_THEME" python3 - <<'PYTHEME'
import json, os, tempfile
from pathlib import Path
p = Path("/home/cage-browser/theme.json")
theme = os.environ["CAGE_GUEST_THEME"]
fd, name = tempfile.mkstemp(prefix=".theme-", dir=p.parent)
try:
    os.fchmod(fd, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as f:
        f.write(json.dumps({"variant": theme}))
        f.flush()
        os.fsync(f.fileno())
    os.replace(name, p)
except Exception:
    try:
        os.unlink(name)
    except OSError:
        pass
    raise
PYTHEME
fi
sudo systemctl reset-failed cage-display cage-window-manager cage-browser cage-webvnc 2>/dev/null || true
sudo systemctl reset-failed cage-display-governor 2>/dev/null || true
sudo systemctl restart cage-display cage-window-manager cage-browser cage-webvnc
# Seed only appearance settings for a new editor profile. Existing preferences stay intact.
python3 - <<'PYEDITOR'
import shutil
from pathlib import Path
p = Path.home() / '.local/share/code-server/User/settings.json'
if not p.exists():
    p.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile('/etc/claude-cage/editor-theme.json', p)
PYEDITOR
if [ "${CAGE_GUEST_THEME:-}" = light ] || [ "${CAGE_GUEST_THEME:-}" = dark ]; then
  /usr/local/bin/cage-apply-theme "$CAGE_GUEST_THEME"
fi
# Systemd owns the editor independently of the SSH session and restarts crashes.
(umask 077; tmp=$(mktemp "$HOME/.config/claude-cage/.editor-workspace.XXXXXX"); printf '%s' "$PWD" > "$tmp"; sync "$tmp" 2>/dev/null || true; mv -f "$tmp" "$HOME/.config/claude-cage/editor-workspace"; sync "$HOME/.config/claude-cage" 2>/dev/null || true)
if ! systemctl is-active --quiet cage-editor.service; then
  sudo /usr/local/bin/cage-editor-migrate
  sudo systemctl start cage-editor.service
fi
sudo python3 /usr/local/lib/cage-wait-services.py
printf 'Cage services ready. Timezone=%s Locale=%s Browser=%s\n' "$CAGE_TZ" "$CAGE_LOCALE" "$CAGE_LANGUAGE"
