package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sylvinus/agent-vm/internal/guestos"
	"github.com/sylvinus/agent-vm/internal/hostimage"
)

func TestVerifiedIntelImageDigestOnlyForTheUnchangedVerifiedFile(t *testing.T) {
	image := filepath.Join(t.TempDir(), "install.pkg")
	if err := os.WriteFile(image, []byte("signed image"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(image)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("ab", 32)
	install := func(mutate func(*hostimage.Plan)) *guestos.Installation {
		source := hostimage.Plan{Path: image, Digest: "sha256:" + digest, Bytes: info.Size(), Modified: info.ModTime().UnixNano()}
		mutate(&source)
		return &guestos.Installation{Source: source}
	}
	if got := verifiedIntelImageDigest(install(func(*hostimage.Plan) {}), image); got != digest {
		t.Fatalf("unchanged verified file: %q", got)
	}
	for name, mutate := range map[string]func(*hostimage.Plan){
		"other-algorithm": func(p *hostimage.Plan) { p.Digest = "sha1:" + digest[:40] },
		"other-path":      func(p *hostimage.Plan) { p.Path = image + ".other" },
		"other-size":      func(p *hostimage.Plan) { p.Bytes++ },
		"unknown-time":    func(p *hostimage.Plan) { p.Modified = 0 },
	} {
		if got := verifiedIntelImageDigest(install(mutate), image); got != "" {
			t.Errorf("%s: reused %q", name, got)
		}
	}
	later := info.ModTime().Add(time.Second)
	if err := os.Chtimes(image, later, later); err != nil {
		t.Fatal(err)
	}
	if got := verifiedIntelImageDigest(install(func(*hostimage.Plan) {}), image); got != "" {
		t.Fatalf("modified file reused %q", got)
	}
}
