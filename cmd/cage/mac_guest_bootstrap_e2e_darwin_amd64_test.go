//go:build e2e && darwin && amd64

package main

import (
	"context"
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/sylvinus/agent-vm/internal/vm"
)

// Build and inspect the real offline product without installing anything on
// the host. Inputs must be explicitly supplied from the prepared test kit.
func TestIntelMacOSBootstrapProduct(t *testing.T) {
	helper := os.Getenv("CAGE_TEST_INTEL_GUEST_CHECK")
	if helper == "" {
		t.Skip("select the built guest identity helper")
	}
	dir := t.TempDir()
	if err := prepareIntelBootstrapPackage(context.Background(), dir, helper, vm.IntelMacOSMedia{Installation: uuid.New(), UUID: uuid.NewString()}); err != nil {
		t.Fatal(err)
	}
	product := filepath.Join(dir, "cage-bootstrap.pkg")
	expanded := filepath.Join(dir, "expanded")
	if out, err := exec.Command("/usr/sbin/pkgutil", "--expand-full", product, expanded).CombinedOutput(); err != nil {
		t.Fatalf("expand: %v %s", err, out)
	}
	distribution, err := os.ReadFile(filepath.Join(expanded, "Distribution"))
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Product struct {
			ID      string `xml:"id,attr"`
			Version string `xml:"version,attr"`
		} `xml:"product"`
	}
	if err := xml.Unmarshal(distribution, &spec); err != nil || spec.Product.ID != "dev.cage.intel-bootstrap.product" || spec.Product.Version != "1" {
		t.Fatal("offline product has no startosinstall product identity")
	}
	if !strings.Contains(string(distribution), "dev.cage.intel-bootstrap") || strings.Contains(string(distribution), "io.macfuse") {
		t.Fatal("bootstrap must be independent of the interactive macFUSE product")
	}
	postinstall, err := os.ReadFile(filepath.Join(expanded, "bootstrap-component.pkg", "Scripts", "postinstall"))
	if err != nil || !strings.Contains(string(postinstall), "launchctl bootstrap system /Library/LaunchDaemons/dev.cage.intel-init.plist") || !strings.Contains(string(postinstall), "--hardware-uuid") {
		t.Fatal("live installation does not activate its bound first-boot job")
	}
	payload := filepath.Join(expanded, "bootstrap-component.pkg", "Payload")
	job := filepath.Join(payload, "Library/LaunchDaemons/dev.cage.intel-init.plist")
	out, err := exec.Command("/usr/bin/plutil", "-extract", "ProgramArguments.0", "raw", "-o", "-", job).Output()
	if err != nil || strings.TrimSpace(string(out)) != "/bin/sh" {
		t.Fatal("bootstrap has no explicit system shell")
	}
	built, err := os.ReadFile(filepath.Join(payload, "usr/local/libexec/cage-guest-check"))
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(helper)
	if err != nil || string(built) != string(original) {
		t.Fatal("packaging changed the signed identity helper")
	}
	if output := os.Getenv("CAGE_TEST_INTEL_BOOTSTRAP_OUTPUT"); output != "" {
		if !filepath.IsAbs(output) {
			t.Fatal("output must be absolute")
		}
		f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(product)
		if err == nil {
			_, err = f.Write(data)
		}
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			t.Fatal("could not retain test product")
		}
	}
}
