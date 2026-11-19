package workbench

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestBrowserWheelReachesTheLinuxControllerWithItsDistance(t *testing.T) {
	s := testServer(t)
	s.ready = true
	s.credentials.Token = "guest-test-token"
	calls, reply := 0, `{"handled":true}`
	s.browserControl = browserClientForTest(t, s, browserTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "http://127.0.0.1:18500/wheel" || r.Header.Get("Authorization") != "Bearer guest-test-token" {
			t.Fatal("wrong wheel destination or authentication")
		}
		var input map[string]int
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil || len(input) != 4 ||
			input["x"] != 0 || input["y"] != 600 || input["deltaX"] != -24 || input["deltaY"] != 0 {
			t.Fatal("wheel position or distance changed", err, input)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(reply))}, nil
	}))
	path := "/api/browser/wheel"
	// A position on the left edge and a purely horizontal distance keep their zeros.
	valid := `{"x":0,"y":600,"deltaX":-24,"deltaY":0}`
	if w := request(s, "POST", path, s.origin, valid, false); w.Code != 401 {
		t.Fatal("unauthenticated wheel accepted", w.Code)
	}
	if w := request(s, "POST", path, "https://example.com", valid, true); w.Code != 403 {
		t.Fatal("cross-origin wheel accepted", w.Code)
	}
	for _, body := range []string{
		`{"x":10,"y":10}`, `{"x":-1,"y":10,"deltaY":5}`, `{"x":10,"y":8193,"deltaY":5}`,
		`{"x":10,"y":10,"deltaY":8193}`, `{"x":10,"y":10,"deltaX":-8193}`, `{"x":10,"y":10,"deltaY":1.5}`,
	} {
		if w := request(s, "POST", path, s.origin, body, true); w.Code != 400 {
			t.Fatal("invalid wheel accepted", body, w.Code)
		}
	}
	if calls != 0 {
		t.Fatal("rejected wheel reached guest")
	}
	w := request(s, "POST", path, s.origin, valid, true)
	if w.Code != 200 || calls != 1 || strings.TrimSpace(w.Body.String()) != `{"handled":true}` {
		t.Fatal("valid wheel rejected", w.Code, w.Body.String())
	}
	// The controller's refusal is the viewer's cue to send wheel steps.
	reply = `{"handled":false}`
	if w = request(s, "POST", path, s.origin, valid, true); w.Code != 200 || strings.TrimSpace(w.Body.String()) != reply {
		t.Fatal("declined wheel not passed on", w.Code, w.Body.String())
	}
}

func TestBrowserWheelIsOfferedByTheGuest(t *testing.T) {
	offered := func(s *Server) bool {
		w := desktopRequestForTest(s, http.MethodGet, "/api/vnc-credentials", nil)
		var reply struct {
			Wheel bool `json:"pixel_wheel"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &reply) != nil {
			t.Fatal("display credentials unavailable", w.Code)
		}
		return reply.Wheel
	}
	s := testServer(t)
	s.ready = true
	if !offered(s) {
		t.Fatal("Linux guest does not offer the scrolled distance")
	}
}
