package workbench

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

type accountRecovery struct {
	Nonce     []byte `json:"nonce"`
	SealedKey []byte `json:"sealed_key"`
}

const accountRecoveryDomain = "Account recovery v1"

// The recovery key has 256 bits of randomness. Only its encrypted wrapping of
// the existing data key is persisted; the printable secret is returned once.
func newAccountRecovery(record *accountRecord, key []byte) (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	defer clear(secret)
	aead, err := deviceAEAD(secret)
	if err != nil {
		return "", err
	}
	recovery := &accountRecovery{Nonce: make([]byte, aead.NonceSize())}
	if _, err = rand.Read(recovery.Nonce); err != nil {
		return "", err
	}
	recovery.SealedKey = aead.Seal(nil, recovery.Nonce, key, []byte(accountRecoveryDomain))
	record.Recovery = recovery
	raw := strings.ToUpper(hex.EncodeToString(secret))
	groups := make([]string, 0, 8)
	for i := 0; i < len(raw); i += 8 {
		groups = append(groups, raw[i:i+8])
	}
	return strings.Join(groups, "-"), nil
}

func (a *localAccount) generateRecovery(password string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.record == nil || len(a.key) != 32 {
		return "", errors.New("请先解锁 Cage")
	}
	if time.Now().Before(a.nextAttempt) {
		return "", errors.New("请稍后再试")
	}
	key, err := openAccountKey(a.record, password)
	if err != nil {
		a.nextAttempt = time.Now().Add(time.Second)
		return "", err
	}
	defer clear(key)
	record := *a.record
	code, err := newAccountRecovery(&record, key)
	if err != nil {
		return "", err
	}
	if err = writePrivate(a.path, &record); err != nil {
		return "", errors.New("恢复密钥未能保存，原有恢复方式保持有效")
	}
	a.record = &record
	a.nextAttempt = time.Time{}
	return code, nil
}

func (a *localAccount) resetPassword(code, password string) (string, error) {
	if err := validAccountPassword(password); err != nil {
		return "", err
	}
	a.macSync.Lock()
	defer a.macSync.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if time.Now().Before(a.nextAttempt) {
		return "", errors.New("请稍后再试")
	}
	var key []byte
	// Accept formatting whitespace, hyphens and either case, without accepting
	// shortened keys or guessing characters. Bound work before normalization.
	if a.record != nil && a.record.Recovery != nil && len(code) <= 256 {
		normalized := strings.NewReplacer("-", "", " ", "", "\r", "", "\n", "", "\t", "").Replace(code)
		secret, err := hex.DecodeString(normalized)
		if err == nil && len(secret) == 32 {
			aead, _ := deviceAEAD(secret)
			key, _ = aead.Open(nil, a.record.Recovery.Nonce, a.record.Recovery.SealedKey, []byte(accountRecoveryDomain))
		}
		clear(secret)
	}
	if len(key) != 32 {
		a.nextAttempt = time.Now().Add(time.Second)
		return "", errors.New("恢复密钥不正确或已失效，请核对保存的最新密钥")
	}
	defer clear(key)
	record, err := a.passwordRecord(key, password, true)
	if err != nil {
		return "", err
	}
	next, err := newAccountRecovery(record, key)
	if err != nil {
		return "", err
	}
	// Revoke application unlock grants, while preserving the data key used by
	// website credentials, desktop keyrings and all existing personal data.
	record.TrustedUnlock = nil
	record.TrustedDevice = ""
	record.Devices = nil
	record.RequireAuthentication = true
	if err = writePrivate(a.path, record); err != nil {
		return "", errors.New("新密码未能保存，原密码与恢复密钥仍然有效")
	}
	a.record = record
	a.nextAttempt = time.Time{}
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}
	clear(a.key)
	a.key = nil
	a.trustedSession = false
	return next, nil
}
