package guestos

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Windows PowerShell 5.1 reads files without a BOM in the system ANSI code
// page. Cage writes UTF-8 JSON, and a Chinese label or user path then breaks
// ConvertFrom-Json on code page 936 hosts and guests (#106).
func TestWindowsScriptsReadJSONAsUTF8(t *testing.T) {
	root := filepath.Join("..", "..", "scripts", "guest-os")
	read := regexp.MustCompile(`Get-Content\b[^|]*\|\s*ConvertFrom-Json`)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.EqualFold(filepath.Ext(path), ".ps1") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			for _, call := range read.FindAllString(line, -1) {
				if !strings.Contains(call, "-Encoding UTF8") {
					t.Errorf("%s:%d reads JSON in the ANSI code page: %s", path, i+1, strings.TrimSpace(call))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
