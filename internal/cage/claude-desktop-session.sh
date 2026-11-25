#!/usr/bin/env bash
set -euo pipefail
# Keep dbus-daemon and other children from announcing readiness for the app.
if [ "${CAGE_DESKTOP_SESSION:-}" != 1 ]; then
  export CAGE_DESKTOP_SESSION=1 CAGE_NOTIFY_SOCKET="$NOTIFY_SOCKET"
  unset NOTIFY_SOCKET
  exec dbus-run-session -- "$0"
fi
export DISPLAY=:78 XAUTHORITY=/run/cage-desktop/Xauthority
export XDG_RUNTIME_DIR=/run/cage-desktop XDG_CURRENT_DESKTOP=Openbox
export PATH="$HOME/.local/bin:$PATH" BROWSER=/usr/local/bin/cage-open
dbus-update-activation-environment DISPLAY XAUTHORITY XDG_RUNTIME_DIR XDG_CURRENT_DESKTOP
umask 077
rm -f "$XDG_RUNTIME_DIR/stopping"
app_pid=
# shellcheck disable=SC2329
on_term() {
  if [ -n "${app_pid:-}" ]; then
    kill -TERM "$app_pid" 2>/dev/null || true
  fi
  if [ -f "$XDG_RUNTIME_DIR/stopping" ]; then
    rm -f "$XDG_RUNTIME_DIR/stopping"
    exit 0
  fi
  trap - TERM
  kill -TERM "$$"
}
trap on_term TERM
touch "$XAUTHORITY"
xauth -f "$XAUTHORITY" add "$DISPLAY" . "$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')"
printf '%s' "$DBUS_SESSION_BUS_ADDRESS" > "$XDG_RUNTIME_DIR/bus-address"
password="$HOME/.config/claude-cage/desktop-password"
if [ ! -s "$password" ]; then
  mkdir -p "$(dirname "$password")"
  od -An -N4 -tx1 /dev/urandom | tr -d ' \n' > "$password"
fi
tigervncpasswd -f < "$password" > "$XDG_RUNTIME_DIR/vnc-password"
Xtigervnc "$DISPLAY" -geometry 1600x1000 -depth 24 -rfbport -1 -rfbunixpath "$XDG_RUNTIME_DIR/vnc.sock" -rfbunixmode 0600 -nolisten tcp -auth "$XAUTHORITY" -SecurityTypes VncAuth -PasswordFile "$XDG_RUNTIME_DIR/vnc-password" -AlwaysShared &
display_pid=$!
trap 'kill "$display_pid" 2>/dev/null || true' EXIT
for _ in $(seq 1 100); do
  if xdpyinfo -display "$DISPLAY" >/dev/null 2>&1; then break; fi
  kill -0 "$display_pid"
  sleep .1
done
xdpyinfo -display "$DISPLAY" >/dev/null
openbox --config-file /etc/claude-cage/desktop-openbox.xml &
# Lower the refresh rate of unattended animation. Optional on older guests.
if [ -x /usr/local/bin/cage-display-governor ]; then
  /usr/local/bin/cage-display-governor &
fi
mkfifo -m 0600 "$XDG_RUNTIME_DIR/unlock"
gnome-keyring-daemon --unlock --components=secrets < "$XDG_RUNTIME_DIR/unlock" >/dev/null
rm "$XDG_RUNTIME_DIR/unlock"
# Do not replace or erase an existing keyring when the supplied key is wrong.
keyring_unlocked() {
  gdbus call --session --dest org.freedesktop.secrets --object-path /org/freedesktop/secrets/aliases/login --method org.freedesktop.DBus.Properties.Get org.freedesktop.Secret.Collection Locked 2>/dev/null | grep -q false &&
  gdbus call --session --dest org.freedesktop.secrets --object-path /org/freedesktop/secrets/aliases/default --method org.freedesktop.DBus.Properties.Get org.freedesktop.Secret.Collection Locked 2>/dev/null | grep -q false
}
for _ in $(seq 1 50); do
  if keyring_unlocked; then break; fi
  sleep .1
done
keyring_unlocked || { echo '无法解锁已有密钥环，登录数据未修改。' >&2; exit 1; }
touch "$XDG_RUNTIME_DIR/keyring-ready"
# Record the running application's density for clients that reconnect later.
display_scale=2
if [[ -r /etc/claude-cage/display-scale && $(< /etc/claude-cage/display-scale) == 1.5 ]]; then
  display_scale=1.5
