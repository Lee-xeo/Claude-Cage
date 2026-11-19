# Publish browser assets while the web endpoint is stopped, so clients cannot
# mix old and new noVNC modules during an upgrade.
if systemctl is-active --quiet browser-web-viewer; then
  sudo systemctl stop browser-web-viewer
fi
if ! command -v ip >/dev/null || ! test -x /usr/sbin/iptables || ! command -v Xtigervnc >/dev/null || ! command -v openbox >/dev/null || ! command -v xrandr >/dev/null || ! command -v xdotool >/dev/null || ! command -v ibus-daemon >/dev/null || ! /usr/bin/python3 -c 'import gi; gi.require_version("IBus", "1.0"); gi.require_version("Gtk", "3.0")' 2>/dev/null; then
  sudo env DEBIAN_FRONTEND=noninteractive APT_CONFIG="${CAGE_APT_CONFIG:-/dev/null}" apt-get install -y --no-install-recommends tigervnc-standalone-server tigervnc-tools openbox x11-xserver-utils xdotool libgtk-3-0 ibus ibus-gtk3 gir1.2-ibus-1.0 gir1.2-gtk-3.0 python3-gi python3-xlib xsettingsd iproute2 iptables
fi
sudo install -d -m 0755 /usr/local/share
cage_novnc_root=/usr/share/novnc
sudo install -d -m 0755 /etc/workbench /usr/local/lib
# New website passwords belong to the host vault. Keep existing browser data.
cage_browser_policy_dir=/etc/chromium/policies/managed
sudo install -d -m 0755 "$cage_browser_policy_dir"
printf '%s\n' '{"PasswordManagerEnabled":false}' | sudo tee "$cage_browser_policy_dir/cage-password-vault.json" >/dev/null
sudo chmod 0644 "$cage_browser_policy_dir/cage-password-vault.json"
# Resolve Tailscale application links in the workbench browser's user session.
if ! command -v xdg-mime >/dev/null; then
  sudo env DEBIAN_FRONTEND=noninteractive APT_CONFIG="${CAGE_APT_CONFIG:-/dev/null}" apt-get install -y --no-install-recommends xdg-utils </dev/null
fi
sudo install -d -m 0755 /usr/local/share/applications
printf '%s\n' '[Desktop Entry]' 'Type=Application' 'Name=Tailscale' 'NoDisplay=true' 'Exec=/usr/local/bin/xdg-open %u' 'MimeType=x-scheme-handler/tailscale;' | sudo tee /usr/local/share/applications/tailnet-login.desktop >/dev/null
sudo -H -u cage-browser sh -ec 'mkdir -p "${XDG_CONFIG_HOME:-$HOME/.config}"'
sudo -H -u cage-browser xdg-mime default tailnet-login.desktop x-scheme-handler/tailscale
sudo tee /usr/local/libexec/editor-start >/dev/null <<'SCRIPT'
#!/bin/sh
set -eu
workspace=$(cat /home/cage/.config/workbench/editor-workspace)
exec /usr/bin/code-server --bind-addr 127.0.0.1:18080 --config /home/cage/.config/workbench/code-server.yaml --disable-telemetry --disable-update-check --disable-workspace-trust --disable-proxy "$workspace"
SCRIPT
sudo chmod 0755 /usr/local/libexec/editor-start
sudo tee /etc/systemd/system/code-editor.service >/dev/null <<'UNIT'
[Unit]
Description=Code editor
[Service]
User=cage
WorkingDirectory=/home/cage
Environment=HOME=/home/cage
EnvironmentFile=/etc/workbench/environment
ExecStart=/usr/local/libexec/service-log /usr/local/libexec/editor-start
LogNamespace=services
Restart=on-failure
RestartSec=2
TimeoutStopSec=15
UNIT
printf 'cage-browser ALL=(root) NOPASSWD: /usr/bin/systemctl restart code-editor.service\n' | sudo tee /etc/sudoers.d/editor-restart >/dev/null
sudo chmod 0440 /etc/sudoers.d/editor-restart
sudo tee /etc/profile.d/workbench.sh >/dev/null <<'SCRIPT'
if [ -f /etc/workbench/environment ]; then
  set -a
  . /etc/workbench/environment
  set +a
