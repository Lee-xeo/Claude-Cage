#!/bin/bash
set -eu
export DISPLAY=:78 XAUTHORITY=/run/desktop-session/Xauthority
export XDG_RUNTIME_DIR=/run/phone-desktop
exec /usr/bin/python3 /usr/local/lib/phone-desktop/view.py "$@"
