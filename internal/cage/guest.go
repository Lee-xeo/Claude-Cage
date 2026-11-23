package cage

import (
	"al.essio.dev/pkg/shellescape"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/version"
)

//go:embed guest-setup.sh
var guestDependencies string

//go:embed tailnet-client.mjs
var tailnetClient string

func tailnetClientScript() string {
	return "sudo tee /usr/local/lib/tailnet-client.mjs >/dev/null <<'CAGE_TAILNET_CLIENT'\n" + tailnetClient + "\nCAGE_TAILNET_CLIENT\n"
}

//go:embed guest-services.sh
var guestServices string

//go:embed guest-wait-services.py
var guestWaitServices string

//go:embed claude-account.py
var claudeAccountHelper string

//go:embed client_runtime.py
var ClientRuntimeHelper string

func clientRuntimeScript() string {
	return "sudo tee /usr/local/bin/client_runtime.py >/dev/null <<'CLIENT_RUNTIME'\n" + ClientRuntimeHelper + "\nCLIENT_RUNTIME\nsudo chmod 0644 /usr/local/bin/client_runtime.py\n"
}

func claudeAccountScript() string {
	return clientRuntimeScript() +
		"sudo tee /usr/local/bin/cage-claude-account >/dev/null <<'CAGE_CLAUDE_ACCOUNT'\n" + claudeAccountHelper + "\nCAGE_CLAUDE_ACCOUNT\nsudo chmod 0755 /usr/local/bin/cage-claude-account\n" +
		`sudo install -d -m 0755 /usr/local/lib/cage-claude-bin
sudo tee /usr/local/lib/cage-claude-bin/claude >/dev/null <<'CAGE_CLAUDE_SHIM'
#!/bin/sh
exec /usr/local/bin/cage-claude-account run "$CAGE_CLAUDE_ACCOUNT_ID" "$@"
CAGE_CLAUDE_SHIM
sudo chmod 0755 /usr/local/lib/cage-claude-bin/claude
`
}

//go:embed guest-audio.sh
var guestAudio string

//go:embed guest-audio-input.py
var guestAudioInput string

func guestAudioInputScript() string {
	return "sudo tee /usr/local/bin/cage-audio-input >/dev/null <<'CAGE_AUDIO_INPUT'\n" + guestAudioInput + "\nCAGE_AUDIO_INPUT\nsudo chmod 0755 /usr/local/bin/cage-audio-input\n"
}

//go:embed guest-memory.sh
var guestMemory string

//go:embed novnc-patch.py
var novncPatch string

//go:embed clipboard.js
var clipboardJS string

//go:embed browser-vault.mjs
var browserVault string

//go:embed browser-agent.mjs
var browserAgent string

//go:embed browser-visibility.mjs
var browserVisibility string

//go:embed browser-wheel.mjs
var browserWheel string

//go:embed display-density.mjs
var displayDensity string

//go:embed display-governor.py
var displayGovernor string

func displayGovernorScript() string {
	return "sudo tee /usr/local/bin/cage-display-governor >/dev/null <<'CAGE_DISPLAY_GOVERNOR'\n" + displayGovernor + "\nCAGE_DISPLAY_GOVERNOR\nsudo chmod 0755 /usr/local/bin/cage-display-governor\n"
}

//go:embed browser-phone.mjs
var phoneBrowser string

//go:embed browser-phone-network.mjs
var phoneBrowserNetwork string

//go:embed browser-private.mjs
var browserPrivate string

//go:embed browser-cookies.mjs
var browserCookies string

//go:embed browser-extensions.mjs
var browserExtensions string

//go:embed browser-history.mjs
var browserHistory string

//go:embed browser-extension-archive.py
var browserExtensionArchive string

//go:embed browser-clipboard.mjs
var browserClipboard string

//go:embed split-extension/manifest.json
var splitManifest string

//go:embed split-extension/control.html
var splitPage string

//go:embed split-extension/settings.html
var browserSettingsPage string

//go:embed split-extension/control.js
var splitControl string

