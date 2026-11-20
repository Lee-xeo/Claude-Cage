# One-off migration of environments installed by earlier versions. It runs
# from the session that applies the components, before they are installed,
# and is never written into the environment. Former names appear only here and
# in the cleanup that follows the start of the current services. Every step is
# idempotent.

# Settings and credentials of earlier versions: the browser control token,
# display and sign-in preferences, the editor and viewer passwords. Each file
# is copied to the current directory only when that has no file of the same
# name, compared with its source, and only then put in place. This happens once:
# a marker records it, so later changes in the current directory (a preference
# turned off by removing its file) are never undone from the former one. The
# former directories stay as they are. A failed copy stops the update before
# anything else changes; the next attempt starts again.
legacy_copy=$(cat <<'LEGACY_COPY'
import filecmp, os, shutil, stat, sys
former, current, mode = sys.argv[1], sys.argv[2], int(sys.argv[3], 8)
marker = os.path.join(current, '.settings-copied')
if not os.path.isdir(former) or os.path.islink(former) or os.path.exists(marker):
    sys.exit(0)
if not os.path.isdir(current):
    os.makedirs(current, mode)
    os.chmod(current, mode)
for name in sorted(os.listdir(former)):
    source, target = os.path.join(former, name), os.path.join(current, name)
    info = os.lstat(source)
    # The component manifest is written anew once the update completes.
    if name == 'components.json' or os.path.lexists(target) or not stat.S_ISREG(info.st_mode):
        continue
    staged = os.path.join(current, '.' + name + '.copy')
    shutil.copy2(source, staged)
    os.chown(staged, info.st_uid, info.st_gid)
    # On disk before it takes the name: a power loss never leaves an empty
    # setting where the copy was expected.
    with open(staged, 'rb') as stream:
        os.fsync(stream.fileno())
    if not filecmp.cmp(source, staged, shallow=False):
        os.unlink(staged)
        sys.exit('Copying ' + name + ' did not verify; earlier settings are unchanged.')
    os.replace(staged, target)
# A copy interrupted before it took its name is left from an earlier attempt
# whose source has since gone.
for name in os.listdir(current):
    if name.startswith('.') and name.endswith('.copy') and not os.path.lexists(os.path.join(former, name[1:-5])):
        os.unlink(os.path.join(current, name))
with open(marker, 'w') as stream:
    os.fsync(stream.fileno())
directory = os.open(current, os.O_RDONLY | os.O_DIRECTORY)
os.fsync(directory)
os.close(directory)
LEGACY_COPY
)
sudo python3 -I -c "$legacy_copy" /etc/claude-cage /etc/workbench 755
python3 -I -c "$legacy_copy" "$HOME/.config/claude-cage" "$HOME/.config/workbench" 700
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
import os, stat, tempfile
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
        mode = stat.S_IMODE(p.stat().st_mode)
        with tempfile.NamedTemporaryFile(dir=p.parent, prefix='.bashrc.', delete=False) as stream:
            stream.write(before.replace(former, current, 1))
            stream.flush()
            os.fsync(stream.fileno())
            staged = stream.name
        try:
            os.chmod(staged, mode)
            os.replace(staged, p)
        except BaseException:
            os.unlink(staged)
            raise
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
