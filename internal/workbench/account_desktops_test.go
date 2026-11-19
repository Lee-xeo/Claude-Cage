package workbench

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestDesktopCatalogUsesStableNumbersWithoutStartingGuest(t *testing.T) {
	s := testServer(t)
	accounts := []claudeAccount{{ID: strings.Repeat("a", 24), Label: "工作", Number: 9, DesktopDedicatedNetwork: true}, {ID: strings.Repeat("b", 24), Label: "个人", Number: 3}}
	if err := s.saveClaudeAccounts(claudeAccountList{Accounts: accounts}); err != nil {
		t.Fatal(err)
	}
	var view struct {
		Supported bool
		Accounts  []desktopEntry
	}
	resp := request(s, "GET", "/api/desktop-instances", s.origin, "", true)
	if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &view) != nil || !view.Supported || len(view.Accounts) != 2 {
		t.Fatalf("catalog: %d %s", resp.Code, resp.Body.String())
	}
	if a := view.Accounts[0]; a.Number != 9 || a.ID != accounts[0].ID || a.Running || !a.Dedicated {
		t.Fatalf("incorrect account state: %+v", a)
	}
	if a := view.Accounts[1]; a.Number != 3 || a.Dedicated {
		t.Fatalf("second entry: %+v", a)
	}
}

func TestDesktopInstanceNeverFallsBackToNativeDefault(t *testing.T) {
	s := testServer(t)
	id := strings.Repeat("a", 24)
	if w := request(s, "POST", "/api/desktop-instances/"+id+"/start", "https://claude.ai", "{}", true); w.Code != 403 {
		t.Fatalf("cross-origin start: %d", w.Code)
	}
	if w := request(s, "POST", "/api/desktop-instances/default/start", s.origin, "{}", true); w.Code != 404 {
		t.Fatalf("invalid instance: %d", w.Code)
	}
	if w := request(s, "POST", "/api/desktop-instances/"+id+"/stop", s.origin, "{}", false); w.Code != 401 {
		t.Fatalf("unauthenticated stop: %d", w.Code)
	}
}

func TestBoundAccountProbeUsesSubscriptionAndGuestCapability(t *testing.T) {
	s := testServer(t)
	first := claudeAccount{ID: strings.Repeat("a", 24), Source: "default", Email: "one@example.org", ProxyNodeID: strings.Repeat("c", 24)}
	second := claudeAccount{ID: strings.Repeat("b", 24), Source: "default", Email: "two@example.org"}
	if err := s.saveClaudeAccounts(claudeAccountList{Accounts: []claudeAccount{first, second}}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []claudeAccount{first, second} {
		route, err := s.boundGuestAccount("default", claudeIdentityHash(a.Email, ""))
		if err != nil {
			t.Fatal(err)
		}
		if a.ID == first.ID && (route == nil || route.ID != first.ID) || a.ID == second.ID && route != nil {
			t.Fatal("default profile took another subscription's route")
		}
	}
}

func TestPendingLoginProbeKeepsSelectedNodeBeforeEnrollment(t *testing.T) {
	s := testServer(t)
	id, node := strings.Repeat("a", 24), strings.Repeat("b", 24)
	if err := s.saveClaudeAccounts(claudeAccountList{Accounts: []claudeAccount{}, PendingLogin: id, PendingProxyNode: node}); err != nil {
		t.Fatal(err)
	}
	route, err := s.boundGuestAccount(id, "")
	if err != nil || route == nil || route.ProxyNodeID != node || route.ID != id {
		t.Fatal("login completion probe lost its draft route", route, err)
	}
	other, err := s.boundGuestAccount(strings.Repeat("c", 24), "")
	if err != nil || other != nil {
		t.Fatal("unrelated login took draft route", other, err)
	}
}
