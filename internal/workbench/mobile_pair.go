package workbench

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	qrcode "github.com/skip2/go-qrcode"
	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/keepawake"
	"github.com/sylvinus/agent-vm/internal/mobilelink"
	"github.com/sylvinus/agent-vm/internal/remotelink"
)

const maxPairedPhones = 8

// UI-managed mobile enrollments on this host. CLI-managed devices keep their separate identities.
type phoneRecord struct {
	ID       string                 `json:"id,omitempty"`
	Seed     []byte                 `json:"Seed"`
	Pin      string                 `json:"Pin"`
	Relay    string                 `json:"Relay"`
	Lookup   string                 `json:"Lookup"`
	Admin    string                 `json:"Admin"`
	Device   *mobilelink.DeviceInfo `json:"device,omitempty"`
	PairedAt time.Time              `json:"paired_at,omitempty"`
}

type phoneStore struct {
	Version int           `json:"version"`
	Devices []phoneRecord `json:"devices"`
}

type pairedPhoneView struct {
	ID       string                 `json:"id"`
	Device   *mobilelink.DeviceInfo `json:"device"`
	Relay    string                 `json:"relay,omitempty"`
	PairedAt time.Time              `json:"paired_at,omitempty"`
}

type phoneSlot struct {
	record    phoneRecord
	serving   context.CancelFunc
	roomProof string
	roomCtx   context.Context
	browser   phoneSurfaceState
}

func (slot *phoneSlot) peer() string {
	if slot == nil || len(slot.record.Seed) == 0 {
		return ""
	}
	digest := sha256.Sum256(slot.record.Seed)
	return hex.EncodeToString(digest[:])
}

type mobilePairing struct {
	mu          sync.Mutex
	desktop     phoneSurfaceState
	pending     context.CancelFunc
	slots       []*phoneSlot
	status      string
	closed      bool
	powerCtx    context.Context
	powerCancel context.CancelFunc
	powerDone   <-chan struct{}
	power       keepawake.State
}

func newPhoneID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func legacyPhoneID(seed []byte) string {
	h := sha256.New()
	h.Write([]byte("cage-phone-id:"))
	h.Write(seed)
	return hex.EncodeToString(h.Sum(nil)[:12])
}

func (s *Server) phoneFile() string { return filepath.Join(s.profile, "mobile-paired-device.json") }

func (p *mobilePairing) matchPhoneLocked(r *http.Request) *phoneSlot {
	if p.closed {
		return nil
	}
	proof := r.Header.Get(mobilelink.PhoneRoomHeader)
	if proof == "" {
		return nil
	}
	for _, slot := range p.slots {
		if slot.roomCtx != nil && slot.roomCtx.Err() == nil && slot.roomProof != "" && equalSecret(slot.roomProof, proof) {
			return slot
		}
	}
	return nil
}

func (p *mobilePairing) hasSlotLocked(target *phoneSlot) bool {
	for _, slot := range p.slots {
		if slot == target {
			return true
		}
	}
	return false
}

func (p *mobilePairing) recordsLocked() []phoneRecord {
	records := make([]phoneRecord, 0, len(p.slots))
	for _, slot := range p.slots {
		records = append(records, slot.record)
	}
	return records
}

