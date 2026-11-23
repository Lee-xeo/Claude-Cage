package managedproxy

import (
	"strings"
	"testing"
)

func TestMissingCoreErrorNamesLocationAndBuildStep(t *testing.T) {
	for _, c := range []struct{ name, goos, build string }{
		{"mihomo", "darwin", "make cage-core"},
		{"mihomo", "linux", "make cage-core"},
		{"mihomo.exe", "windows", "scripts/build-windows.ps1"},
	} {
		message := missingCoreError(c.name, c.goos).Error()
		for _, want := range []string{c.name + " 应与 cage 可执行文件位于同一目录", "proxy-core", c.build} {
			if !strings.Contains(message, want) {
				t.Errorf("%s: %q lacks %q", c.goos, message, want)
			}
		}
	}
}
