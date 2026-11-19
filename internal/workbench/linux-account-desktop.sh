#!/bin/bash
# Runs as the workspace user. The caller supplies a validated account ID.
set -euo pipefail
umask 077
id=$1
code=$2
scope=$3
[[ $id =~ ^[0-9a-f]{24}$ && ( $code == default || $code =~ ^[0-9a-f]{24}$ ) ]]
unit="desktop-$id"
base="$HOME/.local/share/desktop-profiles/$id"
runtime="/run/$unit"
mkdir -p "$base"
exec 9>"$base/launch.lock"
flock -w 55 9
if [[ $scope == stop ]]; then
  if systemctl is-active --quiet "$unit.service"; then
    sudo systemctl stop "$unit.service" 2>/dev/null || ! systemctl is-active --quiet "$unit.service"
  fi
  exit 0
fi
if systemctl is-active --quiet "$unit.service"; then exit 0; fi
test -x /usr/bin/claude-desktop
sudo systemctl reset-failed "$unit.service" 2>/dev/null || :
# The controller creates node namespaces. Older administrator-managed
# namespaces keep their Desktop-only binding until the account is changed.
props=()
if [[ $scope == dedicated || $scope == node-* ]]; then
  namespace=$unit
  if [[ $scope == node-* ]]; then route=${scope#node-}; [[ $route =~ ^[0-9a-f]{24}$ ]]; namespace="net-$route"; fi
  test -e "/run/netns/$namespace" || { echo '专属网络尚未就绪'; exit 1; }
  props+=("--property=NetworkNamespacePath=/run/netns/$namespace")
  [[ -s /etc/netns/$namespace/resolv.conf ]] || { echo '专属网络尚未就绪'; exit 1; }
  props+=("--property=BindReadOnlyPaths=/etc/netns/$namespace/resolv.conf:/etc/resolv.conf"
          "--property=InaccessiblePaths=-/run/systemd/resolve/io.systemd.Resolve -/run/dbus/system_bus_socket -/run/nscd/socket")
elif [[ $scope != shared ]]; then exit 2; fi
program=$(mktemp "$base/session.XXXXXX")
trap 'rm -f -- "$program"' EXIT
cat >"$program" <<'SESSION'
set -euo pipefail
umask 077
base=$1
code=$2
if [[ $code == default ]]; then
  config="$base/code"
else
  config="$HOME/.local/share/cage/claude-accounts/$code/config"
fi
mkdir -p "$config"
# client_runtime creates temporary compatibility aliases. Resolve the path
# inside the service's private /tmp so it also exists for the Code tab.
export CLAUDE_CONFIG_DIR
CLAUDE_CONFIG_DIR=$(python3 -I -c 'import runpy,sys;print(runpy.run_path("/usr/local/bin/client_runtime.py")["path"](sys.argv[1]))' "$config")
export HOME="$base/home" XDG_RUNTIME_DIR="$RUNTIME_DIRECTORY"
unset RUNTIME_DIRECTORY INVOCATION_ID JOURNAL_STREAM SYSTEMD_EXEC_PID
export PATH=/usr/bin:/bin XDG_CURRENT_DESKTOP=Openbox
mkdir -p "$HOME" "$base/data" "$HOME/.local/share/applications" "$HOME/.config"
# Give the login browser the same profile, network namespace and callback
# handler as its originating Desktop. gio reads these defaults in this HOME.
python3 - "$base" <<'PY'
from pathlib import Path
import sys
from configparser import ConfigParser
base=Path(sys.argv[1])
apps=Path.home()/'.local/share/applications'
def arg(path):
    return '"'+str(path).replace('\\','\\\\').replace('"','\\"').replace('$','\\$').replace('`','\\`').replace('%','%%')+'"'
# Claude registers this desktop ID again at startup. A per-profile entry
# with the same ID keeps that registration pointed at the original process.
# The portal builds its chooser from the MIME cache, even when the default
# handler in mimeapps.list is already correct. Minimal guests lack the updater.
cache = ConfigParser(interpolation=None)
cache.optionxform = str
cache.read(apps/'mimeinfo.cache')
if 'MIME Cache' not in cache:
    cache.add_section('MIME Cache')
for name,mimes,line in [
 ('com.anthropic.Claude',['claude'],'/usr/bin/claude-desktop '+arg('--user-data-dir='+str(base/'data'))+' %u'),
 ('web',['http','https'],'/usr/bin/python3 -I /usr/local/lib/desktop-auth-browser.py %u')]:
    (apps/(name+'.desktop')).write_text('[Desktop Entry]\nType=Application\nName='+name+'\nExec='+line+'\nMimeType='+''.join('x-scheme-handler/'+m+';' for m in mimes)+'\n')
    for mime in mimes:
        cache['MIME Cache']['x-scheme-handler/'+mime]=name+'.desktop;'
with (apps/'mimeinfo.cache').open('w') as output:
    cache.write(output,space_around_delimiters=False)
(Path.home()/'.config/mimeapps.list').write_text('[Default Applications]\nx-scheme-handler/claude=com.anthropic.Claude.desktop;\nx-scheme-handler/http=web.desktop;\nx-scheme-handler/https=web.desktop;\n')
PY
# Use the desktop-entry parser for quoted arguments when the browser opens a
# custom URI. The generic xdg-open fallback tokenizes Exec with the shell.
mkdir -p "$XDG_RUNTIME_DIR/bin"
printf '%s\n' '#!/bin/sh' 'exec /usr/bin/gio open "$1"' > "$XDG_RUNTIME_DIR/bin/xdg-open"
chmod 0700 "$XDG_RUNTIME_DIR/bin/xdg-open"
export PATH="$XDG_RUNTIME_DIR/bin:$PATH" BROWSER="$XDG_RUNTIME_DIR/bin/xdg-open"
export XAUTHORITY="$XDG_RUNTIME_DIR/Xauthority"
touch "$XAUTHORITY"
cookie=$(mcookie)
# -displayfd picks a free X display under concurrent launches.
xauth -f "$XAUTHORITY" add :0 . "$cookie"
od -An -N4 -tx1 /dev/urandom | tr -d ' \n' > "$XDG_RUNTIME_DIR/password"
tigervncpasswd -f < "$XDG_RUNTIME_DIR/password" > "$XDG_RUNTIME_DIR/vnc-password"
Xtigervnc -displayfd 3 3>"$XDG_RUNTIME_DIR/display" -geometry 1280x800 -depth 24 -rfbport -1 -rfbunixpath "$XDG_RUNTIME_DIR/vnc.sock" -rfbunixmode 0600 -auth "$XAUTHORITY" -nolisten tcp -SecurityTypes VncAuth -PasswordFile "$XDG_RUNTIME_DIR/vnc-password" &
for _ in {1..100}; do test -s "$XDG_RUNTIME_DIR/display" && break; sleep .05; done
export DISPLAY=":$(cat "$XDG_RUNTIME_DIR/display")"
xauth -f "$XAUTHORITY" add "$DISPLAY" . "$cookie"
dbus-update-activation-environment DISPLAY XAUTHORITY HOME XDG_RUNTIME_DIR
cat > "$XDG_RUNTIME_DIR/windows.xml" <<'XML'
<openbox_config xmlns="http://openbox.org/3.4/rc"><desktops><number>1</number></desktops><applications><application type="normal"><decor>no</decor><maximized>yes</maximized></application></applications></openbox_config>
XML
openbox --config-file "$XDG_RUNTIME_DIR/windows.xml" >/dev/null 2>&1 &
# Electron draws its own GTK controls; Openbox decorations alone do not hide them.
printf 'Gtk/DecorationLayout ":"\n' > "$XDG_RUNTIME_DIR/xsettings.conf"
xsettingsd --config="$XDG_RUNTIME_DIR/xsettings.conf" &
python3 - <<'PYSETTINGS'
from Xlib import display
import time
d = display.Display()
selection = d.intern_atom('_XSETTINGS_S0')
for _ in range(60):
    if d.get_selection_owner(selection):
        break
    time.sleep(.05)
else:
    raise SystemExit('Desktop display settings unavailable')
d.close()
PYSETTINGS
mkfifo "$XDG_RUNTIME_DIR/unlock"
gnome-keyring-daemon --unlock --components=secrets < "$XDG_RUNTIME_DIR/unlock" >/dev/null
rm "$XDG_RUNTIME_DIR/unlock"
for _ in {1..60}; do
  gdbus call --session --dest org.freedesktop.secrets --object-path /org/freedesktop/secrets/aliases/default --method org.freedesktop.DBus.Properties.Get org.freedesktop.Secret.Collection Locked 2>/dev/null | grep -q false && break
  sleep .1
done
gdbus call --session --dest org.freedesktop.secrets --object-path /org/freedesktop/secrets/aliases/default --method org.freedesktop.DBus.Properties.Get org.freedesktop.Secret.Collection Locked | grep -q false
notify=$NOTIFY_SOCKET
unset NOTIFY_SOCKET
cd "$HOME"
# Desktop emits the active identity from its native account provider after
# the renderer confirms the login. Keep the filtered stream in the unit's
# private, ephemeral runtime directory; no callback or cookie is inspected.
DESKTOP_LOG_LEVEL=debug CLAUDE_ENABLE_LOGGING=1 DEBUG='*current-account*' claude-desktop "--user-data-dir=$base/data" --password-store=gnome-libsecret > >(/usr/bin/python3 -I /usr/local/lib/desktop-identity.py "$XDG_RUNTIME_DIR/account.json") &
app=$!
printf '%s\n' "$app" > "$XDG_RUNTIME_DIR/app-pid"
for _ in {1..250}; do
  kill -0 "$app"
  if xprop -root _NET_CLIENT_LIST | grep -q '0x[1-9a-f]'; then NOTIFY_SOCKET="$notify" systemd-notify --ready; wait "$app"; exit $?; fi
  sleep .1
done
exit 1
SESSION
user=$(id -un)
sudo systemd-run --quiet --no-block --collect --unit="$unit" --service-type=notify \
  --property="User=$user" --property="RuntimeDirectory=$unit" --property=RuntimeDirectoryMode=0700 \
  --property=NotifyAccess=all --property=TimeoutStartSec=45 --property=TimeoutStopSec=8 \
  --property="ExecStop=/bin/bash /usr/local/lib/desktop-finish.sh $runtime $unit.service" \
  --property=KillMode=control-group --property=PrivateTmp=yes "${props[@]}" \
  /usr/bin/dbus-run-session -- /bin/bash "$program" "$base" "$code"
ok=
trap 'if [[ -z "$ok" ]]; then sudo systemctl stop "$unit.service" >/dev/null 2>&1 || :; fi; rm -f -- "$program"' EXIT
for _ in {1..160}; do test -p "$runtime/unlock" && break; sleep .1; done
test -p "$runtime/unlock"
# The child shell opens the FIFO so the timeout covers open as well as write.
# shellcheck disable=SC2016
timeout 5 sh -c 'cat >"$1/unlock"' sh "$runtime"
for _ in {1..260}; do
  if systemctl is-active --quiet "$unit.service"; then ok=1; exit 0; fi
  sleep .1
done
exit 1