func (s *Server) savePhones(records []phoneRecord) error {
	if len(records) == 0 {
		if err := os.Remove(s.phoneFile()); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return writePrivate(s.phoneFile(), phoneStore{Version: 2, Devices: records})
}

func parsePhoneRecords(b []byte) ([]phoneRecord, error) {
	var envelope struct {
		Devices json.RawMessage `json:"devices"`
	}
	var records []phoneRecord
	if json.Unmarshal(b, &envelope) == nil && len(envelope.Devices) > 0 {
		var store phoneStore
		if err := json.Unmarshal(b, &store); err != nil {
			return nil, errors.New("无法读取手机配对记录")
		}
		records = store.Devices
	} else {
		var single phoneRecord
		if err := json.Unmarshal(b, &single); err != nil {
			return nil, errors.New("无法读取手机配对记录")
		}
		records = []phoneRecord{single}
	}
	if len(records) > maxPairedPhones {
		return nil, errors.New("无法读取手机配对记录")
	}
	seenIDs := make(map[string]struct{}, len(records))
	seenLookups := make(map[string]struct{}, len(records))
	for i := range records {
		r := &records[i]
		if len(r.Seed) != ed25519.SeedSize || !mobilelink.ValidRelayOrigin(r.Relay) || r.Lookup == "" {
			return nil, errors.New("无法读取手机配对记录")
		}
		if r.ID == "" {
			r.ID = legacyPhoneID(r.Seed)
		}
		if _, dup := seenIDs[r.ID]; dup {
			return nil, errors.New("无法读取手机配对记录")
		}
		if _, dup := seenLookups[r.Lookup]; dup {
			return nil, errors.New("无法读取手机配对记录")
		}
		seenIDs[r.ID] = struct{}{}
		seenLookups[r.Lookup] = struct{}{}
	}
	return records, nil
}

// The transport proof grants only phone operations and is revoked with the
// active room. It never grants native desktop administration to a session cookie.
func (s *Server) pairedPhoneRoom(r *http.Request) context.Context {
	if r.URL.Path != "/api/mobile/device" && !strings.HasPrefix(r.URL.Path, "/api/mobile/cli/") && !strings.HasPrefix(r.URL.Path, "/api/mobile/browser/") && !strings.HasPrefix(r.URL.Path, "/api/mobile/desktop/") {
		return nil
	}
	s.phones.mu.Lock()
	defer s.phones.mu.Unlock()
	slot := s.phones.matchPhoneLocked(r)
	if slot == nil {
		return nil
	}
	return slot.roomCtx
}

func (s *Server) restorePhone() error {
	b, err := os.ReadFile(s.phoneFile())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	records, err := parsePhoneRecords(b)
	if err != nil {
		return err
	}
	s.phones.mu.Lock()
	defer s.phones.mu.Unlock()
	if s.phones.closed {
		return nil
	}
	for _, r := range records {
		s.attachPhoneLocked(r)
	}
	return nil
}

func (s *Server) ensurePowerLocked() {
	p := &s.phones
	if p.closed || p.powerCtx != nil || len(p.slots) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	p.powerCtx = ctx
	p.powerCancel = cancel
	p.powerDone = done
	go func() {
		defer close(done)
		keepawake.Run(ctx, func(state keepawake.State) {
			p.mu.Lock()
			if p.powerCtx == ctx {
				p.power = state
			}
			p.mu.Unlock()
			if state.Error != "" {
				diagnostics.Log("warn", "mobile-remote", "keep-awake request failed: "+state.Error)
			}
		})
	}()
}

func (s *Server) stopPowerLocked() <-chan struct{} {
	p := &s.phones
	if p.powerCancel != nil {
		p.powerCancel()
		p.powerCancel = nil
	}
	done := p.powerDone
	p.powerCtx = nil
	p.power = keepawake.State{}
	return done
}

func (s *Server) attachPhoneLocked(r phoneRecord) *phoneSlot {
	ctx, cancel := context.WithCancel(context.Background())
	slot := &phoneSlot{record: r, serving: cancel}
	s.phones.slots = append(s.phones.slots, slot)
	s.ensurePowerLocked()
	go func() {
		delay := time.Second
		for ctx.Err() == nil {
			start := time.Now()
			err := s.phoneRoom(ctx, slot)
			if ctx.Err() != nil {
				return
			}
			if err == nil || time.Since(start) > time.Minute {
				delay = time.Second
			} else {
				delay = min(30*time.Second, 2*delay)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
		}
	}()
	return slot
}

func (s *Server) phoneRoom(ctx context.Context, slot *phoneSlot) error {
	p := &s.phones
	p.mu.Lock()
	if ctx.Err() != nil || p.closed || !p.hasSlotLocked(slot) {
		p.mu.Unlock()
		return context.Canceled
	}
	seed := append([]byte(nil), slot.record.Seed...)
	relayOrigin, admin, lookup, pin := slot.record.Relay, slot.record.Admin, slot.record.Lookup, slot.record.Pin
	p.mu.Unlock()
	id, err := remotelink.IdentityFromKey(ed25519.NewKeyFromSeed(seed))
	if err != nil {
		return err
	}
	b, err := mobilelink.OpenWorkbench(ctx, s.origin, s.issueTicket())
	if err != nil {
		return err
	}
	defer b.Close()
	room, err := mobilelink.CreateDiscoverableRoom(ctx, relayOrigin, admin, lookup)
	if err != nil {
		return err
	}
	proof := randomToken()
	p.mu.Lock()
	if ctx.Err() != nil || p.closed || !p.hasSlotLocked(slot) {
		p.mu.Unlock()
		return context.Canceled
	}
	roomCtx, cancelRoom := context.WithCancel(ctx)
	slot.roomProof = proof
	slot.roomCtx = roomCtx
	p.mu.Unlock()
	defer func() {
		cancelRoom()
		p.mu.Lock()
		defer p.mu.Unlock()
		if slot.roomProof == proof {
			slot.roomProof = ""
			slot.roomCtx = nil
		}
	}()
	b.SetPhoneRoom(proof)
	return mobilelink.ServeHost(ctx, relayOrigin, room.ID, room.HostToken, id, pin, b)
}

func (s *Server) stopPhone() {
	p := &s.phones
	p.mu.Lock()
	p.closed = true
	if p.pending != nil {
		p.pending()
		p.pending = nil
	}
	for _, slot := range p.slots {
		slot.roomCtx = nil
		slot.roomProof = ""
		if slot.serving != nil {
			slot.serving()
			slot.serving = nil
		}
	}
	done := s.stopPowerLocked()
	p.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (p *mobilePairing) viewsLocked() []pairedPhoneView {
	devices := make([]pairedPhoneView, 0, len(p.slots))
	for _, slot := range p.slots {
		var info *mobilelink.DeviceInfo
		if slot.record.Device != nil {
			copyInfo := *slot.record.Device
			info = &copyInfo
		}
		devices = append(devices, pairedPhoneView{
			ID:       slot.record.ID,
			Device:   info,
			Relay:    slot.record.Relay,
			PairedAt: slot.record.PairedAt,
		})
	}
	return devices
}

func (s *Server) phoneStatus(w http.ResponseWriter, r *http.Request) {
	p := &s.phones
	p.mu.Lock()
	devices := p.viewsLocked()
	var firstDevice *mobilelink.DeviceInfo
	if len(devices) > 0 {
		firstDevice = devices[0].Device
	}
	paired, status, power := len(p.slots) > 0, p.status, p.power
	p.mu.Unlock()
	reply := map[string]any{
		"paired":      paired,
		"status":      status,
		"device":      firstDevice,
		"devices":     devices,
		"max_devices": maxPairedPhones,
		"keep_awake":  power,
	}
	if _, access := s.account.accessStateFor(false, true); access == accountAccessDesktopOnly {
		reply["mobile_access_blocked"] = true
		reply["mobile_access_reason"] = s.account.desktopOnlyMobileMessage()
	}
	jsonReply(w, reply)
}

// Only an active, pinned phone tunnel may describe its enrolled phone.
func (s *Server) reportPhoneDevice(w http.ResponseWriter, r *http.Request) {
	var input mobilelink.DeviceInfo
	if !readJSON(w, r, &input, 1024) {
		return
	}
	input.Model = strings.TrimSpace(input.Model)
	if (input.Platform != "android" && input.Platform != "ios") || input.Model == "" || len(input.Model) > 160 || !utf8.ValidString(input.Model) || strings.ContainsFunc(input.Model, unicode.IsControl) {
		http.Error(w, "手机型号无效", http.StatusBadRequest)
		return
	}
	s.account.mu.Lock()
	defer s.account.mu.Unlock()
	if !s.mobileAuthorized(r.Context()) {
		http.Error(w, "请先解锁 Cage", http.StatusLocked)
		return
	}
	p := &s.phones
	p.mu.Lock()
	defer p.mu.Unlock()
	slot := p.matchPhoneLocked(r)
	if slot == nil {
		http.Error(w, "请通过已配对的手机连接", http.StatusForbidden)
		return
	}
	if slot.record.Device == nil || *slot.record.Device != input {
		records := p.recordsLocked()
		for i := range records {
			if records[i].ID == slot.record.ID {
				info := input
				records[i].Device = &info
				break
			}
		}
		if err := s.savePhones(records); err != nil {
			requestError(w, err)
			return
		}
		info := input
		slot.record.Device = &info
	}
	jsonReply(w, map[string]bool{"ok": true})
}

func (s *Server) removePhone(w http.ResponseWriter, r *http.Request) {
	targetID := strings.TrimSpace(r.URL.Query().Get("id"))
	if r.Body != nil {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
		if err != nil || len(raw) > 4096 {
			http.Error(w, "请求无效", http.StatusBadRequest)
			return
		}
		raw = bytes.TrimSpace(raw)
		if len(raw) > 0 {
			var input struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				http.Error(w, "请求无效", http.StatusBadRequest)
				return
			}
			if targetID == "" {
				targetID = strings.TrimSpace(input.ID)
			}
		}
	}
	p := &s.phones
	p.mu.Lock()
	defer p.mu.Unlock()
	if targetID == "" {
		if p.pending != nil {
			p.pending()
			p.pending = nil
		}
		if err := s.savePhones(nil); err != nil {
			requestError(w, err)
			return
		}
		for _, slot := range p.slots {
			if slot.serving != nil {
				slot.serving()
				slot.serving = nil
			}
			slot.roomProof = ""
			slot.roomCtx = nil
		}
		p.slots = nil
		s.stopPowerLocked()
		p.status = ""
		jsonReply(w, map[string]any{"paired": false, "devices": []pairedPhoneView{}})
		return
	}
	idx := -1
	for i, slot := range p.slots {
		if slot.record.ID == targetID {
			idx = i
			break
		}
	}
	if idx < 0 {
		http.Error(w, "未找到已配对的设备", http.StatusNotFound)
		return
	}
	remaining := make([]phoneRecord, 0, len(p.slots)-1)
	for i, slot := range p.slots {
		if i != idx {
			remaining = append(remaining, slot.record)
		}
	}
	if err := s.savePhones(remaining); err != nil {
		requestError(w, err)
		return
	}
	removed := p.slots[idx]
	if removed.serving != nil {
		removed.serving()
		removed.serving = nil
	}
	removed.roomProof = ""
	removed.roomCtx = nil
	p.slots = append(p.slots[:idx], p.slots[idx+1:]...)
	if len(p.slots) == 0 {
		s.stopPowerLocked()
		p.status = ""
	}
	devices := p.viewsLocked()
	jsonReply(w, map[string]any{"paired": len(p.slots) > 0, "devices": devices})
}

func (s *Server) pairPhone(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Admin string `json:"admin"`
		Relay string `json:"relay"`
	}
	if !readJSON(w, r, &input, 8192) {
		return
	}
	epoch, ok := s.account.access()
	if !ok {
		http.Error(w, "请先解锁 Cage", 423)
		return
	}
	s.mu.Lock()
	origin := s.preferences.MobileRelay
	s.mu.Unlock()
	if origin == "" {
		origin = mobilelink.OfficialRelay
	}
	if input.Admin != "" && (origin == mobilelink.OfficialRelay || origin != input.Relay) {
		requestError(w, errors.New("请先保存自建中继地址"))
		return
	}
	p := &s.phones
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		http.Error(w, "服务正在关闭", http.StatusServiceUnavailable)
		return
	}
	if len(p.slots) >= maxPairedPhones {
		p.mu.Unlock()
		http.Error(w, "最多可同时配对 8 台设备，请先移除不需要的设备", http.StatusConflict)
		return
	}
	if p.pending != nil {
		p.pending()
	}
	ctx, cancel := context.WithTimeout(epoch, 2*time.Minute)
	p.pending = cancel
	p.status = "等待扫码"
	baselineDevices := p.viewsLocked()
	p.mu.Unlock()
	complete := false
	defer func() {
		if !complete {
			cancel()
		}
	}()
	room, err := mobilelink.CreateRoom(ctx, origin, input.Admin)
	if err != nil {
		requestError(w, errors.New("中继未能创建连接。请检查网络、自建地址和管理令牌。"))
		return
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		requestError(w, err)
		return
	}
	host, err := remotelink.IdentityFromKey(key)
	if err != nil {
		requestError(w, err)
		return
	}
	code, raw, err := mobilelink.NewPairCode(origin, room, host.Pin)
	if err != nil {
		requestError(w, err)
		return
	}
	png, err := qrcode.Encode(raw, qrcode.Medium, 384)
	if err != nil {
		requestError(w, errors.New("无法生成二维码"))
		return
	}
	complete = true
	go func() {
		defer cancel()
		err := mobilelink.AcceptPair(ctx, code, room.HostToken, host, func(pin string) (mobilelink.PairedHost, error) {
			// Commit trust in the same unlocked account epoch that issued the
			// code. A lock or logout must not race the on-disk enrollment.
			s.account.mu.Lock()
			defer s.account.mu.Unlock()
			p.mu.Lock()
			defer p.mu.Unlock()
			var result mobilelink.PairedHost
			if ctx.Err() != nil || p.closed {
				return result, errors.New("配对已结束")
			}
			existingIdx := -1
			for i, slot := range p.slots {
				if slot.record.Pin == pin {
					existingIdx = i
					break
				}
			}
			if existingIdx < 0 && len(p.slots) >= maxPairedPhones {
				return result, errors.New("最多可同时配对 8 台设备，请先移除不需要的设备")
			}
			var token [32]byte
			if _, err := rand.Read(token[:]); err != nil {
				return result, err
			}
			record := phoneRecord{
				ID:       newPhoneID(),
				Seed:     key.Seed(),
				Relay:    origin,
				Pin:      pin,
				Admin:    input.Admin,
				Lookup:   base64.RawURLEncoding.EncodeToString(token[:]),
				PairedAt: time.Now().UTC(),
			}
			if existingIdx >= 0 && p.slots[existingIdx].record.Device != nil {
				copyInfo := *p.slots[existingIdx].record.Device
				record.Device = &copyInfo
			}
			nextRecords := make([]phoneRecord, 0, len(p.slots)+1)
			for i, slot := range p.slots {
				if i != existingIdx {
					nextRecords = append(nextRecords, slot.record)
				}
			}
			nextRecords = append(nextRecords, record)
			if err := s.savePhones(nextRecords); err != nil {
				return result, err
			}
			if existingIdx >= 0 {
				old := p.slots[existingIdx]
				if old.serving != nil {
					old.serving()
					old.serving = nil
				}
				old.roomProof = ""
				old.roomCtx = nil
				p.slots = append(p.slots[:existingIdx], p.slots[existingIdx+1:]...)
			}
			s.attachPhoneLocked(record)
			p.status = "手机已配对"
			return mobilelink.PairedHost{Version: 1, Relay: origin, HostPin: host.Pin, Lookup: record.Lookup}, nil
		})
		p.mu.Lock()
		defer p.mu.Unlock()
		if err != nil && ctx.Err() == nil && p.status == "等待扫码" {
			p.status = "配对未完成，请重新生成二维码"
		}
	}()
	w.Header().Set("Cache-Control", "no-store")
	jsonReply(w, map[string]any{
		"image":   "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
		"expires": room.ExpiresAt,
		"manual":  mobilelink.ManualPairCodePrefix + base64.RawURLEncoding.EncodeToString([]byte(raw)),
		"devices": baselineDevices,
	})
}
