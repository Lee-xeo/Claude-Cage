package uilocale

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"
)

func containsHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func TestNativeCatalogMatchesSourceKeys(t *testing.T) {
	var catalogs map[string]map[string]string
	if err := json.Unmarshal(nativeCatalog, &catalogs); err != nil {
		t.Fatal(err)
	}
	var reference map[string]string
	for language, messages := range catalogs {
		if reference == nil {
			reference = messages
		}
		if len(messages) != len(reference) {
			t.Errorf("%s has %d native messages, want %d", language, len(messages), len(reference))
		}
		for source, text := range messages {
			if _, ok := reference[source]; !ok {
				t.Errorf("%s has an unexpected native message %q", language, source)
			}
			if strings.TrimSpace(text) == "" || strings.Count(text, "{0}") != strings.Count(source, "{0}") {
				t.Errorf("%s native message %q has a missing translation or placeholder", language, source)
			}
			if language != "zh-TW" && containsHan(text) {
				t.Errorf("%s native message %q contains untranslated Han characters: %q", language, source, text)
			}
		}
	}

	// Verify every native UI string literal in macOS, Windows, and Linux shells is in native.json.
	callRe := regexp.MustCompile(`\b(?:CageLanguage\.(?:text|format)|InterfaceLanguage\.(?:Text|Format)|ui_text|ui_format)\(\s*("(?:[^"\\]|\\.)*")`)
	shellFiles := []string{
		filepath.Join("..", "..", "desktop", "Cage.swift"),
		filepath.Join("..", "..", "desktop", "windows", "MainWindow.cs"),
		filepath.Join("..", "..", "desktop", "linux", "cage-desktop.c"),
	}
	for _, path := range shellFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, match := range callRe.FindAllSubmatch(data, -1) {
			var key string
			if err := json.Unmarshal(match[1], &key); err != nil {
				t.Fatalf("unquote %s in %s: %v", match[1], path, err)
			}
			if _, ok := reference[key]; !ok {
				t.Errorf("%s references native key %q missing from native.json", path, key)
			}
		}
	}
}

func TestMatchHostDisplayLanguages(t *testing.T) {
	tests := []struct {
		preferred []string
		want      string
	}{
		{nil, "zh-CN"},
		{[]string{"en-US", "de-DE"}, "zh-CN"},
		{[]string{"de-DE", "fr-CA", "ko-KR"}, "fr"},
		{[]string{"ru_RU.UTF-8"}, "ru"},
		{[]string{"zh-Hant-CN"}, "zh-TW"},
		{[]string{"zh-Hans-TW"}, "zh-CN"},
		{[]string{"zh-HK"}, "zh-TW"},
		{[]string{"zh_SG"}, "zh-CN"},
		{[]string{"fa-AF"}, "prs"},
		{[]string{"fa-IR"}, "fa"},
		{[]string{"prs-Arab-AF"}, "prs"},
		{[]string{"ps_PK.UTF-8"}, "ps"},
		{[]string{"ti_ER.UTF-8@saaho"}, "ti"},
		{[]string{"my-MM"}, "my"},
	}
	for _, tc := range tests {
		if got := Match(tc.preferred); got != tc.want {
			t.Errorf("Match(%q) = %q, want %q", tc.preferred, got, tc.want)
		}
	}
}
