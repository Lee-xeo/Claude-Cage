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
		user := p.Username
		if m[1] == "cage-browser" {
			user = p.BrowserUsername
		}
		return "/home/" + user + m[2]
	})
	for _, rule := range linuxAccountRules {
		name := p.Username
		if rule.browser {
			name = p.BrowserUsername
		}
		script = rule.pattern.ReplaceAllString(script, rule.prefix+name+"${1}")
	}
	script = strings.NewReplacer("cage-browser ALL=", p.BrowserUsername+" ALL=", "cage ALL=", p.Username+" ALL=").Replace(script)
	// Clipboard messages carry stable roles, not operating-system usernames.
	roles := fmt.Sprintf("{'cage': '%s', 'cage-browser': '%s'}", p.Username, p.BrowserUsername)
	for _, v := range []string{"user", "sys.argv[2]"} {
		script = strings.ReplaceAll(script, "pwd.getpwnam("+v+")", "pwd.getpwnam("+roles+".get("+v+", "+v+"))")
	}
	script = strings.ReplaceAll(script, "cage_output", p.AudioName)
	script = strings.ReplaceAll(script, "sink_properties=device.description=Cage", `sink_properties="device.description='`+p.AudioDescription+`'"`)
	script = strings.ReplaceAll(script, "2880x1800", fmt.Sprintf("%dx%d", p.DisplayWidth, p.DisplayHeight))
	script = strings.ReplaceAll(script, "--window-size=1440,900", fmt.Sprintf("--window-size=%d,%d", p.BrowserWidth, p.BrowserHeight))
	manifestName, _ := json.Marshal(p.ExtensionName)
	script = strings.ReplaceAll(script, `"name": "Cage browser panes"`, `"name": `+string(manifestName))
	// The unpacked extension ID is derived from its path. Give each new
	// environment a saved path as well as a configurable display name.
	script = strings.NewReplacer(
		"/usr/local/lib/cage-split", "/usr/local/lib/browser-panes-"+p.ExtensionSuffix,
		"/var/snap/chromium/common/workbench-extension", "/var/snap/chromium/common/browser-panes-"+p.ExtensionSuffix,
	).Replace(script)
	flags := []string{}
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
