package workbench

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/appupdate"
	"github.com/sylvinus/agent-vm/internal/version"
)

func TestAboutUsesLocalBuildAndAuthenticatedUpdateEgress(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	s := testServer(t)
	// A selected remote host must never own this software's build information.
	s.remote = newRemoteGateway(s.profile)
	s.remote.setRemote(true)
	t.Cleanup(s.remote.close)
	if got := request(s, "GET", "/api/client/about", "", "", false).Code; got != 401 {
		t.Fatalf("unauthenticated: %d", got)
	}
	if got := request(s, "POST", "/api/client/about", "https://untrusted.example", "{}", true).Code; got != 403 {
		t.Fatalf("origin: %d", got)
	}
	w := request(s, "GET", "/api/client/about", "", "", true)
	var build version.Build
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &build) != nil || build.Version != version.Version {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	// The desktop gateway uses a separate configuration; ensure that the local
	// app's update profile is used instead of the gateway or selected workload.
	s.appUpdateConfigPath = filepath.Join(t.TempDir(), "not-configured.json")
	w = request(s, "POST", "/api/client/about", s.origin, "{}", true)
	var check appupdate.AboutCheck
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &check) != nil {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if check.Source.Status != "failed" || check.Release.Status != "failed" {
		t.Fatalf("%+v", check)
	}
	if strings.Contains(w.Body.String(), "secret") {
		t.Fatal("credentials leaked")
	}
}
