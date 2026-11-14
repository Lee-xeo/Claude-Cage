#!/usr/bin/env bash
set -euo pipefail
sudo env DEBIAN_FRONTEND=noninteractive APT_CONFIG="${CAGE_APT_CONFIG:-/dev/null}" apt-get install -y --no-install-recommends locales tzdata tigervnc-standalone-server openbox x11-xserver-utils tigervnc-scraping-server tigervnc-tools novnc websockify fonts-noto-cjk fonts-noto-color-emoji python3-xlib xsettingsd
if ! id cage-browser >/dev/null 2>&1; then
  sudo useradd --create-home --shell /bin/bash cage-browser
fi
python3 - <<'CAGE_BROWSER_ACCOUNT'
import os, pwd
account = pwd.getpwnam('cage-browser')
if account.pw_uid < 1000 or account.pw_uid == os.getuid() or account.pw_dir != '/home/cage-browser':
    raise SystemExit('The browser requires its own ordinary account and home directory.')
CAGE_BROWSER_ACCOUNT
