#!/bin/bash
# Runs as the workspace user. Starts or stops one plugin desktop application in
# a display of its own. The caller supplies a validated instance ID; what the
# instance runs comes only from the record its installation wrote as root.
set -euo pipefail
umask 077
id=$1
action=$2
[[ $id =~ ^[0-9a-f]{24}$ ]]
unit="desktop-$id"
runtime="/run/$unit"
record="/usr/local/lib/desktop-apps/$id/app.json"
base="$HOME/.local/share/desktop-apps/$id"
mkdir -p "$base"
exec 9>"$base/launch.lock"
flock -w 55 9
if [[ $action == stop ]]; then
  if systemctl is-active --quiet "$unit.service"; then
    sudo systemctl stop "$unit.service" 2>/dev/null || ! systemctl is-active --quiet "$unit.service"
  fi
  exit 0
fi
[[ $action == start ]] || exit 2
if systemctl is-active --quiet "$unit.service"; then exit 0; fi
test -s "$record" || { echo '应用尚未安装' >&2; exit 1; }
# The record is root's. A program outside the installed release, or one that
# is missing, stops here instead of running something else.
python3 -I - "$record" <<'CHECK'
import json, os, sys
record = json.load(open(sys.argv[1]))
program = record["command"][0]
if not (os.path.isabs(program) and (program.startswith("/usr/") or program.startswith("/opt/")) and "/../" not in program):
    sys.exit("应用记录无效")
if not os.access(program, os.X_OK):
    sys.exit("应用程序缺失，请修复安装")
