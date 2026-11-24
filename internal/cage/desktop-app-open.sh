#!/bin/bash
# Hands a sign-in link from the browser to the running desktop application
# that registered its scheme. The link arrives on standard input, never in
# arguments that sudo would record.
set -euo pipefail
[[ ${1:-} == --stdin && $# -eq 1 ]] || exit 2
url=$(head -c 8193)
(( ${#url} > 0 && ${#url} <= 8192 )) || exit 2
[[ $url != *[[:cntrl:]]* ]] || exit 2
scheme=${url%%:*}
[[ $scheme =~ ^[a-z][a-z0-9+.-]{0,39}$ && $url == "$scheme:"* ]] || exit 2
registry=/usr/local/lib/desktop-apps/schemes
id=$(awk -v s="$scheme" '$1 == s && $2 ~ /^[0-9a-f]+$/ && length($2) == 24 {print $2; exit}' "$registry" 2>/dev/null || :)
[[ -n $id ]] || exit 1
runtime="/run/desktop-$id"
systemctl is-active --quiet "desktop-$id.service" || { echo '应用尚未打开' >&2; exit 1; }
test -s "$runtime/display" && test -s "$runtime/bus-address"
mapfile -d '' -t command < <(python3 -I -c 'import json,sys
for value in json.load(open(sys.argv[1]))["command"]: sys.stdout.write(value + "\0")' "/usr/local/lib/desktop-apps/$id/app.json")
(( ${#command[@]} > 0 ))
set -a
# shellcheck disable=SC1091
[[ -r /etc/claude-cage/environment ]] && . /etc/claude-cage/environment
set +a
export DISPLAY=":$(cat "$runtime/display")" XAUTHORITY="$runtime/Xauthority" XDG_RUNTIME_DIR="$runtime"
DBUS_SESSION_BUS_ADDRESS=$(cat "$runtime/bus-address")
export DBUS_SESSION_BUS_ADDRESS PATH="$runtime/bin:/usr/local/bin:/usr/bin:/bin" BROWSER="$runtime/bin/xdg-open"
cd "$HOME"
# A second launch hands the link to the window that is already open.
exec /usr/bin/python3 -I /usr/local/bin/client_runtime.py "${command[@]}" "$url"
