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

func TestDesktopRenameAndDeletePreserveAccountState(t *testing.T) {
	account := claudeAccount{
		ID: strings.Repeat("a", 24), Source: "managed", Number: 4, Label: "工作账号",
		Status: "ready", Enabled: true, Email: "work@example.org", Organization: "4491e880-4322-4cf7-8c34-c3ea9fc835e9",
		ProxyNodeID: strings.Repeat("c", 24),
	}
	list := claudeAccountList{Accounts: []claudeAccount{account}}
	pending, err := list.createDesktop("")
	if err != nil {
		t.Fatal(err)
	}
	renamedPending, err := list.renameDesktop(pending.ID, "  临时测试桌面  ")
	if err != nil || renamedPending.Label != "临时测试桌面" {
		t.Fatalf("rename pending: %+v %v", renamedPending, err)
	}
	// Custom label stays with the instance after login binds it to the account.
	changed, dest, _ := list.bindDesktop(pending.ID, claudeGuestSnapshot{LoggedIn: true, Email: account.Email, Organization: account.Organization})
	if !changed || dest != pending.ID || list.desktopEntry(pending.ID).Label != "临时测试桌面" || list.Accounts[0].Label != "工作账号" {
		t.Fatalf("binding must preserve custom desktop label without mutating account label: %+v", list)
	}
	// Renaming default desktop works independently.
	renamedDefault, err := list.renameDesktop("default", "主桌面")
	if err != nil || renamedDefault.Label != "主桌面" {
		t.Fatalf("rename default: %+v %v", renamedDefault, err)
	}
	// Duplicate redirect cleans up the retired duplicate's custom label.
	dup, _ := list.createDesktop("")
	if _, err := list.renameDesktop(dup.ID, "重复实例"); err != nil {
		t.Fatal(err)
	}
	list.bindDesktop(dup.ID, claudeGuestSnapshot{LoggedIn: true, Email: account.Email, Organization: account.Organization})
	if list.DesktopRedirects[dup.ID] != pending.ID || list.DesktopLabels[dup.ID] != "" {
		t.Fatalf("redirect should drop retired custom label: %+v", list)
	}
	if err := list.validateDesktopBindings(); err != nil {
		t.Fatalf("valid bindings rejected: %v", err)
	}
	// Validation rejects control characters and untrimmed labels.
	if _, err := list.renameDesktop(pending.ID, "   "); err == nil {
		t.Fatal("expected empty rename error")
	}
	if _, err := list.renameDesktop(pending.ID, "line1\nline2"); err == nil {
		t.Fatal("expected control char rename error")
	}
	if _, err := list.renameDesktop(pending.ID, strings.Repeat("桌", 101)); err == nil {
		t.Fatal("expected overly long rename error")
	}
	// Both default and isolated instances can be deleted as long as another instance remains.
	if !list.deleteDesktop(pending.ID) {
		t.Fatal("expected bound desktop deletion to succeed when default remains")
	}
	// Default is now the sole remaining instance and cannot be deleted.
	if list.deleteDesktop("default") {
		t.Fatal("sole remaining default desktop must not be deletable")
	}
	if list.Accounts[0].DesktopInstanceID != "" || list.Accounts[0].DesktopStatus != "" {
		t.Fatalf("desktop binding not cleared: %+v", list.Accounts[0])
	}
	if list.Accounts[0].Label != "工作账号" || list.Accounts[0].Status != "ready" || !list.Accounts[0].Enabled || list.Accounts[0].ProxyNodeID != account.ProxyNodeID {
		t.Fatalf("deleting desktop mutated account data: %+v", list.Accounts[0])
	}
	if list.DesktopLabels[pending.ID] != "" || len(list.DesktopRedirects) != 0 {
		t.Fatalf("stale desktop metadata remained: %+v", list)
	}
	if err := list.validateDesktopBindings(); err != nil {
		t.Fatalf("bindings invalid after delete: %v", err)
	}
}

