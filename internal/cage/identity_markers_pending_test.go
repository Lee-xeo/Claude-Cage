package cage

// Markers still waiting for their rename. Each later change removes the
// entries it resolves; the list must end up empty.
var identityMarkerPending = map[string]bool{
	".config/claude-cage":                      true,
	".config/claude-cage/browser-password":     true,
	".config/claude-cage/code-server.yaml":     true,
	".config/claude-cage/desktop-install.lock": true,
	".config/claude-cage/desktop-password":     true,
	".config/claude-cage/editor-workspace":     true,
	"/etc/claude-cage":                         true,
	"/etc/claude-cage/audio.pa":                true,
	"/etc/claude-cage/browser.env":             true,
	"/etc/claude-cage/browser-control.json":    true,
	"/etc/claude-cage/components.json":         true,
	"/etc/claude-cage/debian-mirror":           true,
	"/etc/claude-cage/desktop-downloads.json":  true,
	"/etc/claude-cage/desktop-openbox.xml":     true,
	"/etc/claude-cage/display-scale":           true,
	"/etc/claude-cage/editor-theme.json":       true,
	"/etc/claude-cage/environment":             true,
	"/etc/claude-cage/login-browser-mode":      true,
	"/etc/claude-cage/openbox.xml":             true,
	"/etc/profile.d/claude-cage.sh":            true,
}
