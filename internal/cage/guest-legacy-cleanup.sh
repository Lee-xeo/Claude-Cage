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
tokens = (b'/usr/local/bin/cage-', b'/usr/local/lib/cage-', b'=/run/cage-', b'unix:/run/cage-', b'LOG_NAMESPACE=cage\0')
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
if legacy_running; then
  printf '%s\n' "$legacy_programs" | while read -r former current; do
    [ -n "$former" ] || continue
    # Forward only to a real, current program; never to itself or a link.
    if [ -e "$former" ] && [ -f "$current" ] && [ ! -L "$current" ] && [ -x "$current" ]; then
      printf '#!/bin/sh\nexec %s "$@"\n' "$current" | sudo tee "$former.new" >/dev/null
      sudo chmod 0755 "$former.new"
      sudo mv -f "$former.new" "$former"
    fi
  done
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
fi
