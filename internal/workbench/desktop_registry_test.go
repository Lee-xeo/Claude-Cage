package workbench

import (
	"strings"
	"testing"
)

func TestDesktopObservedIdentityOwnsExactlyOneInstance(t *testing.T) {
	code := claudeAccount{ID: strings.Repeat("a", 24), Source: "managed", Label: "Code project", Status: "ready", Email: "owner@example.org", Organization: "4491e880-4322-4cf7-8c34-c3ea9fc835e9", Enabled: true}
	list := claudeAccountList{Accounts: []claudeAccount{code}}
	login := claudeGuestSnapshot{LoggedIn: true, Email: code.Email, Organization: code.Organization}
	transient, _ := list.createDesktop("")
	repeated, _ := list.createDesktop("")
	if transient.ID != repeated.ID || transient.AccountID != "" {
		t.Fatal("created duplicate pending Desktop")
	}
	changed, dest, restart := list.bindDesktop(transient.ID, login)
	if !changed || restart || dest != transient.ID || len(list.Accounts) != 1 || list.Accounts[0].DesktopStatus != "ready" || list.Accounts[0].DesktopInstanceID != transient.ID {
		t.Fatalf("initial binding should preserve the running session: %+v", list)
	}
	selected, err := list.createDesktop(code.ID)
	if err != nil || selected.ID != transient.ID || selected.Number != code.Number || selected.AccountID != code.ID {
		t.Fatal("selection lost existing instance", selected)
	}
	duplicate, _ := list.createDesktop("")
	changed, dest, restart = list.bindDesktop(duplicate.ID, login)
	if !changed || !restart || dest != transient.ID || list.DesktopRedirects[duplicate.ID] != dest {
		t.Fatal("duplicate was not merged", list)
	}
	secondOrg := login
	secondOrg.Organization = "68001639-9310-42a3-b5ce-29dfa6008f71"
	changed, dest, restart = list.bindDesktop(transient.ID, secondOrg)
	if !changed || !restart || dest != transient.ID || len(list.Accounts) != 2 || list.Accounts[0].DesktopInstanceID != "" || !list.Accounts[1].CodeUnlinked || list.Accounts[1].Enabled || list.Accounts[1].Status != "signed_out" {
		t.Fatal("organization switch lost identity or granted Code credentials", list)
	}
	if !list.Accounts[0].Enabled || list.Accounts[0].Status != "ready" {
		t.Fatal("original Code state changed")
	}
	if err = list.validateDesktopBindings(); err != nil {
		t.Fatal(err)
	}
	second, _ := list.createDesktop(code.ID)
	if second.ID == transient.ID {
		t.Fatal("original account was issued switched profile")
	}
}

func TestDesktopSharedDefaultAndDedicatedScopeRebinding(t *testing.T) {
	account := claudeAccount{ID: strings.Repeat("a", 24), Source: "managed", Status: "ready", Email: "a@example.org", Organization: "d925ea0c-a17e-42da-88ae-eec3311313d2", ProxyNodeID: strings.Repeat("b", 24)}
	login := claudeGuestSnapshot{LoggedIn: true, Email: account.Email, Organization: account.Organization}
	for _, id := range []string{"default", strings.Repeat("f", 24)} {
		list := claudeAccountList{Accounts: []claudeAccount{account}}
		if id != "default" {
			list.PendingDesktop = id
		}
		_, dest, restart := list.bindDesktop(id, login)
		if !restart || dest != id || list.Accounts[0].DesktopInstanceID != id {
			t.Fatal("dedicated node must apply to the bound physical instance", list)
		}
		_, _, restart = list.bindDesktop(id, login)
		if restart {
			t.Fatal("restarted a confirmed unchanged identity")
		}
	}
}

func TestDefaultDesktopRestartsWhenItFindsAnExistingCodeProfile(t *testing.T) {
	account := claudeAccount{
		ID: strings.Repeat("a", 24), Source: "managed", Status: "signed_out",
		Email: "owner@example.org", Organization: "4491e880-4322-4cf7-8c34-c3ea9fc835e9",
	}
	list := claudeAccountList{Accounts: []claudeAccount{account}}
	login := claudeGuestSnapshot{LoggedIn: true, Email: account.Email, Organization: account.Organization}

	changed, dest, restart := list.bindDesktop("default", login)
	if !changed || dest != "default" || !restart {
		t.Fatal("the default Desktop must restart to read the existing account's Code history")
	}
	if owner := list.desktopOwner("default"); owner == nil || owner.guestID() != account.guestID() {
		t.Fatal("restart must keep the verified account's existing Code profile")
	}
	if changed, _, restart := list.bindDesktop("default", login); changed || restart {
		t.Fatal("the same account observation after restart must leave the Desktop running")
	}
}

func TestDesktopLegacyRowsRequireExistingGuestProfile(t *testing.T) {
	used, unused := strings.Repeat("1", 24), strings.Repeat("2", 24)
	list := claudeAccountList{Accounts: []claudeAccount{{ID: used}, {ID: unused}}}
	if !list.migrateDesktopInstances(map[string]desktopObservation{used: {}}) || list.Accounts[0].DesktopInstanceID != used || list.Accounts[1].DesktopInstanceID != "" {
		t.Fatal(list)
	}
	if list.migrateDesktopInstances(map[string]desktopObservation{used: {}}) {
		t.Fatal("migration must be idempotent")
	}
}

func TestDuplicateDesktopReturnsToPreviouslyAuthorizedDefault(t *testing.T) {
	account := claudeAccount{ID: strings.Repeat("a", 24), Source: "managed", Status: "ready", Email: "existing@example.org", Organization: "40126b9e-07ff-48c5-bb9f-e361d2b9c724", DesktopInstanceID: "default", DesktopStatus: "ready"}
	list := claudeAccountList{Accounts: []claudeAccount{account}, PendingDesktop: strings.Repeat("b", 24)}
	duplicate := list.PendingDesktop
	changed, owner, closeDuplicate := list.bindDesktop(duplicate, claudeGuestSnapshot{LoggedIn: true, Email: account.Email, Organization: account.Organization})
	if !changed || !closeDuplicate || owner != "default" || list.PendingDesktop != "" || list.DesktopRedirects[duplicate] != "default" || len(list.Accounts) != 1 || list.Accounts[0] != account {
		t.Fatal("original default data and identity must be preserved", list)
	}
}