fi
unset HTTP_PROXY HTTPS_PROXY ALL_PROXY FTP_PROXY NO_PROXY http_proxy https_proxy all_proxy ftp_proxy no_proxy
export BROWSER=/usr/local/bin/xdg-open
SCRIPT
if ! grep -q '/etc/profile.d/workbench.sh' /etc/zsh/zshenv; then
  echo '. /etc/profile.d/workbench.sh' | sudo tee -a /etc/zsh/zshenv >/dev/null
fi
sudo tee /etc/systemd/system/browser-display.service >/dev/null <<'UNIT'
[Unit]
Description=Virtual browser display
Wants=display-governor.service
[Service]
User=cage-browser
ExecStartPre=/usr/bin/rm -f /home/cage-browser/.vnc/display.sock
ExecStart=/usr/local/libexec/service-log /usr/bin/Xtigervnc :77 -geometry 2880x1800 -depth 24 -rfbport 15900 -localhost -nolisten tcp -SecurityTypes VncAuth -PasswordFile /home/cage-browser/.vnc/passwd -AlwaysShared
LogNamespace=services
Restart=on-failure
UNIT
sudo tee /etc/workbench/openbox.xml >/dev/null <<'WINDOW_CONFIG'
<?xml version="1.0" encoding="UTF-8"?>
<openbox_config xmlns="http://openbox.org/3.4/rc">
<focus><focusNew>yes</focusNew><followMouse>no</followMouse></focus>
<desktops><number>1</number></desktops>
<keyboard></keyboard><mouse></mouse>
<applications><application type="normal"><decor>no</decor><maximized>yes</maximized></application></applications>
</openbox_config>
WINDOW_CONFIG
sudo tee /etc/systemd/system/browser-window-manager.service >/dev/null <<'UNIT'
[Unit]
Description=Browser window geometry and focus (no desktop shell)
Requires=browser-display.service
After=browser-display.service
[Service]
User=cage-browser
Environment=DISPLAY=:77
ExecStart=/usr/local/libexec/service-log /usr/bin/openbox --config-file /etc/workbench/openbox.xml
LogNamespace=services
Restart=on-failure
UNIT
sudo tee /etc/systemd/system/browser-session.service >/dev/null <<'UNIT'
[Unit]
Description=Persistent browser
Requires=browser-display.service browser-window-manager.service
After=browser-display.service browser-window-manager.service
[Service]
User=cage-browser
Environment=DISPLAY=:77
EnvironmentFile=/etc/workbench/environment
EnvironmentFile=-/etc/workbench/browser.env
ExecStart=/usr/local/libexec/service-log /usr/local/libexec/input-session /usr/bin/node /usr/local/lib/browser-agent.mjs
LogNamespace=services
# Let the controller close Chromium and flush its tabs and profile before
# systemd terminates any remaining child processes.
KillMode=mixed
Restart=on-failure
RestartSec=2
UNIT
# Starts, restarts and stops with the display it paces. The display only
# wants it: a guest without python3-xlib keeps its full refresh rate.
sudo tee /etc/systemd/system/display-governor.service >/dev/null <<'UNIT'
[Unit]
Description=Browser display pacing
PartOf=browser-display.service
After=browser-display.service
[Service]
User=cage-browser
Environment=DISPLAY=:77
# The browser controller reports here the scrolling it delivers to a page.
Environment=DISPLAY_ACTIVITY_SOCKET=/home/cage-browser/.cache/display/activity.sock
ExecStart=/usr/local/libexec/service-log /usr/local/libexec/display-governor
LogNamespace=services
Restart=on-failure
RestartSec=5
UNIT
sudo tee /etc/systemd/system/browser-web-viewer.service >/dev/null <<'UNIT'
[Unit]
Description=Browser web access
Requires=browser-display.service
After=browser-display.service
[Service]
User=cage-browser
ExecStart=/usr/local/libexec/service-log /usr/bin/websockify --web=/usr/share/novnc 127.0.0.1:16080 127.0.0.1:15900
LogNamespace=services
Restart=on-failure
UNIT
sudo systemctl daemon-reload

if ! grep -q 'WORKBENCH_TERMINAL_PROMPT' /home/cage/.bashrc; then
  sudo tee -a /home/cage/.bashrc >/dev/null <<'PROMPT'
# WORKBENCH_TERMINAL_PROMPT
if [ "${WORKBENCH_TERMINAL:-}" = 1 ]; then
  PS1='\[\e[32m\]\u\[\e[0m\]:\[\e[34m\]\W\[\e[0m\]\$ '
fi
PROMPT
fi
