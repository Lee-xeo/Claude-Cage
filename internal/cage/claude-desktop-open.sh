#!/usr/bin/env bash
set -euo pipefail
test "$#" -eq 1
test "${#1}" -le 8192
case "$1" in claude://*) ;; *) exit 2;; esac
systemctl is-active --quiet desktop-session.service || exit 1
export DISPLAY=:78 XAUTHORITY=/run/desktop-session/Xauthority XDG_RUNTIME_DIR=/run/desktop-session
export DBUS_SESSION_BUS_ADDRESS="$(cat /run/desktop-session/bus-address)"
exec /usr/bin/claude-desktop "$1"