func TestDeleteDefaultAndOnlyBlockLastRemainingDesktop(t *testing.T) {
	first := claudeAccount{
		ID: strings.Repeat("a", 24), Source: "managed", Number: 1, Label: "账号一",
		Status: "ready", Enabled: true, Email: "one@example.org", Organization: "4491e880-4322-4cf7-8c34-c3ea9fc835e9",
		DesktopInstanceID: "default", DesktopStatus: "ready",
	}
	second := claudeAccount{
		ID: strings.Repeat("b", 24), Source: "managed", Number: 2, Label: "账号二",
		Status: "ready", Enabled: true, Email: "two@example.org", Organization: "68001639-9310-42a3-b5ce-29dfa6008f71",
		DesktopInstanceID: strings.Repeat("d", 24), DesktopStatus: "ready",
	}
	list := claudeAccountList{
		Accounts:      []claudeAccount{first, second},
		DesktopLabels: map[string]string{"default": "主力桌面", second.DesktopInstanceID: "第二桌面"},
	}
	// Two instances exist (default + second): default can be deleted.
	if !list.deleteDesktop("default") {
		t.Fatal("default desktop should be deletable when another instance exists")
	}
	if !list.DefaultDesktopRemoved || list.Accounts[0].DesktopInstanceID != "" || list.Accounts[0].DesktopStatus != "" || list.DesktopLabels["default"] != "" {
		t.Fatalf("default desktop delete left stale state: %+v", list)
	}
	if list.Accounts[0].Status != "ready" || !list.Accounts[0].Enabled {
		t.Fatalf("default desktop delete mutated account state: %+v", list.Accounts[0])
	}
	if _, err := list.renameDesktop("default", "复活"); err == nil {
		t.Fatal("removed default desktop must not be renamable")
	}
	if changed, _, _ := list.bindDesktop("default", claudeGuestSnapshot{LoggedIn: true, Email: first.Email, Organization: first.Organization}); changed {
		t.Fatal("removed default desktop must not bind observations")
	}
	// Second is now the sole remaining instance and cannot be deleted.
	if list.deleteDesktop(second.DesktopInstanceID) {
		t.Fatal("sole remaining isolated desktop must not be deletable")
	}
	// Adding a third instance makes both isolated instances deletable again.
	third, err := list.createDesktop("")
	if err != nil {
		t.Fatal(err)
	}
	if !list.deleteDesktop(second.DesktopInstanceID) {
		t.Fatal("second desktop should be deletable once third exists")
	}
	if list.deleteDesktop(third.ID) {
		t.Fatal("last remaining pending desktop must not be deletable")
	}
	if err := list.validateDesktopBindings(); err != nil {
		t.Fatalf("bindings invalid after default+isolated deletes: %v", err)
	}

	// Deleting a merged default's target while another desktop remains must not resurrect default.
	merged := claudeAccountList{
		Accounts: []claudeAccount{
			{ID: strings.Repeat("a", 24), DesktopInstanceID: strings.Repeat("1", 24)},
			{ID: strings.Repeat("b", 24), DesktopInstanceID: strings.Repeat("2", 24)},
		},
		DesktopRedirects: map[string]string{"default": strings.Repeat("1", 24)},
	}
	if !merged.deleteDesktop(strings.Repeat("1", 24)) {
		t.Fatal("expected merged target deletion to succeed when another desktop exists")
	}
	if !merged.DefaultDesktopRemoved || merged.hasDefaultDesktop() || merged.desktopCount() != 1 {
		t.Fatalf("merged default must stay removed rather than resurrecting: %+v", merged)
	}
	if merged.deleteDesktop(strings.Repeat("2", 24)) {
		t.Fatal("sole remaining desktop after merged target deletion must not be deletable")
	}
	if err := merged.validateDesktopBindings(); err != nil {
		t.Fatalf("merged bindings invalid: %v", err)
	}
}

func TestServerHasDefaultDesktopFollowsRemovalAndMerge(t *testing.T) {
	s := testServer(t)
	if !s.hasDefaultDesktop() {
		t.Fatal("a profile without account records has the default Desktop")
	}
	if err := s.saveClaudeAccounts(claudeAccountList{Accounts: []claudeAccount{}, DefaultDesktopRemoved: true}); err != nil {
		t.Fatal(err)
	}
	if s.hasDefaultDesktop() {
		t.Fatal("removed default Desktop reported as available")
	}
}
