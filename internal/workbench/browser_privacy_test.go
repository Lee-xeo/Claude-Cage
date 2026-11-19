package workbench

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestBrowserPrivacyHTTPContract(t *testing.T) {
	s := testServer(t)
	s.ready = true
	calls := 0
	s.browserControl = browserClientForTest(t, s, browserTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("Authorization") != "Bearer "+s.credentials.Token {
			t.Fatal("guest authentication missing")
		}
		var input browserInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		switch r.URL.Path {
		case "/new":
			if !input.Incognito || input.URL != "https://example.test/" {
				t.Fatalf("private flag lost: %+v", input)
			}
		case "/cookies":
			if input.ID != "private-tab" || input.Scope != "site" || input.Query != "session" || input.Page != 2 || input.URL != "https://example.test/" {
				t.Fatalf("cookie input lost: %+v", input)
			}
			if input.Mode == "delete-selected" {
				if len(input.CookieKeys) != 2 || input.CookieKeys[0] != strings.Repeat("a", 64) || input.CookieKeys[1] != strings.Repeat("b", 64) {
					t.Fatalf("cookie selection lost: %+v", input)
				}
			} else if input.Mode != "delete" || input.CookieKey != strings.Repeat("a", 64) {
				t.Fatalf("cookie target lost: %+v", input)
			}
		default:
			t.Fatalf("unexpected action: %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Header: make(http.Header)}, nil
	}))
	for _, body := range []struct{ path, input string }{
		{"new", `{"url":"https://example.test/","incognito":true}`},
		{"cookies", `{"id":"private-tab","url":"https://example.test/","mode":"delete","scope":"site","query":"session","page":2,"cookieKey":"` + strings.Repeat("a", 64) + `"}`},
		{"cookies", `{"id":"private-tab","url":"https://example.test/","mode":"delete-selected","scope":"site","query":"session","page":2,"cookieKeys":["` + strings.Repeat("a", 64) + `","` + strings.Repeat("b", 64) + `"]}`},
	} {
		response := request(s, "POST", "/api/browser/"+body.path, s.origin, body.input, true)
		if response.Code != 200 {
			t.Fatalf("%s: %d %s", body.path, response.Code, response.Body.String())
		}
	}
	if calls != 3 {
		t.Fatalf("calls = %d", calls)
	}
	for _, input := range []string{
		`{"mode":"clear","scope":"all"}`,
		`{"id":"tab","mode":"clear","scope":"arbitrary-context"}`,
		`{"id":"tab","mode":"delete","scope":"all","cookieKey":"bad"}`,
		`{"id":"tab","mode":"list","scope":"site","page":-1}`,
		`{"id":"tab","mode":"delete-selected","scope":"all","cookieKeys":[]}`,
		`{"id":"tab","mode":"delete-selected","scope":"all","cookieKeys":["bad"]}`,
		`{"id":"tab","mode":"delete-selected","scope":"all","cookieKeys":["` + strings.Repeat("z", 64) + `"]}`,
	} {
		response := request(s, "POST", "/api/browser/cookies", s.origin, input, true)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid input accepted: %s: %d", input, response.Code)
		}
	}
	if calls != 3 {
		t.Fatal("invalid request reached guest")
	}
	response := request(s, "POST", "/api/browser/cookies", s.origin, `{"id":"tab","mode":"list","scope":"all"}`, false)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request: %d", response.Code)
	}
	s.account.lock()
	response = request(s, "POST", "/api/browser/cookies", s.origin, `{"id":"tab","mode":"list","scope":"all"}`, true)
	if response.Code != http.StatusLocked {
		t.Fatalf("locked request: %d", response.Code)
	}
	if calls != 3 {
		t.Fatal("unauthorized request reached guest")
	}
}

func TestBrowserCookieSelectionAcrossManyPages(t *testing.T) {
	s := testServer(t)
	s.ready = true
	keys := make([]string, 1100)
	for index := range keys {
		keys[index] = strings.Repeat("a", 64)
	}
	s.browserControl = browserClientForTest(t, s, browserTransport(func(r *http.Request) (*http.Response, error) {
		var input browserInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if r.URL.Path != "/cookies" || input.Mode != "delete-selected" || len(input.CookieKeys) != len(keys) {
			t.Fatalf("large selection lost: path=%s, keys=%d", r.URL.Path, len(input.CookieKeys))
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Header: make(http.Header)}, nil
	}))
	body, err := json.Marshal(browserInput{ID: "tab", Mode: "delete-selected", Scope: "all", CookieKeys: keys})
	if err != nil {
		t.Fatal(err)
	}
	response := request(s, "POST", "/api/browser/cookies", s.origin, string(body), true)
	if response.Code != http.StatusOK {
		t.Fatalf("large selection: %d %s", response.Code, response.Body.String())
	}
}