func splitExtensionScript() string {
	return "split_directory=/usr/local/lib/cage-split\nsudo install -d -m 0755 \"$split_directory\"\n" +
		"sudo tee \"$split_directory\"/manifest.json >/dev/null <<'CAGE_MANIFEST'\n" + splitManifest + "\nCAGE_MANIFEST\n" +
		"sudo tee \"$split_directory\"/control.html >/dev/null <<'CAGE_HTML'\n" + splitPage + "\nCAGE_HTML\n" +
		"sudo tee \"$split_directory\"/settings.html >/dev/null <<'CAGE_SETTINGS_HTML'\n" + browserSettingsPage + "\nCAGE_SETTINGS_HTML\n" +
		"sudo tee \"$split_directory\"/control.js >/dev/null <<'CAGE_CONTROL'\n" + splitControl + "\nCAGE_CONTROL\n"
}

func browserClipboardScript() string {
	return sharedClipboardScript() + "sudo tee /usr/local/lib/browser-clipboard.mjs >/dev/null <<'CAGE_BROWSER_CLIPBOARD'\n" + browserClipboard + "\nCAGE_BROWSER_CLIPBOARD\n"
}

//go:embed browser-audit.mjs
var browserAudit string

//go:embed device-observation.mjs
var deviceObservation string

func deviceObservationScript() string {
	return "sudo tee /usr/local/lib/device-observation.mjs >/dev/null <<'CAGE_DEVICE_OBSERVATION'\n" + deviceObservation + "\nCAGE_DEVICE_OBSERVATION\n"
}

//go:embed browser-downloads.mjs
var browserDownloads string

//go:embed browser-uploads.mjs
var browserUploads string

//go:embed editor-migrate.py
var editorMigrate string

//go:embed save-download.py
var saveDownload string

//go:embed image-input.py
var imageInput string

//go:embed desktop-files.py
var desktopFiles string

//go:embed desktop-downloads.py
var desktopDownloads string

//go:embed native-ime.py
var nativeIME string

//go:embed native-ime-session.sh
var nativeIMESession string

//go:embed native-input.mjs
var nativeInputClient string

func nativeInputScript() string {
	return "\nsudo tee /usr/local/bin/cage-native-input >/dev/null <<'CAGE_NATIVE_INPUT'\n" + nativeIME + "\nCAGE_NATIVE_INPUT\nsudo chmod 0755 /usr/local/bin/cage-native-input\n" +
		"sudo tee /usr/local/bin/cage-input-session >/dev/null <<'CAGE_INPUT_SESSION'\n" + nativeIMESession + "\nCAGE_INPUT_SESSION\nsudo chmod 0755 /usr/local/bin/cage-input-session\n" +
		"sudo tee /usr/local/lib/native-input.mjs >/dev/null <<'CAGE_INPUT_CLIENT'\n" + nativeInputClient + "\nCAGE_INPUT_CLIENT\n" +
		"printf 'cage-browser ALL=(cage) NOPASSWD: /usr/local/bin/cage-native-input --request, /usr/local/bin/cage-native-input --relay\\n' | sudo tee /etc/sudoers.d/cage-input >/dev/null\nsudo chmod 0440 /etc/sudoers.d/cage-input\n"
}

func fileInputScript() string {
	return "\nsudo tee /usr/local/bin/cage-desktop-files >/dev/null <<'CAGE_DESKTOP_FILES'\n" + desktopFiles + "\nCAGE_DESKTOP_FILES\nsudo chmod 0755 /usr/local/bin/cage-desktop-files\n" +
		"sudo tee /usr/local/lib/cage-desktop-downloads.py >/dev/null <<'CAGE_DESKTOP_DOWNLOADS'\n" + desktopDownloads + "\nCAGE_DESKTOP_DOWNLOADS\n" +
		"sudo tee /usr/local/bin/cage-save-download >/dev/null <<'CAGE_DESKTOP_DOWNLOAD_COPY'\n" + saveDownload + "\nCAGE_DESKTOP_DOWNLOAD_COPY\nsudo chmod 0755 /usr/local/bin/cage-save-download\n"
}

func imageInputScript() string {
	return nativeInputScript() + fileInputScript() + "\nsudo tee /usr/local/bin/cage-image-input >/dev/null <<'CAGE_IMAGE_INPUT'\n" + imageInput + "\nCAGE_IMAGE_INPUT\nsudo chmod 0755 /usr/local/bin/cage-image-input\n"
}

