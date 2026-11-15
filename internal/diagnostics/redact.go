// Package diagnostics owns Cage's local, bounded diagnostic record. Never pass
// terminal traffic, user files, request bodies or credential objects to it.
package diagnostics

import (
	"regexp"
	"strings"
)

const ipv4Address = `(?:[0-9]{1,3}\.){3}[0-9]{1,3}`

// Require either eight groups, an embedded IPv4 tail, or "::". Matching any
// colon-separated hex would also erase timestamps and their diagnostic value.
const ipv6Address = `(?:(?:[0-9a-f]{1,4}:){7}[0-9a-f]{1,4}|(?:[0-9a-f]{1,4}:){6}` + ipv4Address + `|(?:[0-9a-f]{1,4}:){0,6}[0-9a-f]{0,4}::[0-9a-f:.]*)`

var patterns = []struct {
	re          *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`), ""},
	{regexp.MustCompile(`(?im)^(\s*(?:cookie|set-cookie|authorization|proxy-authorization)\s*[:=]).*$`), "${1} [redacted]"},
	{regexp.MustCompile(`(?is)-----BEGIN [^-]*(?:PRIVATE KEY|CERTIFICATE)-----.*?(?:-----END [^-]+-----|$)`), "[redacted key]"},
	{regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s<>"'` + "`" + `]+`), "[url]"},
	{regexp.MustCompile(`(?i)\b(?:Bearer|Basic)\s+[a-z0-9._~+/=-]+`), "[redacted authorization]"},
	{regexp.MustCompile(`(?i)(["']?(?:password|passwd|pwd|token|access[_-]?token|refresh[_-]?token|id[_-]?token|api[_-]?key|apikey|secret|client[_-]?secret|authorization|proxy-authorization|cookie|set-cookie|auth|credential|subscription[_-]?url)["']?\s*[:=]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;}]+)`), "${1}[redacted]"},
	{regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{8,}|gh[pousr]_[A-Za-z0-9_]{12,}|github_pat_[A-Za-z0-9_]+|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)\b`), "[redacted token]"},
	// Imported proxy names are replaced with 24-character node IDs at runtime.
	{regexp.MustCompile(`\b(?:[a-fA-F0-9]{24,}|[a-fA-F0-9]{8}(?:-[a-fA-F0-9]{4}){3}-[a-fA-F0-9]{12})\b`), "[redacted id]"},
	{regexp.MustCompile(`(?i)[a-z]:[\\/][^\r\n"'<>:]+`), "[home path]"},
	{regexp.MustCompile(`(?i)/(?:Users|home|Volumes|mnt|media|private|tmp|var|opt|Applications|usr|etc|run)/[^\r\n"'<>:]+`), "[home path]"},
	{regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), "[email]"},
	// IPv6 must precede IPv4 so mapped addresses are removed as a whole. These
	// expressions also run in GuestRunner's Python re engine.
	{regexp.MustCompile(`(?i)(^|[^a-z0-9_])(?:\[` + ipv6Address + `(?:%[a-z0-9_.-]+)?\](?::[0-9]+)?|` + ipv6Address + `(?:%[a-z0-9_.-]+)?)`), "${1}[ip]"},
	{regexp.MustCompile(`\b` + ipv4Address + `\b(?::[0-9]+)?`), "[ip]"},
	// Older logs already contain masked IPs with their original ports attached.
	{regexp.MustCompile(`\[ip\]:[0-9]+`), "[ip]"},
	// Core errors contain bare endpoints as well as URLs. Remove the complete
	// hostname and optional port, including punycode labels and DNS trailing dots.
	{regexp.MustCompile(`(?i)\b(?:[a-z0-9_][a-z0-9_-]*\.)+(?:xn--[a-z0-9-]+|[a-z]{2,})\b\.?(?::[0-9]+)?`), "[host]"},
}

func Redact(s string) string {
	for _, p := range patterns {
		s = p.re.ReplaceAllString(s, p.replacement)
	}
	return strings.ToValidUTF8(s, "�")
}
