package cage

import _ "embed"

//go:embed claude-desktop-install.sh
var claudeDesktopInstall string

//go:embed claude-desktop-session.sh
var claudeDesktopSession string

//go:embed claude-desktop-open.sh
var claudeDesktopOpen string

//go:embed claude-desktop-start.sh
var claudeDesktopStart string

//go:embed guest-open.sh
var guestOpen string

//go:embed browser-login.mjs
var browserLogin string

//go:embed desktop-finish.sh
var desktopFinish string

//go:embed desktop-identity.py
var desktopIdentity string

//go:embed desktop-history.py
var desktopHistory string

//go:embed desktop-auth-browser.py
var desktopAuthBrowser string

func guestOpenScript() string {
	return "sudo tee /usr/local/lib/desktop-finish.sh >/dev/null <<'DESKTOP_FINISH'\n" + desktopFinish + "\nDESKTOP_FINISH\n" +
		"sudo tee /usr/local/lib/desktop-identity.py >/dev/null <<'DESKTOP_IDENTITY'\n" + desktopIdentity + "\nDESKTOP_IDENTITY\n" +
		"sudo tee /usr/local/lib/desktop-auth-browser.py >/dev/null <<'DESKTOP_AUTH_BROWSER'\n" + desktopAuthBrowser + "\nDESKTOP_AUTH_BROWSER\n" +
		"sudo tee /usr/local/lib/browser-login.mjs >/dev/null <<'CAGE_BROWSER_LOGIN'\n" + browserLogin + "\nCAGE_BROWSER_LOGIN\n" +
		"sudo tee /usr/local/bin/.xdg-open.new >/dev/null <<'OPEN_URL'\n" + guestOpen + "\nOPEN_URL\n" +
		// Replace the file in one step: earlier versions installed a link here.
		"sudo chmod 0755 /usr/local/bin/.xdg-open.new\nsudo mv -f /usr/local/bin/.xdg-open.new /usr/local/bin/xdg-open\n" + `
sudo install -d -m 0755 /usr/local/share/applications
printf '%s\n' '[Desktop Entry]' 'Type=Application' 'Name=Browser' 'NoDisplay=true' 'Exec=/usr/local/bin/xdg-open %u' 'MimeType=x-scheme-handler/http;x-scheme-handler/https;' | sudo tee /usr/local/share/applications/workbench-web.desktop >/dev/null
sudo -H -u cage xdg-mime default workbench-web.desktop x-scheme-handler/http x-scheme-handler/https
`
}

func ClaudeDesktopInstallScript() string {
	return ToolInstallPrelude() + claudeDesktopInstall + "\n" + ClaudeDesktopProvisionScript()
}

// Provisioning is independent of the browser and never restarts it. Desktop
// services are deliberately not enabled at boot: the user opens the app.
func ClaudeDesktopProvisionScript() string {
	return "set -euo pipefail\n" + clientRuntimeScript() + sharedClipboardScript() + nativeInputScript() + fileInputScript() + displayGovernorScript() +
		"sudo install -d -m 0755 /etc/claude-cage\n" +
		"sudo tee /usr/local/lib/desktop-history.py >/dev/null <<'DESKTOP_HISTORY'\n" + desktopHistory + "\nDESKTOP_HISTORY\n" +
		"sudo tee /usr/local/libexec/claude-desktop-session >/dev/null <<'CAGE_CLAUDE_SESSION'\n" + claudeDesktopSession + "\nCAGE_CLAUDE_SESSION\n" +
		"sudo tee /usr/local/libexec/claude-desktop-open >/dev/null <<'CAGE_CLAUDE_OPEN'\n" + claudeDesktopOpen + "\nCAGE_CLAUDE_OPEN\n" +
		"sudo tee /usr/local/libexec/claude-desktop-start >/dev/null <<'CAGE_CLAUDE_START'\n" + claudeDesktopStart + "\nCAGE_CLAUDE_START\n" +
		"sudo chmod 0755 /usr/local/libexec/claude-desktop-session /usr/local/libexec/claude-desktop-open /usr/local/libexec/claude-desktop-start\n" + guestOpenScript() + `
printf 'cage-browser ALL=(cage) NOPASSWD: /usr/local/libexec/claude-desktop-open *\n' | sudo tee /etc/sudoers.d/claude-desktop-open >/dev/null
sudo chmod 0440 /etc/sudoers.d/claude-desktop-open
sudo python3 - <<'CAGE_DESKTOP_DOWNLOAD_CONFIG'
import json, os
from pathlib import Path
source = Path('/etc/claude-cage/browser-control.json')
if source.exists():
    with source.open() as config:
        workspace = json.load(config)['workspace']
    destination = Path('/etc/claude-cage/desktop-downloads.json')
    destination.write_text(json.dumps({'workspace': workspace}))
    os.chmod(destination, 0o644)
CAGE_DESKTOP_DOWNLOAD_CONFIG
sudo tee /etc/claude-cage/desktop-openbox.xml >/dev/null <<'CAGE_CLAUDE_WINDOWS'
<?xml version="1.0" encoding="UTF-8"?>
<openbox_config xmlns="http://openbox.org/3.4/rc">
<focus><focusNew>yes</focusNew><followMouse>no</followMouse></focus>
<desktops><number>1</number></desktops>
<applications><application type="normal"><decor>no</decor><maximized>yes</maximized></application></applications>
</openbox_config>
CAGE_CLAUDE_WINDOWS
# A Desktop opened before the environment's components were updated keeps
# running under its former unit, which this name refers to until it closes.
unit=/etc/systemd/system/desktop-session.service
if ! { [ -L "$unit" ] && systemctl is-active --quiet desktop-session.service; }; then
  if [ -L /run/desktop-session ]; then sudo rm -f /run/desktop-session; fi
  sudo tee /etc/systemd/system/.desktop-session.tmp >/dev/null <<'DESKTOP_UNIT'
[Unit]
Description=Claude Desktop session
[Service]
Type=notify
NotifyAccess=all
User=cage
WorkingDirectory=/home/cage
Environment=HOME=/home/cage
EnvironmentFile=/etc/claude-cage/environment
RuntimeDirectory=desktop-session
RuntimeDirectoryMode=0700
ExecStart=/usr/local/libexec/service-log /usr/local/libexec/claude-desktop-session
ExecStop=/bin/bash /usr/local/lib/desktop-finish.sh /run/desktop-session desktop-session.service
LogNamespace=services
KillMode=control-group
TimeoutStartSec=45
TimeoutStopSec=20
DESKTOP_UNIT
  sudo mv -f /etc/systemd/system/.desktop-session.tmp "$unit"
fi
sudo systemctl daemon-reload
`
}
