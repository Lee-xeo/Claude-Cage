package hostbridge

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type tarFile struct {
	name string
	body string
	kind byte
}

func packTar(t *testing.T, files ...tarFile) []byte {
	t.Helper()
	var packed bytes.Buffer
	compressed := gzip.NewWriter(&packed)
	archive := tar.NewWriter(compressed)
	for _, file := range files {
		kind := file.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		header := &tar.Header{Name: file.name, Typeflag: kind, Mode: 0o755, Size: int64(len(file.body))}
		if kind == tar.TypeSymlink {
			header.Linkname, header.Size = file.body, 0
		}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if kind == tar.TypeReg {
			if _, err := archive.Write([]byte(file.body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return packed.Bytes()
}

const marketManifest = `{"name":"notes","version":"1.2.0","description":"Keeps notes","permissions":["files"],"command":["python3","server.py"]}`

func serveMarket(t *testing.T, archives map[string][]byte, entries func(base string) []MarketEntry) (Market, *http.Client) {
	t.Helper()
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	market := Market{IndexURL: server.URL + "/index.json", ArchivePrefix: server.URL + "/dist/"}
	mux.HandleFunc("/index.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"version": 1, "plugins": entries(market.ArchivePrefix)})
	})
	mux.HandleFunc("/dist/", func(w http.ResponseWriter, r *http.Request) {
		data, ok := archives[strings.TrimPrefix(r.URL.Path, "/dist/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	})
	return market, server.Client()
}

func entryFor(base, file string, data []byte) MarketEntry {
	digest := sha256.Sum256(data)
	return MarketEntry{Name: "notes", Version: "1.2.0", Description: "Keeps notes", Permissions: []string{"files"},
		Archive: base + file, SHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(data))}
}

func TestMarketInstallsAVerifiedPlugin(t *testing.T) {
	good := packTar(t, tarFile{name: "notes/", kind: tar.TypeDir}, tarFile{name: "notes/plugin.json", body: marketManifest}, tarFile{name: "notes/lib/server.py", body: "print()"})
	market, client := serveMarket(t, map[string][]byte{"notes-1.2.0.tar.gz": good}, func(base string) []MarketEntry {
		elsewhere := entryFor(base, "notes-1.2.0.tar.gz", good)
		elsewhere.Name, elsewhere.Archive = "elsewhere", "https://example.com/x.tar.gz"
		nested := entryFor(base, "../index.json", good)
		nested.Name = "nested"
		return []MarketEntry{entryFor(base, "notes-1.2.0.tar.gz", good), elsewhere, nested, entryFor(base, "notes-1.2.0.tar.gz", good)}
	})
	entries, err := market.Fetch(context.Background(), client)
	if err != nil || len(entries) != 1 || entries[0].Name != "notes" {
		t.Fatalf("entries %+v: %v", entries, err)
	}
	dir := t.TempDir()
	if err := market.Install(context.Background(), client, entries[0], dir); err != nil {
		t.Fatal(err)
	}
	if manifest, err := LoadManifest(filepath.Join(dir, "notes")); err != nil || manifest.Version != "1.2.0" {
		t.Fatalf("manifest %+v: %v", manifest, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes", "lib", "server.py")); err != nil {
		t.Fatal(err)
	}
	if err := market.Install(context.Background(), client, entries[0], dir); err == nil {
		t.Fatal("replaced an installed plugin")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".installing-*")); len(left) != 0 {
		t.Fatalf("left %v behind", left)
	}
}

func TestMarketRefusesArchivesThatAreNotWhatTheListSays(t *testing.T) {
	good := packTar(t, tarFile{name: "notes/plugin.json", body: marketManifest})
	cases := map[string][]byte{
		"escapes":     packTar(t, tarFile{name: "notes/plugin.json", body: marketManifest}, tarFile{name: "notes/../../evil", body: "x"}),
		"outside":     packTar(t, tarFile{name: "notes/plugin.json", body: marketManifest}, tarFile{name: "other/file", body: "x"}),
		"link":        packTar(t, tarFile{name: "notes/plugin.json", body: marketManifest}, tarFile{name: "notes/link", body: "/etc/passwd", kind: tar.TypeSymlink}),
		"version":     packTar(t, tarFile{name: "notes/plugin.json", body: strings.Replace(marketManifest, "1.2.0", "9.9.9", 1)}),
		"permissions": packTar(t, tarFile{name: "notes/plugin.json", body: strings.Replace(marketManifest, `["files"]`, `["files","commands"]`, 1)}),
		"manifest":    packTar(t, tarFile{name: "notes/readme", body: "x"}),
	}
	archives := map[string][]byte{"good.tar.gz": good}
	for name, data := range cases {
		archives[name+".tar.gz"] = data
	}
	market, client := serveMarket(t, archives, func(string) []MarketEntry { return nil })
	for name, data := range cases {
		dir := t.TempDir()
		if err := market.Install(context.Background(), client, entryFor(market.ArchivePrefix, name+".tar.gz", data), dir); err == nil {
			t.Errorf("%s: installed", name)
		}
		if left, _ := os.ReadDir(dir); len(left) != 0 {
			t.Errorf("%s: left %v behind", name, left)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil")); err == nil {
			t.Errorf("%s: wrote outside the plugin folder", name)
		}
	}
	// The digest in the list is for other bytes than the server sends.
	tampered := entryFor(market.ArchivePrefix, "good.tar.gz", good)
	tampered.SHA256 = strings.Repeat("0", 64)
	if err := market.Install(context.Background(), client, tampered, t.TempDir()); err == nil {
		t.Error("installed an archive that does not match its digest")
	}
	short := entryFor(market.ArchivePrefix, "good.tar.gz", good)
	short.Bytes--
	if err := market.Install(context.Background(), client, short, t.TempDir()); err == nil {
		t.Error("installed an archive larger than the list says")
	}
}

func TestPluginIcons(t *testing.T) {
	dir := t.TempDir()
	write := func(icon string) {
		manifest := strings.Replace(marketManifest, `"permissions"`, `"icon":"`+icon+`","permissions"`, 1)
		if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, bad := range []string{"../icon.png", "/etc/icon.png", "icon.gif", "a/../icon.svg", "icon.html"} {
		write(bad)
		if _, err := LoadManifest(dir); err == nil {
			t.Errorf("accepted icon %q", bad)
		}
	}
	write("art/icon.svg")
	if _, _, err := ReadIcon(dir); err == nil {
		t.Fatal("read an icon that is not there")
	}
	if err := os.MkdirAll(filepath.Join(dir, "art"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "art", "icon.svg"), []byte("<svg/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if data, kind, err := ReadIcon(dir); err != nil || kind != "image/svg+xml" || string(data) != "<svg/>" {
		t.Fatalf("icon %q %q: %v", data, kind, err)
	}

	market := Market{IndexURL: "https://example.test/index.json", ArchivePrefix: "https://example.test/dist/", IconPrefix: "https://example.test/plugins/"}
	icons := map[string]string{
		"https://example.test/plugins/notes/icon.png":      "https://example.test/plugins/notes/icon.png",
		"https://example.test/plugins/other/icon.png":      "",
		"https://elsewhere.test/plugins/notes/icon.png":    "",
		"https://example.test/plugins/notes/../x/icon.png": "",
		"https://example.test/plugins/notes/icon.png?x=1":  "",
		"https://example.test/plugins/notes/page.html":     "",
	}
	for icon, want := range icons {
		entry := MarketEntry{Name: "notes", Version: "1", Archive: market.ArchivePrefix + "notes-1.tar.gz", SHA256: strings.Repeat("a", 64), Bytes: 10, Icon: icon}
		body, _ := json.Marshal(map[string]any{"version": 1, "plugins": []MarketEntry{entry}})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
		served := market
		served.IndexURL = server.URL
		entries, err := served.Fetch(context.Background(), server.Client())
		server.Close()
		if err != nil || len(entries) != 1 || entries[0].Icon != want {
			t.Errorf("icon %q: got %+v, %v", icon, entries, err)
		}
	}
}