func phoneBrowserScript() string {
	return "\nsudo tee /usr/local/lib/browser-phone.mjs >/dev/null <<'CAGE_PHONE_BROWSER'\n" + phoneBrowser + "\nCAGE_PHONE_BROWSER\n" +
		"sudo tee /usr/local/lib/browser-phone-network.mjs >/dev/null <<'CAGE_PHONE_NETWORK'\n" + phoneBrowserNetwork + "\nCAGE_PHONE_NETWORK\n"
}

func browserServices() string {
	return deviceObservationScript() + "sudo tee /usr/local/lib/cage-wait-services.py >/dev/null <<'CAGE_WAIT_SERVICES'\n" + guestWaitServices + "\nCAGE_WAIT_SERVICES\n" + toolInstallFunctions() + "\n" + tailnetClientScript() + "sudo tee /usr/local/lib/browser-visibility.mjs >/dev/null <<'CAGE_BROWSER_VISIBILITY'\n" + browserVisibility + "\nCAGE_BROWSER_VISIBILITY\nsudo tee /usr/local/lib/browser-wheel.mjs >/dev/null <<'CAGE_BROWSER_WHEEL'\n" + browserWheel + "\nCAGE_BROWSER_WHEEL\nsudo tee /usr/local/lib/display-density.mjs >/dev/null <<'CAGE_DISPLAY_DENSITY'\n" + displayDensity + "\nCAGE_DISPLAY_DENSITY\n" + displayGovernorScript() + PhoneDesktopProvisionScript() + phoneBrowserScript() + "\nsudo tee /usr/local/lib/browser-extensions.mjs >/dev/null <<'CAGE_BROWSER_EXTENSIONS'\n" + browserExtensions + "\nCAGE_BROWSER_EXTENSIONS\n" + "\nsudo tee /usr/local/lib/browser-history.mjs >/dev/null <<'CAGE_BROWSER_HISTORY'\n" + browserHistory + "\nCAGE_BROWSER_HISTORY\n" + "\nsudo tee /usr/local/lib/browser-extension-archive.py >/dev/null <<'CAGE_EXTENSION_ARCHIVE'\n" + browserExtensionArchive + "\nCAGE_EXTENSION_ARCHIVE\n" + "\nsudo tee /usr/local/lib/browser-private.mjs >/dev/null <<'CAGE_BROWSER_PRIVATE'\n" + browserPrivate + "\nCAGE_BROWSER_PRIVATE\n" + "\nsudo tee /usr/local/lib/browser-cookies.mjs >/dev/null <<'CAGE_BROWSER_COOKIES'\n" + browserCookies + "\nCAGE_BROWSER_COOKIES\n" + "\nsudo tee /usr/local/lib/browser-vault.mjs >/dev/null <<'CAGE_BROWSER_VAULT'\n" + browserVault + "\nCAGE_BROWSER_VAULT\n" + guestMemory + "\n" + GuestDiskGrowScript + "\n" + guestDiagnostics() + guestAudioInputScript() + guestAudio + "\n" + guestOpenScript() + splitExtensionScript() + browserClipboardScript() + imageInputScript() + "\nsudo tee /usr/local/lib/browser-uploads.mjs >/dev/null <<'CAGE_BROWSER_UPLOADS'\n" + browserUploads + "\nCAGE_BROWSER_UPLOADS\n" + guestServices + "\nsudo tee /usr/local/lib/browser-downloads.mjs >/dev/null <<'CAGE_BROWSER_DOWNLOADS'\n" + browserDownloads + "\nCAGE_BROWSER_DOWNLOADS\n" + "\nsudo tee /usr/local/bin/cage-editor-migrate >/dev/null <<'CAGE_EDITOR_MIGRATE'\n" + editorMigrate + "\nCAGE_EDITOR_MIGRATE\nsudo chmod 0755 /usr/local/bin/cage-editor-migrate\n" + "\nsudo tee /usr/local/lib/browser-audit.mjs >/dev/null <<'CAGE_BROWSER_AUDIT'\n" + browserAudit + "\nCAGE_BROWSER_AUDIT\n" + "\nsudo tee /usr/local/lib/cage-browser-agent.mjs >/dev/null <<'CAGE_BROWSER_AGENT'\n" + browserAgent + "\nCAGE_BROWSER_AGENT\n" + "sudo tee /usr/local/bin/cage-save-download >/dev/null <<'CAGE_DOWNLOAD_HELPER'\n" + saveDownload + "\nCAGE_DOWNLOAD_HELPER\nsudo chmod 0755 /usr/local/bin/cage-save-download\nprintf 'cage-browser ALL=(root) NOPASSWD: /usr/local/bin/cage-save-download *\\n' | sudo tee /etc/sudoers.d/cage-download >/dev/null\nsudo chmod 0440 /etc/sudoers.d/cage-download\n" + "\nsudo python3 - \"$cage_novnc_root\" <<'CAGE_NOVNC_PATCH'\n" + novncPatch + "\nCAGE_NOVNC_PATCH\n" + "\nsudo tee \"$cage_novnc_root/cage-clipboard.js\" >/dev/null <<'CAGE_CLIPBOARD_JS'\n" + clipboardJS + "\nCAGE_CLIPBOARD_JS\n" + `sudo python3 - <<'PYCODE'
from pathlib import Path
import re
p = Path("/usr/share/novnc/vnc.html")
s = p.read_text()
tag = '<script type="module" src="cage-clipboard.js?cage=8"></script>'
pattern = r'<script type="module" src="cage-clipboard\.js(?:\?cage=\d+)?"></script>'
if re.search(pattern, s):
    s = re.sub(pattern, lambda _: tag, s)
else:
    s = s.replace("</body>", tag + "\n</body>")
p.write_text(s)
PYCODE
`
}