fi
printf '%s\n' "$display_scale" > "$XDG_RUNTIME_DIR/display-scale"
printf '%s\n' "$display_scale" > "$XDG_RUNTIME_DIR/base-display-scale"
scale_args=(--force-device-scale-factor="$display_scale")
if command -v xsettingsd >/dev/null; then
  dpi=$(awk -v scale="$display_scale" 'BEGIN {printf "%.0f", scale * 96 * 1024}')
  # Settings that stay as they are when a viewer changes the density.
  # Cage owns the embedded app's window controls; keep its native buttons hidden.
  printf 'Gtk/DecorationLayout ":"\n' > "$XDG_RUNTIME_DIR/xsettings.fixed"
  if [ "$display_scale" = 1.5 ]; then
    # Favoring smoothness gives up the app's animations too: while one plays,
    # the app repaints its whole message area on every frame.
    printf 'Gtk/EnableAnimations 0\n' >> "$XDG_RUNTIME_DIR/xsettings.fixed"
  fi
  guest_theme=
  if [ -r "$HOME/.config/claude-cage/theme-variant" ]; then
    read -r guest_theme < "$HOME/.config/claude-cage/theme-variant" || true
    if [ "$guest_theme" = dark ]; then
      printf 'Net/ThemeName "Adwaita-dark"\nGtk/ApplicationPreferDarkTheme 1\n' >> "$XDG_RUNTIME_DIR/xsettings.fixed"
    elif [ "$guest_theme" = light ]; then
      printf 'Net/ThemeName "Adwaita"\nGtk/ApplicationPreferDarkTheme 0\n' >> "$XDG_RUNTIME_DIR/xsettings.fixed"
    fi
  fi
  if [ -n "$guest_theme" ] && command -v gsettings >/dev/null 2>&1; then
    if [ "$guest_theme" = dark ]; then
      gsettings set org.gnome.desktop.interface color-scheme prefer-dark 2>/dev/null || true
      gsettings set org.gnome.desktop.interface gtk-theme Adwaita-dark 2>/dev/null || true
    elif [ "$guest_theme" = light ]; then
      gsettings set org.gnome.desktop.interface color-scheme prefer-light 2>/dev/null || true
      gsettings set org.gnome.desktop.interface gtk-theme Adwaita 2>/dev/null || true
    fi
  fi
  { printf 'Xft/DPI %s\nGdk/WindowScalingFactor 1\nGdk/UnscaledDPI %s\n' "$dpi" "$dpi"; cat "$XDG_RUNTIME_DIR/xsettings.fixed"; } > "$XDG_RUNTIME_DIR/xsettings.conf"
  xsettingsd --config="$XDG_RUNTIME_DIR/xsettings.conf" &
  printf '%s\n' "$!" > "$XDG_RUNTIME_DIR/xsettings.pid"
  /usr/bin/python3 - <<'CAGE_DESKTOP_DPI_READY'
import time
from Xlib import display
connection = display.Display()
selection = connection.intern_atom('_XSETTINGS_S0')
for attempt in range(60):
    if connection.get_selection_owner(selection):
        break
    time.sleep(.05)
else:
    raise RuntimeError('Desktop display settings unavailable')
connection.close()
CAGE_DESKTOP_DPI_READY
  scale_args=()
fi
/usr/local/bin/cage-desktop-files &
files_pid=$!
for _ in $(seq 1 100); do
  kill -0 "$files_pid"
  if [ -S "$XDG_RUNTIME_DIR/files.sock" ]; then break; fi
  sleep .1
done
test -S "$XDG_RUNTIME_DIR/files.sock"
export GTK_USE_PORTAL=1
# Openbox is not auto-detected as a libsecret desktop by Electron. Select the
# unlocked keyring explicitly so OAuth tokens survive application restarts.
# Desktop emits the active identity from its native account provider after
# the renderer confirms the login. Keep the filtered stream in the unit's
# private, ephemeral runtime directory; no callback or cookie is inspected.
history_args=()
if [[ -s "$XDG_RUNTIME_DIR/account-scope" ]]; then
  read -r _ _ code subject < "$XDG_RUNTIME_DIR/account-scope"
  if [[ $code =~ ^[a-f0-9]{24}$ ]]; then
    export CLAUDE_CONFIG_DIR="$HOME/.local/share/cage/claude-accounts/$code/config"
    mkdir -p "$CLAUDE_CONFIG_DIR"
    # Desktop's Code tab starts the CLI without Cage's launch flags. Link the
    # shared skills and instructions and add shared settings first; a conflict
    # keeps the original files, is logged by this unit and does not block Desktop.
    /usr/local/bin/cage-claude-account share "$code" >/dev/null || :
    if [[ $subject =~ ^[a-f0-9]{64}$ ]]; then
      history_args=("$CLAUDE_CONFIG_DIR" "$subject")
    fi
  fi
fi
lang_args=()
if [[ "${CAGE_LANGUAGE:-}" =~ ^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$ ]]; then
  lang_args=("--lang=${CAGE_LANGUAGE}")
fi
DESKTOP_LOG_LEVEL=debug CLAUDE_ENABLE_LOGGING=1 DEBUG='*current-account*' /usr/local/bin/cage-input-session /usr/bin/python3 -I /usr/local/bin/client_runtime.py claude-desktop "${scale_args[@]}" ${lang_args[@]+"${lang_args[@]}"} --password-store=gnome-libsecret --xdg-portal-required-version=3 > >(/usr/bin/python3 -I /usr/local/lib/desktop-identity.py "$XDG_RUNTIME_DIR/account.json" "${history_args[@]}") &
app_pid=$!
printf '%s\n' "$app_pid" > "$XDG_RUNTIME_DIR/app-pid"
# The service becomes ready only after the application has opened a window.
for _ in $(seq 1 200); do
  if ! kill -0 "$app_pid" 2>/dev/null; then
    break
  fi
  if xprop -root _NET_CLIENT_LIST 2>/dev/null | grep -q '0x[1-9a-f]'; then
    NOTIFY_SOCKET="$CAGE_NOTIFY_SOCKET" systemd-notify --ready
    break
  fi
  sleep .1
done
status=0
wait "$app_pid" || status=$?
if [ -f "$XDG_RUNTIME_DIR/stopping" ]; then
  rm -f "$XDG_RUNTIME_DIR/stopping"
  exit 0
fi
exit "$status"