CHECK
sudo systemctl reset-failed "$unit.service" 2>/dev/null || :
program=$(mktemp "$base/session.XXXXXX")
trap 'rm -f -- "$program"' EXIT
cat >"$program" <<'SESSION'
set -euo pipefail
umask 077
base=$1
record=$2
export XDG_RUNTIME_DIR="$RUNTIME_DIRECTORY"
unset RUNTIME_DIRECTORY INVOCATION_ID JOURNAL_STREAM SYSTEMD_EXEC_PID MANAGERPID
rm -f "$XDG_RUNTIME_DIR/stopping"
app=
# shellcheck disable=SC2329
on_term() {
  if [[ -n "$app" ]]; then
    kill -TERM "$app" 2>/dev/null || :
  fi
  if [[ -f "$XDG_RUNTIME_DIR/stopping" ]]; then
    rm -f "$XDG_RUNTIME_DIR/stopping"
    exit 0
  fi
  trap - TERM
  kill -TERM "$$"
}
trap on_term TERM
export PATH=/usr/local/bin:/usr/bin:/bin XDG_CURRENT_DESKTOP=Openbox
mapfile -d '' -t command < <(python3 -I -c 'import json,sys
for value in json.load(open(sys.argv[1]))["command"]: sys.stdout.write(value + "\0")' "$record")
mapfile -t schemes < <(python3 -I -c 'import json,sys
print("\n".join(json.load(open(sys.argv[1])).get("schemes", [])))' "$record")
# Web addresses go to the browser and the application's own sign-in links
# back to the application. Local files and folders go to this session's file
# broker, which resolves them only within shared project folders.
mkdir -p "$XDG_RUNTIME_DIR/bin"
{
  printf '#!/bin/sh\ncase "${1:-}" in\n  http://*|https://*|about:blank) exec /usr/local/bin/cage-open "$1" ;;\n'
  for scheme in "${schemes[@]}"; do
    [[ $scheme =~ ^[a-z][a-z0-9+.-]*$ ]] || continue
    printf '  %s:*) exec %q "$1" ;;\n' "$scheme" "${command[0]}"
  done
  printf '  *) exec /usr/local/bin/cage-desktop-files --open "${1:-}" ;;\nesac\n'
} > "$XDG_RUNTIME_DIR/bin/xdg-open"
chmod 0700 "$XDG_RUNTIME_DIR/bin/xdg-open"
export PATH="$XDG_RUNTIME_DIR/bin:$PATH" BROWSER="$XDG_RUNTIME_DIR/bin/xdg-open"
export XAUTHORITY="$XDG_RUNTIME_DIR/Xauthority"
touch "$XAUTHORITY"
cookie=$(mcookie)
xauth -f "$XAUTHORITY" add :0 . "$cookie"
od -An -N4 -tx1 /dev/urandom | tr -d ' \n' > "$XDG_RUNTIME_DIR/password"
tigervncpasswd -f < "$XDG_RUNTIME_DIR/password" > "$XDG_RUNTIME_DIR/vnc-password"
# -displayfd picks a free X display under concurrent launches.
Xtigervnc -displayfd 3 3>"$XDG_RUNTIME_DIR/display" -geometry 1280x800 -depth 24 -rfbport -1 -rfbunixpath "$XDG_RUNTIME_DIR/vnc.sock" -rfbunixmode 0600 -auth "$XAUTHORITY" -nolisten tcp -SecurityTypes VncAuth -PasswordFile "$XDG_RUNTIME_DIR/vnc-password" &
for _ in {1..100}; do test -s "$XDG_RUNTIME_DIR/display" && break; sleep .05; done
export DISPLAY=":$(cat "$XDG_RUNTIME_DIR/display")"
xauth -f "$XAUTHORITY" add "$DISPLAY" . "$cookie"
dbus-update-activation-environment DISPLAY XAUTHORITY HOME XDG_RUNTIME_DIR TZ LANG LC_ALL
# Sign-in links that come back from the browser start a second process in
# this session, which hands them to the open window.
printf '%s' "$DBUS_SESSION_BUS_ADDRESS" > "$XDG_RUNTIME_DIR/bus-address"
cat > "$XDG_RUNTIME_DIR/windows.xml" <<'XML'
<openbox_config xmlns="http://openbox.org/3.4/rc"><desktops><number>1</number></desktops><applications><application type="normal"><decor>no</decor><maximized>yes</maximized></application></applications></openbox_config>
XML
openbox --config-file "$XDG_RUNTIME_DIR/windows.xml" >/dev/null 2>&1 &
# Electron draws its own GTK controls; Openbox decorations alone do not hide them.
printf 'Gtk/DecorationLayout ":"\n' > "$XDG_RUNTIME_DIR/xsettings.conf"
xsettingsd --config="$XDG_RUNTIME_DIR/xsettings.conf" &
python3 -I - <<'PYSETTINGS'
from Xlib import display
import time
d = display.Display()
selection = d.intern_atom('_XSETTINGS_S0')
for _ in range(60):
    if d.get_selection_owner(selection):
        break
    time.sleep(.05)
else:
    raise SystemExit('Display settings unavailable')
d.close()
PYSETTINGS
# The application keeps its sign-in in a keyring of its own, so it never
# shares a keyring file with another running session in the same home.
mkdir -p "$base/keyring"
mkfifo "$XDG_RUNTIME_DIR/unlock"
XDG_DATA_HOME="$base/keyring" gnome-keyring-daemon --unlock --components=secrets < "$XDG_RUNTIME_DIR/unlock" >/dev/null
rm "$XDG_RUNTIME_DIR/unlock"
for _ in {1..60}; do
  gdbus call --session --dest org.freedesktop.secrets --object-path /org/freedesktop/secrets/aliases/default --method org.freedesktop.DBus.Properties.Get org.freedesktop.Secret.Collection Locked 2>/dev/null | grep -q false && break
  sleep .1
done
gdbus call --session --dest org.freedesktop.secrets --object-path /org/freedesktop/secrets/aliases/default --method org.freedesktop.DBus.Properties.Get org.freedesktop.Secret.Collection Locked | grep -q false
touch "$XDG_RUNTIME_DIR/keyring-ready"
if [[ -x /usr/local/bin/cage-desktop-files ]]; then
  /usr/local/bin/cage-desktop-files &
  files_pid=$!
  for _ in {1..100}; do
    kill -0 "$files_pid"
    if [[ -S "$XDG_RUNTIME_DIR/files.sock" ]]; then break; fi
    sleep .1
  done
  test -S "$XDG_RUNTIME_DIR/files.sock"
  export GTK_USE_PORTAL=1
fi
notify=$NOTIFY_SOCKET
unset NOTIFY_SOCKET
cd "$HOME"
# The input method keeps its sockets under a home of the instance's own; the
# application itself runs in the user's home with the user's projects.
input_session=()
if [[ -x /usr/local/bin/cage-input-session ]]; then
  mkdir -p "$base/input"
  input_session=(/usr/bin/env HOME="$base/input" /usr/local/bin/cage-input-session /usr/bin/env HOME="$HOME")
fi
# The neutral launcher keeps the application's own state out of the
# environment it passes on and renames the audio and input socket paths.
client_runtime=()
if [[ -r /usr/local/bin/client_runtime.py ]]; then client_runtime=(/usr/bin/python3 -I /usr/local/bin/client_runtime.py); fi
${input_session[@]+"${input_session[@]}"} ${client_runtime[@]+"${client_runtime[@]}"} "${command[@]}" >/dev/null 2>&1 &
app=$!
printf '%s\n' "$app" > "$XDG_RUNTIME_DIR/app-pid"
for _ in {1..450}; do
  if ! kill -0 "$app" 2>/dev/null; then
    status=0
    wait "$app" || status=$?
    if [[ -f "$XDG_RUNTIME_DIR/stopping" ]]; then
      rm -f "$XDG_RUNTIME_DIR/stopping"
      exit 0
    fi
    if [[ "$status" -ne 0 ]]; then
      exit "$status"
    fi
    exit 1
  fi
  if xprop -root _NET_CLIENT_LIST 2>/dev/null | grep -q '0x[1-9a-f]'; then
    NOTIFY_SOCKET="$notify" systemd-notify --ready
    status=0
    wait "$app" || status=$?
    if [[ -f "$XDG_RUNTIME_DIR/stopping" ]]; then
      rm -f "$XDG_RUNTIME_DIR/stopping"
      exit 0
    fi
    exit "$status"
  fi
  sleep .1
done
if [[ -f "$XDG_RUNTIME_DIR/stopping" ]]; then
  rm -f "$XDG_RUNTIME_DIR/stopping"
  exit 0
fi
exit 1
SESSION
user=$(id -un)
sudo systemd-run --quiet --no-block --collect --unit="$unit" --service-type=notify \
  --property="User=$user" --property="RuntimeDirectory=$unit" --property=RuntimeDirectoryMode=0700 \
  --property=NotifyAccess=all --property=TimeoutStartSec=60 --property=TimeoutStopSec=8 \
  --property="ExecStop=/bin/bash /usr/local/lib/desktop-finish.sh $runtime $unit.service" \
  --property=KillMode=control-group --property=EnvironmentFile=-/etc/claude-cage/environment \
  /usr/bin/dbus-run-session -- /bin/bash "$program" "$base" "$record"
ok=
trap 'if [[ -z "$ok" ]]; then sudo systemctl stop "$unit.service" >/dev/null 2>&1 || :; fi; rm -f -- "$program"' EXIT
for _ in {1..160}; do test -p "$runtime/unlock" && break; sleep .1; done
test -p "$runtime/unlock"
# The child shell opens the FIFO so the timeout covers open as well as write.
# shellcheck disable=SC2016
timeout 5 sh -c 'cat >"$1/unlock"' sh "$runtime"
for _ in {1..460}; do
  if systemctl is-active --quiet "$unit.service"; then ok=1; exit 0; fi
  if systemctl is-failed --quiet "$unit.service"; then exit 1; fi
  sleep .1
done
exit 1