var GuestSetup = APTBootstrapScript + "\n" + guestDependencies + "\n" + browserServices()

//go:embed editor-theme.json
var editorTheme string

//go:embed guest-start.sh
var guestStart string

//go:embed guest-env.sh
var guestEnv string

//go:embed tool-versions.env
var toolVersions string

//go:embed tool-download.py
var toolDownload string

//go:embed guest-tools.sh
var guestTools string

//go:embed debian-mirrors.sh
var DebianMirrorsScript string

//go:embed apt-bootstrap.sh
var aptBootstrap string

// APTBootstrapScript preserves the temporary maintenance proxy through sudo.
var APTBootstrapScript = "set -e\nsudo env APT_CONFIG=\"${CAGE_APT_CONFIG:-/dev/null}\" sh -se <<'CAGE_APT_BOOTSTRAP'\n" + aptBootstrap + "\nCAGE_APT_BOOTSTRAP\n"

// DebianPrepareScript runs before Lima's own dependency installation as well
// as Cage setup. Its version check also covers previously created guests.
var DebianPrepareScript = DebianPrepareScriptFor("")

// DebianMirrorEnv carries the workbench's Debian source choice to the host
// cage CLI. It stays on the host: the guest receives the validated value as
// a non-exported shell variable of the preparation script. The cloud image
// build's CAGE_DEBIAN_MIRROR names a mirror URL instead, hence the distinct
// name.
const DebianMirrorEnv = "CAGE_DEBIAN_MIRROR_ORDER"

// InstallTLS12Env carries the user's explicit opt-in TLS 1.2 compatibility
// switch to the host cage CLI. It stays on the host; when set to "1", setup
// and tool-update subshells create a temporary CURL_HOME capped at TLS 1.2 so
// curl avoids proxy egresses that drop TLS 1.3 ClientHello packets. It defaults
// to off and never alters runtime browsing or API traffic.
const InstallTLS12Env = "CAGE_INSTALL_TLS12_COMPAT"

// InstallTLS12Prelude configures a temporary curl config capped at TLS 1.2 for
// the current install or tool-update shell when enabled is true. When false
// (the default), it emits nothing so TLS 1.3 stays active.
func InstallTLS12Prelude(enabled bool) string {
	if !enabled {
		return ""
	}
	return `_curl_compat_home="$(mktemp -d)"
printf 'tls-max = "1.2"\n' > "$_curl_compat_home/.curlrc"
export CURL_HOME="$_curl_compat_home"
trap 'rm -rf "$_curl_compat_home"' EXIT
`
}

// ValidDebianMirror reports whether preference names a Debian source policy:
// "auto" (official first), "official" (pinned), or "nju" / "ustc" (that
// domestic mirror first, the other next, the official source last).
func ValidDebianMirror(preference string) bool {
	switch preference {
	case "auto", "official", "nju", "ustc":
		return true
	}
	return false
}

