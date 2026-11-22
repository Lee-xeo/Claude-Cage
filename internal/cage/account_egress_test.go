package cage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sylvinus/agent-vm/internal/exitpolicy"
	"github.com/sylvinus/agent-vm/internal/managedproxy"
)

func TestAccountRouteRequiresBothGateAndNodeEvidence(t *testing.T) {
	g := fixtureGate(t)
	verified := 0
	accepted := true
	h := nodeHandler{parent: g, life: t.Context(), fingerprint: g.Status().Configuration, verify: func(context.Context) error {
		verified++
		if !accepted {
			return errors.New("exit changed")
		}
		return nil
	}}
	reject := func() {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/dns", nil))
		if w.Code != http.StatusForbidden {
			t.Fatalf("closed gate served request: %d", w.Code)
		}
	}
	reject()
	if verified != 0 {
		t.Fatal("closed gate sent probes")
	}
	fixtureGrant(t, g)
	permit, err := h.check(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	accepted = false
	h.checked = time.Now().Add(-time.Minute)
	reject()
	select {
	case <-permit.Done():
	default:
		t.Fatal("active connections retained node permission")
	}
	g.Close("offline")
	accepted = true
	h.checked = time.Time{}
	reject()
	if verified != 2 {
		t.Fatal("unexpected verification count", verified)
	}
}

func TestLocationLookupOptionsFollowWorkbenchPreference(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	if LocationLookupOptions(configPath).IPInfoFallback {
		t.Fatal("missing preferences must keep the ipinfo.io fallback off")
	}
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"reuse_claude_login":true}`, false},
		{`{"location_ipinfo_fallback":false}`, false},
		{`not json`, false},
		{`{"location_ipinfo_fallback":true}`, true},
	} {
		if err := os.WriteFile(filepath.Join(dir, "workbench.json"), []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := LocationLookupOptions(configPath).IPInfoFallback; got != tc.want {
			t.Fatalf("%s: IPInfoFallback = %v, want %v", tc.body, got, tc.want)
		}
	}
}

func TestSaveAndLoadNodeLocation(t *testing.T) {
	profile := t.TempDir()
	nodeID := strings.Repeat("a", 24)
	if _, ok := LoadNodeLocation(profile, nodeID); ok {
		t.Fatal("missing node location reported ok")
	}
	if _, ok := LoadNodeLocation(profile, "not-a-valid-node-id"); ok {
		t.Fatal("invalid node id reported ok")
	}
	policyFile := filepath.Join(managedproxy.Dir(profile), "account-policy", nodeID+".json")
	saveNodeLocation(policyFile, exitpolicy.Location{CountryCode: "jp", Timezone: "Asia/Tokyo"})
	got, ok := LoadNodeLocation(profile, nodeID)
	if !ok || got.CountryCode != "JP" || got.Timezone != "Asia/Tokyo" {
		t.Fatalf("LoadNodeLocation = (%+v, %v), want ({JP Asia/Tokyo}, true)", got, ok)
	}
}
