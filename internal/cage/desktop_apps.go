package cage

import _ "embed"

//go:embed desktop-app-open.sh
var desktopAppOpen string

// DesktopAppDependencies are the Debian packages every plugin desktop
// application's session needs: its display, window manager, keyring, input
// method, file chooser and clipboard. The vendor's package brings its own.
const DesktopAppDependencies = "ca-certificates curl gnupg dbus-x11 xauth x11-utils x11-xserver-utils xdg-utils tigervnc-standalone-server tigervnc-tools openbox gnome-keyring gcr bubblewrap socat openssh-client libglib2.0-bin ibus ibus-gtk3 gir1.2-ibus-1.0 gir1.2-gtk-3.0 python3-gi python3-pil python3-xlib xsettingsd util-linux"

// DesktopAppSupportScript installs the helpers a plugin desktop application's
// session shares with Claude Desktop, and the handler that gives sign-in links
// back to the application. It installs no service: the user opens the app.
func DesktopAppSupportScript() string {
	return "set -euo pipefail\n" + clientRuntimeScript() + sharedClipboardScript() + nativeInputScript() + DesktopFileSupportScript() + guestOpenScript() +
		"sudo tee /usr/local/bin/desktop-app-open >/dev/null <<'DESKTOP_APP_OPEN'\n" + desktopAppOpen + "\nDESKTOP_APP_OPEN\nsudo chmod 0755 /usr/local/bin/desktop-app-open\n" +
		"sudo install -d -m 0755 /usr/local/lib/desktop-apps\n" +
		"printf 'cage-browser ALL=(cage) NOPASSWD: /usr/local/bin/desktop-app-open --stdin\\n' | sudo tee /etc/sudoers.d/desktop-apps >/dev/null\nsudo chmod 0440 /etc/sudoers.d/desktop-apps\n"
}
