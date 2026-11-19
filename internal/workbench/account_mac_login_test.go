package workbench

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A record written while the macOS guest existed carries its sealed login in
// mac_login. It must still load, and every later rewrite must keep the value
// byte for byte, whatever it contains.
func TestAccountRecordKeepsLegacyMacLoginUnread(t *testing.T) {
	a := newTestAccount(t)
	a.lock()
	data, err := os.ReadFile(a.path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]json.RawMessage
	if err = json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	legacy := json.RawMessage(`{"Nonce":"AAECAwQFBgcICQoL","Data":"c2VhbGVkIGJ5IGFuIGVhcmxpZXIgdmVyc2lvbg=="}`)
	record["mac_login"] = legacy
	if data, err = json.Marshal(record); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(a.path, data, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadLocalAccount(filepath.Dir(a.path))
	if err != nil {
		t.Fatal("legacy record no longer loads", err)
	}
	defer loaded.lock()
	if err = loaded.unlock("initial-test-password"); err != nil {
		t.Fatal(err)
	}
	if err = loaded.updateProfile("新的名称", ""); err != nil {
		t.Fatal(err)
	}
	if err = loaded.changePassword("initial-test-password", "replacement-test-password"); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(a.path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]json.RawMessage
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	var want, got bytes.Buffer
	if json.Compact(&want, legacy) != nil || json.Compact(&got, saved["mac_login"]) != nil || !bytes.Equal(want.Bytes(), got.Bytes()) {
		t.Fatalf("rewrite changed the legacy login: %s", saved["mac_login"])
	}
}
