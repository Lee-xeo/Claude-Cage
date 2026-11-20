# Remove what earlier versions installed under their former names. It runs
# from the session that applies the components, after the current services
# started, and is never installed. Processes started before the update (an
# open Claude Desktop, workbench terminals, Claude sessions) may still run the
# former programs; until none of them is left, each former program forwards to
# its current replacement and the former files stay. Only exact, known paths
# are touched; user data is never removed here.
legacy_programs='
/usr/local/bin/cage-claude-account /usr/local/libexec/claude-account
/usr/local/lib/cage-claude-bin/claude /usr/local/libexec/claude-wrapper/claude
/usr/local/bin/cage-claude-session /usr/local/libexec/claude-desktop-session
/usr/local/bin/cage-claude-open /usr/local/libexec/claude-desktop-open
/usr/local/bin/cage-claude-start /usr/local/libexec/claude-desktop-start
/usr/local/bin/cage-desktop-files /usr/local/libexec/desktop-files
/usr/local/bin/cage-display-governor /usr/local/libexec/display-governor
/usr/local/bin/cage-editor-migrate /usr/local/libexec/editor-migrate
/usr/local/bin/cage-editor-start /usr/local/libexec/editor-start
/usr/local/bin/cage-image-input /usr/local/libexec/image-input
/usr/local/bin/cage-input-session /usr/local/libexec/input-session
/usr/local/bin/cage-log-run /usr/local/libexec/service-log
/usr/local/bin/cage-memory-start /usr/local/libexec/memory-reserve
/usr/local/bin/cage-native-input /usr/local/libexec/native-input
/usr/local/bin/cage-save-download /usr/local/libexec/save-download
/usr/local/bin/cage-shared-clipboard /usr/local/libexec/shared-clipboard
/usr/local/bin/cage-open /usr/local/bin/xdg-open
'
legacy_files='
/usr/local/lib/cage-browser-agent.mjs
/usr/local/lib/cage-wait-services.py
/usr/local/lib/cage-desktop-downloads.py
/usr/local/share/cage-webvnc
'
legacy_directories='
/usr/local/lib/cage-claude-bin
/usr/local/lib/cage-phone-desktop
'
# Login shells load the current profile script, installed above, which zsh
# now also names; drop only the exact line earlier versions appended.
if [ -f /etc/zsh/zshenv ] && grep -qx '\. /etc/profile\.d/claude-cage\.sh' /etc/zsh/zshenv; then
  sudo sed -i '/^\. \/etc\/profile\.d\/claude-cage\.sh$/d' /etc/zsh/zshenv
fi
sudo rm -f /etc/profile.d/claude-cage.sh /etc/chromium/policies/managed/cage-password-vault.json
# The browser account's services were restarted with the current programs and
# rules; the work account keeps its own administrator rights.
for former in /etc/sudoers.d/cage-claude-desktop /etc/sudoers.d/cage-download /etc/sudoers.d/cage-editor /etc/sudoers.d/cage-input /etc/sudoers.d/cage-phone-desktop; do
  sudo rm -f -- "$former"
