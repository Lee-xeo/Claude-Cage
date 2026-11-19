package workbench

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The list of schemes is written for the link opener, and each is made the
// opener's to handle; with none left, both go.
func TestPluginLinksScriptRegistersAndRemoves(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the script runs in the environment, which is Linux")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"sudo":     "#!/bin/sh\nwhile [ \"${1#-}\" != \"$1\" ]; do case \"$1\" in -u) shift 2 ;; *) shift ;; esac; done\nexec \"$@\"\n",
		"xdg-mime": "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$ROOT/registered\"\n",
		"id":       "#!/bin/sh\n[ \"$1\" = -un ] && { printf tester; exit 0; }\nexit 0\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run := func(schemes ...string) {
		script := strings.ReplaceAll(pluginLinksScript(schemes), "/usr/local/", root+"/")
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin", "ROOT="+root)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	run("demo", "notes")
	if list, err := os.ReadFile(filepath.Join(root, "lib", "plugin-links")); err != nil || string(list) != "demo\nnotes\n" {
		t.Fatalf("list %q %v", list, err)
	}
	entry, err := os.ReadFile(filepath.Join(root, "share", "applications", "plugin-links.desktop"))
	if err != nil || !strings.Contains(string(entry), "MimeType=x-scheme-handler/demo;x-scheme-handler/notes;") {
		t.Fatalf("entry %q %v", entry, err)
	}
	registered, _ := os.ReadFile(filepath.Join(root, "registered"))
	if strings.Count(string(registered), "default plugin-links.desktop x-scheme-handler/") != 4 {
		t.Fatalf("registered for both users:\n%s", registered)
	}
	run()
	for _, path := range []string{filepath.Join(root, "lib", "plugin-links"), filepath.Join(root, "share", "applications", "plugin-links.desktop")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s left behind: %v", path, err)
		}
	}
}

// A link no running plugin handles is refused, and a link already handed on
// by another window is not handed on again.
func TestPluginLinkIsHandedOnOnce(t *testing.T) {
	s := &Server{}
	post := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/host-tools/link", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		s.openPluginLink(w, r)
		return w
	}
	if w := post(`{"id":"","url":"demo:x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("a link without an id: %d", w.Code)
	}
	if w := post(`{"id":"one","url":"demo:x"}`); w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "-32003") {
		t.Fatalf("no plugin: %d %s", w.Code, w.Body)
	}
	if w := post(`{"id":"one","url":"demo:x"}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"duplicate":true`) {
		t.Fatalf("the same link again: %d %s", w.Code, w.Body)
	}
}
