package guestprofile

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Linux renders Cage-owned provisioning/control templates, never user commands.
// Service names and clipboard role names deliberately stay stable: only OS
// accounts, home directories and the chosen public device preferences change.
func (p *Profile) Linux(script string) string {
	if p == nil {
		return script
	}
	script = linuxHomePaths.ReplaceAllStringFunc(script, func(path string) string {
		m := linuxHomePaths.FindStringSubmatch(path)
		home := p.WorkHomePath()
		if m[1] == "cage-browser" {
			home = p.BrowserHomePath()
		}
		return home + m[2]
	})
	for _, rule := range linuxAccountRules {
		name := p.Username
		if rule.browser {
			name = p.BrowserUsername
		}
		script = rule.pattern.ReplaceAllString(script, rule.prefix+name+"${1}")
	}
	script = strings.NewReplacer("cage-browser ALL=", p.BrowserUsername+" ALL=", "cage ALL=", p.Username+" ALL=").Replace(script)
	script = strings.ReplaceAll(script, "useradd --create-home --shell /bin/bash "+p.BrowserUsername, "useradd --create-home --home-dir "+p.BrowserHomePath()+" --shell /bin/bash "+p.BrowserUsername)
	// Clipboard messages carry stable roles, not operating-system usernames.
	roles := fmt.Sprintf("{'cage': '%s', 'cage-browser': '%s'}", p.Username, p.BrowserUsername)
	for _, v := range []string{"user", "sys.argv[2]"} {
		script = strings.ReplaceAll(script, "pwd.getpwnam("+v+")", "pwd.getpwnam("+roles+".get("+v+", "+v+"))")
	}
	script = strings.ReplaceAll(script, "cage_output", p.AudioName)
	script = strings.ReplaceAll(script, "sink_properties=device.description=Cage", `sink_properties="device.description='`+p.AudioDescription+`'"`)
	script = strings.ReplaceAll(script, "2880x1800", fmt.Sprintf("%dx%d", p.DisplayWidth, p.DisplayHeight))
	script = strings.ReplaceAll(script, "--window-size=1440,900", fmt.Sprintf("--window-size=%d,%d", p.BrowserWidth, p.BrowserHeight))
	if p.GPUBackend == "virgl" {
		script = strings.ReplaceAll(script, "/usr/bin/Xtigervnc :77 -geometry ", "/usr/bin/Xtigervnc :77 -rendernode /dev/dri/renderD128 -geometry ")
		script = strings.ReplaceAll(script, `Xtigervnc "$DISPLAY" -geometry `, `Xtigervnc "$DISPLAY" -rendernode /dev/dri/renderD128 -geometry `)
		// Dependencies and both accounts must exist before checking DRI3.
		marker := "# New website passwords belong to the host vault. Keep existing browser data."
		if !strings.Contains(script, "# CAGE_VIRGL_SETUP") {
			script = strings.Replace(script, marker, p.virGLSetup()+"\n"+marker, 1)
		}
	}
	manifestName, _ := json.Marshal(p.ExtensionName)
	script = strings.ReplaceAll(script, `"name": "Cage browser panes"`, `"name": `+string(manifestName))
	// The unpacked extension ID is derived from its path. Give each new
	// environment a saved path as well as a configurable display name.
	script = strings.NewReplacer(
		"/usr/local/lib/cage-split", "/usr/local/lib/browser-panes-"+p.ExtensionSuffix,
		"/var/snap/chromium/common/workbench-extension", "/var/snap/chromium/common/browser-panes-"+p.ExtensionSuffix,
	).Replace(script)
	flags := p.GraphicsFlags()
	if p.WebGL == "disabled" {
		flags = append(flags, "--disable-3d-apis", "--disable-webgl")
	}
	if p.Canvas == "blocked" {
		flags = append(flags, "--disable-reading-from-canvas")
	}
	for _, flag := range flags {
		if strings.Contains(script, fmt.Sprintf("%q", flag)) {
			continue
		}
		script = strings.ReplaceAll(script, `"--no-first-run",`, `"--no-first-run", `+fmt.Sprintf("%q", flag)+",")
	}
	return script
}

func (p *Profile) virGLSetup() string {
	return `# CAGE_VIRGL_SETUP
cage_vnc_help=$(/usr/bin/Xtigervnc -help 2>&1 || true)
if ! printf '%s' "$cage_vnc_help" | grep -qi rendernode; then
  echo 'This guest needs TigerVNC with DRI3 rendernode support; select a newer Linux image.' >&2
  exit 1
fi
test -c /dev/dri/renderD128 || { echo 'VirGL render node is unavailable in this guest.' >&2; exit 1; }
sudo env DEBIAN_FRONTEND=noninteractive APT_CONFIG="${CAGE_APT_CONFIG:-/dev/null}" apt-get install -y --no-install-recommends libgl1-mesa-dri libegl1 mesa-vulkan-drivers
cage_render_group=$(stat -c %G /dev/dri/renderD128)
getent group "$cage_render_group" >/dev/null
sudo usermod -a -G "$cage_render_group" ` + p.Username + `
sudo usermod -a -G "$cage_render_group" ` + p.BrowserUsername + `
sudo -u ` + p.BrowserUsername + ` test -r /dev/dri/renderD128
sudo -u ` + p.BrowserUsername + ` test -w /dev/dri/renderD128
`
}

var linuxHomePaths = regexp.MustCompile(`/home/(cage-browser|cage)([/\s"']|$)`)

type accountRule struct {
	pattern *regexp.Regexp
	prefix  string
	browser bool
}

var linuxAccountRules = func() []accountRule {
	var rules []accountRule
	for _, old := range []string{"cage-browser", "cage"} {
		for _, prefix := range []string{"User=", "Group=", "-u ", "-o ", "-g ", "id ", "id -u ", "enable-linger ", "--shell /bin/bash ", "-G cage-audio ", "ALL=(", "getpwnam('", "getgrnam('", `getpwnam("`, `getgrnam("`, "= "} {
			rules = append(rules, accountRule{regexp.MustCompile(regexp.QuoteMeta(prefix+old) + `([^A-Za-z0-9_-]|$)`), prefix, old == "cage-browser"})
		}
	}
	return rules
}()

func (p *Profile) WorkUser() string {
	if p == nil {
		return "cage"
	}
	return p.Username
}
func (p *Profile) BrowserUser() string {
	if p == nil {
		return "cage-browser"
	}
	return p.BrowserUsername
}
