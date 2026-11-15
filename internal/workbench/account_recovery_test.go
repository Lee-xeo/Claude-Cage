package workbench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRecoveryResetPreservesDataRevokesGrantsAndRotatesSecret(t *testing.T) {
	a := newTestAccount(t)
	original := append([]byte(nil), a.key...)
	defer clear(original)
	code, err := a.generateRecovery("initial-test-password")
	if err != nil {
		t.Fatal(err)
	}
	device, err := a.enrollDevice("biometric")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.trustedLogin("enable", "initial-test-password", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err = a.withVault(context.Background(), true, func(db *vaultData) error {
		return db.save(vaultEntry{Origin: "https://example.com", Username: "alice", Password: "existing-login-secret"})
	}); err != nil {
		t.Fatal(err)
	}
	vaultPath := filepath.Join(filepath.Dir(a.path), "password-vault.json")
	vaultBefore, _ := os.ReadFile(vaultPath)
	// Representative unrelated personal data must remain byte-for-byte intact.
	dataPath := filepath.Join(filepath.Dir(a.path), "personal-disk.fixture")
	if err = os.WriteFile(dataPath, []byte("personal files and login state"), 0600); err != nil {
		t.Fatal(err)
	}
	epoch, _ := a.access()
	a.lock()
	a, err = loadLocalAccount(filepath.Dir(a.path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.lock)
	if !a.view().RecoveryReady {
		t.Fatal("recovery enrollment lost on reload")
	}
	formatted := strings.ToLower(strings.ReplaceAll(code, "-", " \n"))
	next, err := a.resetPassword(formatted, "recovered-test-password")
	if err != nil || next == code || len(next) != 71 {
		t.Fatal("recovery reset failed", err)
	}
	if a.view().Unlocked || !a.view().RequireAuthentication || a.view().TrustedLogin || len(a.view().Devices) != 0 {
		t.Fatal("recovery left application unlock grants active")
	}
	if epoch.Err() == nil {
		t.Fatal("old access epoch not cancelled")
	}
	if _, err = a.resetPassword(code, "third-test-password"); err == nil {
		t.Fatal("used recovery key accepted")
	}
	a.nextAttempt = time.Time{}
	if err = a.unlock("initial-test-password"); err == nil {
		t.Fatal("old password accepted")
	}
	a.nextAttempt = time.Time{}
	if err = a.unlockDevice("biometric", device); err == nil {
		t.Fatal("old biometric grant accepted")
	}
	if err = a.unlock("recovered-test-password"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, a.key) {
		t.Fatal("data key changed, existing login access lost")
	}
	if err = a.withVault(context.Background(), false, func(db *vaultData) error {
		if len(db.Entries) != 1 || db.Entries[0].Password != "existing-login-secret" {
			t.Fatal("vault data lost")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	vaultAfter, _ := os.ReadFile(vaultPath)
	data, _ := os.ReadFile(dataPath)
	if !bytes.Equal(vaultBefore, vaultAfter) || string(data) != "personal files and login state" {
		t.Fatal("personal data rewritten")
	}
	raw, _ := os.ReadFile(a.path)
	for _, secret := range []string{code, next, strings.ReplaceAll(next, "-", ""), "recovered-test-password"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("plaintext recovery secret or password persisted")
		}
	}
	a.lock()
	reloaded, err := loadLocalAccount(filepath.Dir(a.path))
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.lock()
	if _, err = reloaded.resetPassword(next, "third-test-password"); err != nil {
		t.Fatal("replacement recovery key failed after reload", err)
	}
}

func TestRecoveryEnrollmentLegacyValidationAndWriteFailure(t *testing.T) {
	a := newTestAccount(t)
	// A version-1 account created before this feature has no recovery field.
	a.record.Recovery = nil
	if err := writePrivate(a.path, a.record); err != nil {
		t.Fatal(err)
	}
	if a.view().RecoveryReady {
		t.Fatal("legacy account reports enrollment")
	}
	if _, err := a.generateRecovery("wrong-password"); err == nil {
		t.Fatal("enrollment without password proof")
	}
	a.nextAttempt = time.Time{}
	code, err := a.generateRecovery("initial-test-password")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(a.path)
	for _, invalid := range []string{"", "12345678", strings.Repeat("f", 64), strings.Repeat("a", 300)} {
		a.nextAttempt = time.Time{}
		if _, err = a.resetPassword(invalid, "new-test-password"); err == nil {
			t.Fatal("invalid recovery accepted")
		}
		after, _ := os.ReadFile(a.path)
		if !bytes.Equal(before, after) {
			t.Fatal("invalid recovery mutated account")
		}
	}
	a.nextAttempt = time.Time{}
	if _, err = a.resetPassword(code, "short"); err == nil {
		t.Fatal("weak replacement password accepted")
	}
	path := a.path
	a.path = t.TempDir() // rename over a directory must fail, without touching disk data.
	if _, err = a.generateRecovery("initial-test-password"); err == nil {
		t.Fatal("enrollment save unexpectedly succeeded")
	}
	if _, err = a.resetPassword(code, "new-test-password"); err == nil {
		t.Fatal("reset save unexpectedly succeeded")
	}
	a.path = path
	if !a.view().Unlocked {
		t.Fatal("failed reset locked existing session")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("failed write replaced existing account")
	}
	if _, err = a.resetPassword(code, "new-test-password"); err != nil {
		t.Fatal("failed write consumed recovery key", err)
	}
}

func TestRecoverySurvivesOrdinaryPasswordChange(t *testing.T) {
	a := newTestAccount(t)
	code, err := a.generateRecovery("initial-test-password")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.changePassword("initial-test-password", "changed-test-password"); err != nil {
		t.Fatal(err)
	}
	a.lock()
	if _, err = a.resetPassword(code, "recovered-test-password"); err != nil {
		t.Fatal("ordinary password change invalidated the recovery key", err)
	}
}

func TestRecoveryHTTPRegistrationEnrollmentAndLockedReset(t *testing.T) {
	s := testServer(t)
	a, err := loadLocalAccount(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer a.lock()
	s.account = a
	w := request(s, "POST", "/api/account/create", s.origin, `{"username":"recovery_qa","password":"original-test-password","confirm_password":"original-test-password"}`, true)
	var created struct {
		accountView
		RecoveryKey string `json:"recovery_key"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &created) != nil || len(created.RecoveryKey) != 71 || !created.RecoveryReady {
		t.Fatal("registration missing recovery", w.Code)
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("secret response cacheable")
	}
	w = request(s, "GET", "/api/account", "", "", true)
	if strings.Contains(w.Body.String(), "recovery_key") || strings.Contains(w.Body.String(), created.RecoveryKey) {
		t.Fatal("public status exposes recovery secret")
	}
	for _, tc := range []struct {
		origin  string
		session bool
		status  int
	}{{s.origin, false, 401}, {"https://evil.example", true, 403}} {
		w = request(s, "POST", "/api/account/recovery-reset", tc.origin, `{}`, tc.session)
		if w.Code != tc.status {
			t.Fatal("recovery bypassed session/origin boundary", w.Code)
		}
	}
	w = request(s, "POST", "/api/account/recovery-generate", s.origin, `{"password":"original-test-password"}`, true)
	var generated struct {
		RecoveryKey string `json:"recovery_key"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &generated) != nil || generated.RecoveryKey == created.RecoveryKey {
		t.Fatal("enrollment failed", w.Code)
	}
	a.lock()
	if w = request(s, "POST", "/api/account/recovery-generate", s.origin, `{"password":"original-test-password"}`, true); w.Code != http.StatusLocked {
		t.Fatal("locked enrollment accepted", w.Code)
	}
	body, _ := json.Marshal(map[string]string{"recovery_key": generated.RecoveryKey, "password": "recovered-test-password", "confirm_password": "different-password"})
	if w = request(s, "POST", "/api/account/recovery-reset", s.origin, string(body), true); w.Code != 400 {
		t.Fatal("mismatched confirmation accepted")
	}
	body, _ = json.Marshal(map[string]string{"recovery_key": generated.RecoveryKey, "password": "recovered-test-password", "confirm_password": "recovered-test-password"})
	w = request(s, "POST", "/api/account/recovery-reset", s.origin, string(body), true)
	if w.Code != 200 || a.view().Unlocked {
		t.Fatal("locked reset failed or unlocked workspace", w.Code)
	}
	if w = request(s, "GET", "/api/settings", "", "", true); w.Code != 423 {
		t.Fatal("reset granted a general session")
	}
	if w = request(s, "POST", "/api/account/unlock", s.origin, `{"password":"recovered-test-password"}`, true); w.Code != 200 {
		t.Fatal("new password did not unlock", w.Code)
	}
}

func TestRecoveryMacInterruptedSyncRetainsBothPasswordSteps(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "synced", true: "interrupted"}[pending], func(t *testing.T) {
			a := newSharedMacAccount(t)
			id := uuid.New()
			if _, err := a.macGuestCredentials(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			code, err := a.generateRecovery("initial-test-password")
			if err != nil {
				t.Fatal(err)
			}
			if pending {
				if err = a.changePassword("initial-test-password", "intermediate-test-password"); err != nil {
					t.Fatal(err)
				}
			}
			a.lock()
			next, err := a.resetPassword(code, "recovered-test-password")
			if err != nil {
				t.Fatal(err)
			}
			// Repeated recovery before a sync must preserve the original pending target.
			if _, err = a.resetPassword(next, "final-test-password"); err != nil {
				t.Fatal(err)
			}
			if err = a.unlock("final-test-password"); err != nil {
				t.Fatal(err)
			}
			if !a.view().MacOSPasswordPending {
				t.Fatal("missing pending guest password")
			}
			calls := 0
			lostReply := true
			apply := func(_ context.Context, old, target string) error {
				calls++
				if old != "initial-test-password" {
					t.Fatal("original guest credential lost")
				}
				if pending && target != "intermediate-test-password" || !pending && target != "recovered-test-password" {
					t.Fatal("interrupted target lost")
				}
				if lostReply {
					return errors.New("reply lost after guest changed")
				}
				return nil
			}
			if err = a.syncMacPassword(context.Background(), id, "shihao", apply); err == nil {
				t.Fatal("lost reply reported success")
			}
			profile := filepath.Dir(a.path)
			a.lock()
			a, err = loadLocalAccount(profile)
			if err != nil {
				t.Fatal(err)
			}
			defer a.lock()
			if err = a.unlock("final-test-password"); err != nil {
				t.Fatal(err)
			}
			lostReply = false
			if err = a.syncMacPassword(context.Background(), id, "shihao", func(ctx context.Context, old, target string) error {
				if target != "final-test-password" {
					return apply(ctx, old, target)
				}
				if pending && old != "intermediate-test-password" || !pending && old != "recovered-test-password" {
					t.Fatal("second sync step lost confirmed credential")
				}
				calls++
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if calls != 3 || a.view().MacOSPasswordPending {
				t.Fatal("recovery sync did not finish", calls)
			}
		})
	}
}
