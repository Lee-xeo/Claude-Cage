package workbench

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sylvinus/agent-vm/internal/remotehost"
)

// launchWindowSession performs /api/launch then /api/session the way a
// desktop shell does and returns the cookies the window receives.
func launchWindowSession(t *testing.T, s *Server, launchBody string) []*http.Cookie {
	t.Helper()
	w := request(s, http.MethodPost, "/api/launch", s.origin, launchBody, false)
	if w.Code != http.StatusOK {
		t.Fatalf("launch %s = %d: %s", launchBody, w.Code, w.Body.String())
	}
	var launched launchResponse
	if err := json.Unmarshal(w.Body.Bytes(), &launched); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(launched.URL)
	if err != nil || u.Fragment == "" {
		t.Fatalf("launch URL = %q, %v", launched.URL, err)
	}
	w = request(s, http.MethodPost, "/api/session", s.origin, `{"token":"`+u.Fragment+`"}`, false)
	if w.Code != http.StatusOK {
		t.Fatalf("session = %d: %s", w.Code, w.Body.String())
	}
	return w.Result().Cookies()
}

func cookiesTrusted(s *Server, cookies []*http.Cookie) bool {
	r := httptest.NewRequest(http.MethodGet, s.origin+"/", nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	return s.hasTrustedView(r)
}

func TestTrustedViewTicketIsOnlyIssuedToDesktopEntryWindows(t *testing.T) {
	s := testServer(t)
	trusted := `{"token":"` + s.token + `","trusted_view":true}`
	if w := request(s, http.MethodPost, "/api/launch", s.origin, trusted, false); w.Code != http.StatusBadRequest {
		t.Fatalf("workload controller issued a trusted-view ticket: %d", w.Code)
	}
	s.desktopEntry = true
	if w := request(s, http.MethodPost, "/api/launch", s.origin, `{"token":"`+s.token+`","trusted_view":true,"remote_desktop":true}`, false); w.Code != http.StatusBadRequest {
		t.Fatalf("trusted view combined with remote desktop: %d", w.Code)
	}
	if w := request(s, http.MethodPost, "/api/launch", s.origin, `{"token":"wrong","trusted_view":true}`, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("trusted view without the launcher token: %d", w.Code)
	}

	window := launchWindowSession(t, s, trusted)
	if !cookiesTrusted(s, window) {
		t.Fatalf("desktop window session is not trusted: %+v", window)
	}
	for _, c := range window {
		if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Domain != "" {
			t.Fatalf("unsafe cookie attributes: %+v", c)
		}
	}
	// A browser opened by `cage desktop` and remote-desktop tickets stay untrusted.
	if browser := launchWindowSession(t, s, `{"token":"`+s.token+`"}`); cookiesTrusted(s, browser) {
		t.Fatal("plain launch ticket received the trusted-view proof")
	}
	if ssh := launchWindowSession(t, s, `{"token":"`+s.token+`","remote_desktop":true}`); cookiesTrusted(s, ssh) {
		t.Fatal("remote-desktop ticket received the trusted-view proof")
	}
}

func TestDesktopEntryWindowCanManageRemotePlugins(t *testing.T) {
	var forwarded atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/host-tools" {
			forwarded.Add(1)
		}
		jsonReply(w, map[string]bool{"enabled": true})
	}))
	defer upstream.Close()

	s := testServer(t)
	s.desktopEntry = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &remotehost.Session{
		Origin:    upstream.URL,
		Client:    upstream.Client(),
		Transport: upstream.Client().Transport,
		Context:   ctx,
		Manifest:  remotehost.PublicManifest{Platform: "linux", InstanceID: strings.Repeat("c", 64)},
	}
	s.remote = newDesktopGateway(s.profile, &gatewayLocalFixture{endpoint: upstream})
	t.Cleanup(s.remote.close)
	d := s.remote.desktop
	d.mu.Lock()
	d.active = &desktopTarget{id: "conn-remote-1", mode: "remote", ctx: ctx, cancel: cancel, session: session}
	d.generation = "conn-remote-1"
	d.mu.Unlock()

	enable := func(cookies []*http.Cookie) int {
		r := httptest.NewRequest(http.MethodPost, s.origin+"/api/host-tools", strings.NewReader(`{"enabled":true}`))
		r.Header.Set("Origin", s.origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(connectionHeader, "conn-remote-1")
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		s.handler().ServeHTTP(w, r)
		return w.Code
	}
	if code := enable(launchWindowSession(t, s, `{"token":"`+s.token+`"}`)); code != http.StatusForbidden || forwarded.Load() != 0 {
		t.Fatalf("untrusted browser session enabled remote plugins: %d, forwarded=%d", code, forwarded.Load())
	}
	if code := enable(launchWindowSession(t, s, `{"token":"`+s.token+`","trusted_view":true}`)); code != http.StatusOK || forwarded.Load() != 1 {
		t.Fatalf("desktop window could not enable remote plugins: %d, forwarded=%d", code, forwarded.Load())
	}
}
