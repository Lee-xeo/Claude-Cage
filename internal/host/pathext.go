package host

import "strings"

// Bundled QEMU and OpenSSH are looked up without an extension, which on
// Windows depends on PATHEXT. A launcher such as WSL can pass a PATHEXT
// without .EXE; keep its other entries and put the standard ones first.
var standardPathExt = []string{".COM", ".EXE", ".BAT", ".CMD"}

func normalizePathExt(raw string) string {
	result := append([]string(nil), standardPathExt...)
	for _, item := range strings.Split(raw, ";") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if !strings.HasPrefix(item, ".") {
			item = "." + item
		}
		known := false
		for _, existing := range result {
			if strings.EqualFold(existing, item) {
				known = true
				break
			}
		}
		if !known {
			result = append(result, item)
		}
	}
	return strings.Join(result, ";")
}
