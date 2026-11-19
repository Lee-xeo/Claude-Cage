# One-off migration of environments installed by earlier versions. It runs
# from the session that applies the components, before they are installed,
# and is never written into the environment. Former names appear only here and
# in the cleanup that follows the start of the current services. Every step is
# idempotent.

# Services of the browser, editor and audio output restart under their current
# names below. Stop the former units first: they hold the same displays, ports
# and sockets. Their programs keep running only where something still uses them.
for former in cage-webvnc cage-browser cage-window-manager cage-display-governor cage-display cage-editor cage-audio cage-vnc; do
  if [ -e "/etc/systemd/system/$former.service" ]; then
    sudo systemctl stop "$former.service" 2>/dev/null || true
    sudo rm -f "/etc/systemd/system/$former.service"
  fi
done
# The compressed memory reserve stays in place; only its unit is renamed.
if [ -e /etc/systemd/system/cage-memory.service ]; then
  sudo systemctl disable cage-memory.service >/dev/null 2>&1 || true
  sudo rm -f /etc/systemd/system/cage-memory.service
fi
if [ -f /run/cage-zram-device ] && [ ! -e /run/memory-reserve-device ]; then
  sudo mv /run/cage-zram-device /run/memory-reserve-device
fi
# Clients read every file in this directory; the former one names a socket
# that no longer exists.
sudo rm -f /etc/pulse/client.conf.d/99-cage.conf
# An open Claude Desktop keeps running under its former unit and runtime
# directory until the user closes it. The current names refer to it meanwhile,
# so that it can still be viewed, receive links and files, and be closed.
if systemctl is-active --quiet cage-claude-desktop.service; then
  if [ ! -e /etc/systemd/system/desktop-session.service ] && [ ! -L /etc/systemd/system/desktop-session.service ]; then
    sudo ln -s cage-claude-desktop.service /etc/systemd/system/desktop-session.service
  fi
  if [ -d /run/cage-desktop ] && [ ! -e /run/desktop-session ] && [ ! -L /run/desktop-session ]; then
    sudo ln -s /run/cage-desktop /run/desktop-session
  fi
fi
sudo systemctl daemon-reload
# Workbench terminals select their prompt by a variable whose name changed.
# Only the exact block earlier versions appended is replaced.
python3 - <<'LEGACY_PROMPT'
from pathlib import Path
p = Path.home() / '.bashrc'
former = b"""# CAGE_WORKBENCH_PROMPT
if [ "${CAGE_WORKBENCH:-}" = 1 ]; then
"""
current = b"""# WORKBENCH_TERMINAL_PROMPT
if [ "${WORKBENCH_TERMINAL:-}" = 1 ]; then
"""
if p.is_file() and not p.is_symlink():
    before = p.read_bytes()
    if former in before:
        p.write_bytes(before.replace(former, current, 1))
LEGACY_PROMPT
# Downloaded tool archives are a cache; keep it under its current name. The
# image paste lock of earlier versions lived beside it.
if [ -d "$HOME/.cache/cage/downloads" ] && [ ! -e "$HOME/.cache/tool-downloads" ]; then
  mv "$HOME/.cache/cage/downloads" "$HOME/.cache/tool-downloads"
fi
if [ -d "$HOME/.cache/cage" ]; then
  rm -f "$HOME/.cache/cage/image-input.lock"
  rmdir "$HOME/.cache/cage" 2>/dev/null || true
fi