done
# A process started before the update still carries one of the former
# locations in its environment or command line. The session applying the
# components is excluded: it inherits the login environment of the time. A
# process that only waits for it may keep the former files until the next
# start of the environment, which removes them.
legacy_running() {
  sudo python3 -I - <<'LEGACY_RUNNING'
import os, sys
tokens = (b'/usr/local/bin/cage-', b'/usr/local/lib/cage-', b'=/run/cage-', b'unix:/run/cage-', b'LOG_NAMESPACE=cage\0', b'CAGE_WORKBENCH=')
own = set()
pid = os.getpid()
while pid > 1:
    own.add(pid)
    try:
        with open(f'/proc/{pid}/stat', 'rb') as f:
            pid = int(f.read().rsplit(b')', 1)[1].split()[1])
    except (OSError, ValueError, IndexError):
        break
for entry in os.listdir('/proc'):
    if not entry.isdigit() or int(entry) in own:
        continue
    try:
        with open(f'/proc/{entry}/environ', 'rb') as f:
            data = f.read() + b'\0'
        with open(f'/proc/{entry}/cmdline', 'rb') as f:
            data += f.read()
    except OSError:
        continue
    if any(token in data for token in tokens):
        sys.exit(0)
sys.exit(1)
LEGACY_RUNNING
}
# Claude Desktop opened before the update keeps its former unit until it is
# closed; the migration pointed the current names at it meanwhile.
if ! systemctl is-active --quiet cage-claude-desktop.service; then
  if [ "$(readlink /etc/systemd/system/desktop-session.service 2>/dev/null)" = cage-claude-desktop.service ]; then
    sudo rm -f /etc/systemd/system/desktop-session.service
  fi
  if [ "$(readlink /run/desktop-session 2>/dev/null)" = /run/cage-desktop ]; then
    sudo rm -f /run/desktop-session
  fi
  if [ -e /etc/systemd/system/cage-claude-desktop.service ]; then
    sudo rm -f /etc/systemd/system/cage-claude-desktop.service
  fi
  sudo systemctl daemon-reload
fi
if legacy_running; then
  # Programs started before the update read the former variable names.
  printf '%s\n' "$legacy_programs" | while read -r former current; do
    [ -n "$former" ] || continue
    # Forward only to a real, current program; never to itself or a link.
    if [ -e "$former" ] && [ -f "$current" ] && [ ! -L "$current" ] && [ -x "$current" ]; then
      printf '%s\n' '#!/bin/sh' \
        '[ -z "${CAGE_CLAUDE_ACCOUNT_ID:-}" ] || export ACCOUNT_PROFILE_ID="$CAGE_CLAUDE_ACCOUNT_ID"' \
        '[ -z "${CAGE_CLAUDE_SUBJECT:-}" ] || export ACCOUNT_SUBJECT="$CAGE_CLAUDE_SUBJECT"' \
        '[ -z "${CAGE_CLAUDE_LOGIN:-}" ] || export LOGIN_ID="$CAGE_CLAUDE_LOGIN"' \
        'unset CAGE_CLAUDE_ACCOUNT_ID CAGE_CLAUDE_SUBJECT CAGE_CLAUDE_LOGIN CAGE_WORKBENCH' \
        "exec $current \"\$@\"" | sudo tee "$former.new" >/dev/null
      sudo chmod 0755 "$former.new"
      sudo mv -f "$former.new" "$former"
    fi
  done
  # Their audio clients still name the former socket directory.
  if [ ! -e /run/cage-audio ] && [ -d /run/audio-output ]; then
    sudo ln -s /run/audio-output /run/cage-audio
  fi
else
  printf '%s\n' "$legacy_programs" | while read -r former _; do
    [ -n "$former" ] && sudo rm -f -- "$former"
  done
  printf '%s\n' "$legacy_files" | while read -r former; do
    [ -n "$former" ] && sudo rm -f -- "$former"
  done
  printf '%s\n' "$legacy_directories" | while read -r former; do
    [ -n "$former" ] && sudo rm -rf --one-file-system -- "$former"
  done
  if [ -L /run/cage-audio ]; then sudo rm -f /run/cage-audio; fi
  # Sockets and display settings of the former input and display helpers.
  sudo rm -rf --one-file-system /home/cage-browser/.cache/cage-display /home/cage-browser/.cache/cage-input /home/cage/.cache/cage-input
  # The former journal namespace's settings; its entries stay until they expire.
  sudo rm -rf --one-file-system /etc/systemd/journald@cage.conf.d
  # The audio group of earlier versions has no members left that need it.
  if getent group cage-audio >/dev/null; then sudo groupdel cage-audio 2>/dev/null || true; fi
fi