func debianMirrorChoice(preference string) string {
	if !ValidDebianMirror(preference) {
		return ""
	}
	return "debian_mirror_choice=" + preference + "\n"
}

// DebianPrepareScriptFor pins the source policy for this run. An empty or
// unknown preference keeps the guest's saved choice or its default probe.
func DebianPrepareScriptFor(preference string) string {
	return "#!/bin/sh\n" + APTBootstrapScript + "\n" + debianMirrorChoice(preference) + DebianMirrorsScript
}

//go:embed chromium-download.py
var chromiumDownload string

func toolInstallFunctions() string {
	return toolVersions + `
cage_curl_download() {
  python3 - "$@" <<'CAGE_DOWNLOAD_IDLE'
` + toolDownload + `
CAGE_DOWNLOAD_IDLE
}
` + guestTools + `
cage_install_chromium() {
  cage_cache_chromium --no-install-recommends chromium chromium-l10n
  sudo env DEBIAN_FRONTEND=noninteractive APT_CONFIG="${CAGE_APT_CONFIG:-/dev/null}" apt-get install -y --no-install-recommends chromium chromium-l10n </dev/null
}
`
}

func ToolInstallPrelude() string {
	return toolInstallFunctions() + `
cage_cache_chromium() {
  local status=0
  sudo env APT_CONFIG="${CAGE_APT_CONFIG:-}" CAGE_SETUP_PROGRESS="${CAGE_SETUP_PROGRESS:-}" /usr/bin/python3 -I - "$@" <<'CAGE_CHROMIUM_DOWNLOAD' || status=$?
` + chromiumDownload + `
CAGE_CHROMIUM_DOWNLOAD
  if (( status >= 128 )); then return "$status"; fi
  if (( status != 0 )); then echo 'Chromium mirror prefetch unavailable; continuing with APT.' >&2; fi
}
`
}

func RequiredToolVersions() map[string]string {
	result := map[string]string{"chromium": "Debian security repository"}
	for _, line := range strings.Split(toolVersions, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "CAGE_CODE_SERVER_VERSION":
			result["code_server"] = value
		case "CAGE_MISE_VERSION":
			result["mise"] = value
		case "CAGE_GH_VERSION":
			result["gh"] = value
		}
	}
	result["claude"] = "官方最新版（下载时解析）"
	return result
}

func ToolsUpdateScript() string { return ToolsUpdateScriptFor(false) }

func ToolsUpdateScriptFor(tls12Compat bool) string {
	return "set -euo pipefail\nexport PATH=\"$HOME/.local/bin:$PATH\"\n" + InstallTLS12Prelude(tls12Compat) + `
sudo env DEBIAN_FRONTEND=noninteractive dpkg --configure -a </dev/null
` + APTBootstrapScript + ToolInstallPrelude() + `
sudo env DEBIAN_FRONTEND=noninteractive APT_CONFIG="${CAGE_APT_CONFIG:-/dev/null}" apt-get update </dev/null
cage_install_chromium
sudo env DEBIAN_FRONTEND=noninteractive APT_CONFIG="${CAGE_APT_CONFIG:-/dev/null}" apt-get install -y --no-install-recommends python3-xlib </dev/null || echo 'python3-xlib is unavailable; displays keep their full refresh rate.' >&2
sudo env DEBIAN_FRONTEND=noninteractive APT_CONFIG="${CAGE_APT_CONFIG:-/dev/null}" apt-get install -y --no-install-recommends xsettingsd </dev/null || echo 'xsettingsd is unavailable; displays keep their configured density.' >&2
cage_install_tool mise
cage_install_tool gh
cage_install_tool code-server
cage_install_tool claude
printf 'Cage tool packages are ready; applying browser and editor components.\n'
`
}

func (c Config) EnvironmentScript() string {
	return fmt.Sprintf("export CAGE_TZ=%s\nexport CAGE_LOCALE=%s\nexport CAGE_LANGUAGE=%s\n", shellescape.Quote(c.Timezone), shellescape.Quote(c.Locale), shellescape.Quote(c.BrowserLanguage)) + guestEnv
}

func (c Config) StartScript() string {
	body := c.EnvironmentScript() + "\n" + componentStartScript()
	return c.GuestProfile.Linux(markGuestStart(body))
}

