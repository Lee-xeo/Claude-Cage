package workbench

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Status and handoff wait on a Hyper-V probe that can outlast the ordinary
// launcher timeout (#140). Other requests keep the short timeout.
func TestControllerVMRequestsOutlastWindowsProbe(t *testing.T) {
	if controllerVMRequestTimeout <= windowsVMProbeTimeout || controllerReleaseRequestTimeout <= 2*windowsVMProbeTimeout || localBootstrapTimeout <= controllerVMRequestTimeout {
		t.Fatal("controller budgets do not cover the Windows VM probe")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	c := &controllerClient{origin: server.URL, client: &http.Client{Timeout: 50 * time.Millisecond}}
	for _, path := range []string{"/api/desktop-local/status", "/api/desktop-local/release", "/api/handoff"} {
		if status, err := c.call(http.MethodPost, path, nil, nil); err != nil || status != http.StatusOK {
			t.Fatalf("%s: status %d, %v", path, status, err)
		}
	}
	if _, err := c.call(http.MethodPost, "/api/session", nil, nil); err == nil {
		t.Fatal("ordinary request ignored the short client timeout")
	}
	if c.client.Timeout != 50*time.Millisecond {
		t.Fatal("extended budget changed the shared client")
	}
}
