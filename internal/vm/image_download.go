package vm

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// ImageDownloadMarker starts the line that names why each Debian image
// location failed. It carries only fixed codes, never a URL or server
// response, so it stays readable after diagnostics redaction.
const ImageDownloadMarker = "CAGE_IMAGE_DOWNLOAD_FAILED"

// debianImageMirrors are the locations cageDebianImages offers, in order, and
// the code each has in ImageDownloadMarker.
var debianImageMirrors = []struct{ base, code string }{
	{"https://cloud.debian.org/images/cloud/", "official"},
	{"https://mirrors.nju.edu.cn/debian-cdimage/cloud/", "nju"},
	{"https://mirrors.ustc.edu.cn/debian-cdimage/cloud/", "ustc"},
}

// fileutils.DownloadFile reports each candidate as "failed to download
// `URL`: cause"; the candidates of one image are joined line by line.
var imageDownloadFailure = regexp.MustCompile("failed to download `(https?://[^`]+)`: ([^\n]*)")
var httpStatusCode = regexp.MustCompile(`(?i)\bhttp (\d{3})\b`)

// ImageDownloadSummary describes a failed download of the Debian image: one
// "location:cause:partial" token per attempted location. It is empty when err
// is not such a failure.
func ImageDownloadSummary(err error) string {
	if err == nil {
		return ""
	}
	var tokens []string
	for _, match := range imageDownloadFailure.FindAllStringSubmatch(err.Error(), -1) {
		location := "other"
		for _, mirror := range debianImageMirrors {
			if strings.HasPrefix(match[1], mirror.base) {
				location = mirror.code
			}
		}
		partial := "none"
		if strings.Contains(match[2], "partial data retained") {
			partial = "kept"
		}
		tokens = append(tokens, location+":"+imageDownloadCause(match[2])+":"+partial)
	}
	if len(tokens) == 0 {
		return ""
	}
	return ImageDownloadMarker + " " + strings.Join(tokens, " ")
}

// imageDownloadCause reduces a download error to a fixed code.
func imageDownloadCause(text string) string {
	lower := strings.ToLower(text)
	if m := httpStatusCode.FindStringSubmatch(lower); m != nil {
		return "http-" + m[1]
	}
	// httpclientutil.HTTPStatusError names the status by its text.
	for code := 400; code < 600; code++ {
		if name := http.StatusText(code); name != "" && strings.Contains(lower, "unexpected http status "+strings.ToLower(name)) {
			return fmt.Sprintf("http-%d", code)
		}
	}
	for _, rule := range []struct {
		code     string
		patterns []string
	}{
		// hostproxy stops the download, without falling back to a direct
		// connection, when the host's proxy settings or PAC cannot be read or
		// resolved; its messages name the host proxy this way. Checked first:
		// a wrapped cause may itself mention a timeout.
		{"proxy-config", []string{"主机系统代理", "主机代理", "主机自动代理", "主机桌面代理", "主机 pac", "macos 系统代理", "桌面 pac"}},
		// net/http names a failed dial to the host's HTTP or SOCKS proxy this
		// way; the mirror itself was never reached.
		{"proxy", []string{"proxyconnect", "socks connect"}},
		{"checksum", []string{"checksum mismatch", "digest", "sha512"}},
		{"dns", []string{"no such host", "server misbehaving", "temporary failure in name resolution"}},
		{"refused", []string{"connection refused"}},
		{"timeout", []string{"timeout", "timed out", "deadline exceeded"}},
		{"tls", []string{"tls", "x509", "certificate"}},
		{"eof", []string{"eof", "connection reset", "broken pipe"}},
	} {
		for _, pattern := range rule.patterns {
			if strings.Contains(lower, pattern) {
				return rule.code
			}
		}
	}
	return "other"
}