func (c Config) SetupScript() string { return c.GuestProfile.Linux(GuestSetup) }

// clearAccountComment empties the work account's comment field. Environments
// created by earlier versions copied the host account's full name into it.
// Only the comment changes; the account, its groups and home stay as they are.
const clearAccountComment = `account_name=$(id -un)
if [ -n "$(getent passwd "$account_name" | cut -d: -f5)" ]; then sudo usermod -c '' "$account_name" || echo 'The account comment could not be cleared.' >&2; fi
unset account_name
`

func componentStartScript() string {
	return clearAccountComment + APTBootstrapScript + "\n" + claudeAccountScript() + browserServices() + "\nsudo tee /etc/claude-cage/editor-theme.json >/dev/null <<'CAGE_EDITOR_THEME'\n" + editorTheme + "\nCAGE_EDITOR_THEME\nif systemctl is-active --quiet cage-editor.service; then sudo systemctl stop cage-editor.service; fi\n" + guestStart
}

func markGuestStart(body string) string {
	manifest, _ := json.Marshal(RequiredGuestManifest())
	// Keep the last successful manifest, but invalidate it durably before any
	// component changes. An interrupted start must be replayed, never accepted.
	return `set -euo pipefail
sudo python3 - <<'CAGE_COMPONENT_UPDATE'
from pathlib import Path
import os
p=Path('/etc/claude-cage/components.updating')
p.parent.mkdir(parents=True,exist_ok=True)
with p.open('w') as f:
 f.flush();os.fsync(f.fileno())
fd=os.open(p.parent,os.O_RDONLY)
try: os.fsync(fd)
finally: os.close(fd)
CAGE_COMPONENT_UPDATE
(
` + body + `
)
sudo python3 - <<'CAGE_COMPONENT_MANIFEST'
from pathlib import Path
import os,tempfile
p=Path('/etc/claude-cage/components.json')
directory=os.open(p.parent,os.O_RDONLY)
name=None
try:
 fd,name=tempfile.mkstemp(prefix='.components-',dir=p.parent)
 with os.fdopen(fd,'w') as f:
  f.write(` + fmt.Sprintf("%q", string(manifest)) + `)
  os.chmod(name,0o644)
  f.flush();os.fsync(f.fileno())
 os.replace(name,p)
 os.fsync(directory)
 p.with_suffix('.updating').unlink()
 os.fsync(directory)
finally:
 if name is not None and os.path.exists(name): os.unlink(name)
 os.close(directory)
CAGE_COMPONENT_MANIFEST
`
}

type GuestManifest struct {
	Protocol   int    `json:"protocol"`
	Revision   string `json:"revision"`
	AppVersion string `json:"app_version"`
	Updating   bool   `json:"updating,omitempty"`
}

var guestRevision = sync.OnceValue(func() string {
	payload := strings.Join([]string{"cage-guest-protocol-1", guestDependencies, guestEnv, componentStartScript(), ToolInstallPrelude()}, "\x00")
	return fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
})

func RequiredGuestManifest() GuestManifest {
	return GuestManifest{Protocol: 1, Revision: guestRevision(), AppVersion: version.Version}
}

func (m GuestManifest) Current() bool {
	required := RequiredGuestManifest()
	return !m.Updating && m.Protocol == required.Protocol && m.Revision == required.Revision
}

func guestDiagnostics() string {
	return "sudo tee /usr/local/bin/cage-log-run >/dev/null <<'CAGE_LOG_RUN'\n" + diagnostics.GuestRunner() + "\nCAGE_LOG_RUN\nsudo chmod 0755 /usr/local/bin/cage-log-run\n" + `sudo install -d -m 0755 /etc/systemd/journald@cage.conf.d
sudo tee /etc/systemd/journald@cage.conf.d/limits.conf >/dev/null <<'CAGE_JOURNAL'
[Journal]
Storage=persistent
SystemMaxUse=10M
SystemMaxFileSize=1M
RuntimeMaxUse=10M
RuntimeMaxFileSize=1M
MaxRetentionSec=14day
MaxFileSec=1day
RateLimitIntervalSec=30s
RateLimitBurst=200
CAGE_JOURNAL
sudo systemctl try-restart systemd-journald@cage.service
`
}
