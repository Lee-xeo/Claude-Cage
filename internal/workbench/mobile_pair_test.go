package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sylvinus/agent-vm/internal/mobilelink"
	"github.com/sylvinus/agent-vm/internal/relay"
	"github.com/sylvinus/agent-vm/internal/remotelink"
)

// Reload the password account from disk so access comes from Remember me alone.
func resumeRememberedPhoneHost(t *testing.T, s *Server) {
	t.Helper()
	devices := t.TempDir()
	account, err := loadLocalAccount(s.profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := account.create("phone-test", "test-password", ""); err != nil {
		t.Fatal(err)
	}
	if err := account.trustedLogin("enable", "test-password", devices); err != nil {
		t.Fatal(err)
	}
	account.lock()
	s.account, err = loadLocalAccount(s.profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.account.trustedLogin("resume", "", devices); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.account.lock)
}

// Drive the same management routes as the desktop settings page. Separate
// workbench instances must recover the same on-disk pairing after a restart.
func TestDesktopPhonePairingLifecycle(t *testing.T) {
	edge, err := relay.New(relay.Config{PublicRooms: true, AdminToken: strings.Repeat("x", 32)})
	if err != nil {
		t.Fatal(err)
	}
	relayHTTP := httptest.NewServer(edge)
	t.Cleanup(func() { edge.Close(); relayHTTP.Close() })
	origin := relayHTTP.URL
	s, networkGate := grantedToolUpdateServer(t)
	serve := func(s *Server) {
		server := httptest.NewUnstartedServer(s.handler())
		s.origin = "http://" + server.Listener.Addr().String()
		server.Start()
		t.Cleanup(server.Close)
		t.Cleanup(s.stopPhone)
	}
	manage := func(s *Server, method string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, s.origin+"/api/mobile-pair", strings.NewReader("{}"))
		r.Header.Set("Origin", s.origin)
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: s.cookieName(), Value: s.session})
		r.AddCookie(&http.Cookie{Name: s.cookieName() + "-desktop", Value: trustedViewProof(s.token)})
		w := httptest.NewRecorder()
		s.handler().ServeHTTP(w, r)
		return w
	}
	s.preferences.MobileRelay = origin
	serve(s)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	phone, err := remotelink.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	w := manage(s, "POST")
	var code struct{ Manual, Image string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &code) != nil || !strings.HasPrefix(code.Manual, "CAGE-") || !strings.HasPrefix(code.Image, "data:image/png;base64,") {
		t.Fatalf("desktop pairing code unavailable (HTTP %d)", w.Code)
	}
	credentials, err := mobilelink.RedeemPair(ctx, code.Manual, origin, phone.Pin)
	if err != nil {
		t.Fatal(err)
	}
	var record mobilelink.PairedHost
	if err := json.Unmarshal([]byte(credentials), &record); err != nil {
		t.Fatal(err)
	}
	connect := func() *mobilelink.Client {
		t.Helper()
		for {
			room, err := mobilelink.LookupRoom(ctx, origin, record.Lookup)
			if err == nil {
				client, err := mobilelink.ConnectClient(ctx, origin, room.ID, room.ClientToken, phone, record.HostPin)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { client.Close() })
				return client
			}
			if !errors.Is(err, mobilelink.ErrNoInvitation) || ctx.Err() != nil {
				t.Fatal(err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	read := func(client *mobilelink.Client, status int) {
		t.Helper()
		reply, err := client.Do(ctx, "GET", "/api/mobile/cli/projects", nil)
		if err != nil {
			t.Fatal(err)
		}
		if reply.Status != status {
			t.Fatalf("projects: got %d, want %d", reply.Status, status)
		}
	}
	client := connect()
	read(client, 200)
	// Legacy pairings gain a model over the authenticated phone tunnel without
	// changing their key, lookup token, or the optical pairing protocol.
	if w := manage(s, "GET"); w.Code != 200 || !strings.Contains(w.Body.String(), `"device":null`) {
		t.Fatal("legacy pairing should have no invented model")
	}
	reply, err := client.Do(ctx, "POST", "/api/mobile/device", []byte(`{"platform":"android","model":"Google Pixel 9 Pro"}`))
	if err != nil || reply.Status != http.StatusOK {
		t.Fatalf("device report failed: %+v, %v", reply, err)
	}
	if w := manage(s, "GET"); w.Code != 200 || !strings.Contains(w.Body.String(), `"model":"Google Pixel 9 Pro"`) || strings.Contains(w.Body.String(), record.Lookup) || strings.Contains(w.Body.String(), phone.Pin) {
		t.Fatal("pairing status must expose model without credentials")
	}
	for _, input := range []string{`{"platform":"other","model":"Unknown"}`, `{"platform":"ios","model":""}`, `{"platform":"ios","model":"bad\nmodel"}`} {
		reply, err := client.Do(ctx, "POST", "/api/mobile/device", []byte(input))
		if err != nil || reply.Status != http.StatusBadRequest {
			t.Fatalf("invalid device report was accepted: %+v, %v", reply, err)
		}
	}
	// An authenticated desktop session is not the pinned phone tunnel.
	r := httptest.NewRequest("POST", s.origin+"/api/mobile/device", strings.NewReader(`{"platform":"ios","model":"other phone"}`))
	r.Header.Set("Origin", s.origin)
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: s.cookieName(), Value: s.session})
	w = httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("desktop session could overwrite phone model: %d", w.Code)
	}
	testSharedPhoneFavorites(t, ctx, s, client)
	testPairedBrowser(t, ctx, s, client, networkGate)
	read(client, 423)
	client.Close()
	s.phones.mu.Lock()
	powerCtx, powerDone := s.phones.powerCtx, s.phones.powerDone
	s.phones.mu.Unlock()
	if powerCtx == nil || powerCtx.Err() != nil {
		t.Fatal("phone disconnect stopped standby power request")
	}
	s.stopPhone()
	select {
	case <-powerDone:
	default:
		t.Fatal("host shutdown left power request running")
	}
	// Restore an old enrollment whose Desktop and browser switches were off.
	saved, err := os.ReadFile(s.phoneFile())
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(saved, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy["BrowserAllowed"] = json.RawMessage("false")
	legacy["DesktopAllowed"] = json.RawMessage("false")
	newHost, restoredGate := grantedToolUpdateServer(t)
	// This restored host uses a fake VM and guest frames, so no real X display
	// exists. Its display effect has the same boolean contract as the first host.
	newHost.desktopDisplayUpdate = func(bool) error { return nil }
	if err := writePrivate(newHost.phoneFile(), legacy); err != nil {
		t.Fatal(err)
	}
	resumeRememberedPhoneHost(t, newHost)
	serve(newHost)
	if err := newHost.restorePhone(); err != nil {
		t.Fatal(err)
	}
	if w := manage(newHost, "GET"); w.Code != 200 || !strings.Contains(w.Body.String(), `"model":"Google Pixel 9 Pro"`) {
		t.Fatal("phone model was lost after restarting the host")
	}
	client = connect()
	read(client, 200)
	newHost.phones.mu.Lock()
	proof := newHost.phones.slots[0].roomProof
	newHost.phones.mu.Unlock()
	for _, denied := range []struct{ path, proof string }{
		{"/api/mobile/cli/projects", ""},
		{"/api/mobile/cli/projects", "old-room"},
		{"/api/settings", proof},
		{"/api/mobile-pair", proof},
	} {
		if got := phoneRequestForTest(t, newHost, "GET", denied.path, "", denied.proof); got.Code != http.StatusLocked {
			t.Fatalf("untrusted access to %s: HTTP %d", denied.path, got.Code)
		}
	}
	newHost.browserControl = browserClientForTest(t, newHost, browserTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/phone-browser/action" {
			restoredGate.Close("network lost before action frame returned")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}, nil
	}))
	for _, surface := range []string{"browser", "desktop"} {
		reply, err := client.Do(ctx, "POST", "/api/mobile/"+surface+"/open", []byte(`{"id":"d872e7ef-e0ef-44b7-a256-38395cc6ee16","width":390,"height":640}`))
		if err != nil || reply.Status != 200 {
			t.Fatalf("restored %s session: %+v, %v", surface, reply, err)
		}
	}
	reply, err = client.Do(ctx, "POST", "/api/mobile/browser/d872e7ef-e0ef-44b7-a256-38395cc6ee16/action",
		[]byte(`{"type":"scroll","document":"test","x":0,"y":0,"dx":0,"dy":20,"return_frame":true}`))
	if err != nil || reply.Status != http.StatusConflict {
		t.Fatalf("network change must withhold attached frame: %+v, %v", reply, err)
	}
	if w := manage(newHost, "DELETE"); w.Code != 200 {
		t.Fatalf("revoke: HTTP %d", w.Code)
	}
	newHost.phones.mu.Lock()
	removedPowerDone := newHost.phones.powerDone
	newHost.phones.mu.Unlock()
	select {
	case <-removedPowerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("unpairing left power request running")
	}
	if _, err := os.Stat(newHost.phoneFile()); !os.IsNotExist(err) {
		t.Fatal("revoked credentials still on disk")
	}
	if w := manage(newHost, "GET"); !strings.Contains(w.Body.String(), `"device":null`) || strings.Contains(w.Body.String(), "Pixel") {
		t.Fatal("removed phone model remains in pairing status")
	}
	for {
		_, err := client.Do(ctx, "GET", "/api/mobile/cli/projects", nil)
		if err != nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("active phone retained access after revocation")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := mobilelink.LookupRoom(ctx, origin, record.Lookup); !errors.Is(err, mobilelink.ErrNoInvitation) {
		t.Fatalf("revoked lookup %v", err)
	}
}

func TestDesktopMultiplePhonePairingLifecycle(t *testing.T) {
	edge, err := relay.New(relay.Config{PublicRooms: true, AdminToken: strings.Repeat("y", 32)})
	if err != nil {
		t.Fatal(err)
	}
	relayHTTP := httptest.NewServer(edge)
	t.Cleanup(func() { edge.Close(); relayHTTP.Close() })
	origin := relayHTTP.URL

	serve := func(s *Server) {
		server := httptest.NewUnstartedServer(s.handler())
		s.origin = "http://" + server.Listener.Addr().String()
		server.Start()
		t.Cleanup(server.Close)
		t.Cleanup(s.stopPhone)
	}
	manage := func(s *Server, method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, s.origin+"/api/mobile-pair", strings.NewReader(body))
		r.Header.Set("Origin", s.origin)
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: s.cookieName(), Value: s.session})
		r.AddCookie(&http.Cookie{Name: s.cookieName() + "-desktop", Value: trustedViewProof(s.token)})
		w := httptest.NewRecorder()
		s.handler().ServeHTTP(w, r)
		return w
	}

	s, _ := grantedToolUpdateServer(t)
	s.preferences.MobileRelay = origin
	var profileMu sync.Mutex
	var openedProfiles []string
	s.browserControl = browserClientForTest(t, s, browserTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/phone-browser/open" {
			var in phoneSurfaceInput
			_ = json.NewDecoder(r.Body).Decode(&in)
			profileMu.Lock()
			openedProfiles = append(openedProfiles, in.Profile)
			profileMu.Unlock()
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"ok"}`)), Header: http.Header{}}, nil
	}))
	serve(s)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	pair := func(phone *remotelink.Identity) mobilelink.PairedHost {
		t.Helper()
		w := manage(s, "POST", "{}")
		var code struct{ Manual string }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &code) != nil {
			t.Fatalf("pair code failed: HTTP %d %s", w.Code, w.Body.String())
		}
		raw, err := mobilelink.RedeemPair(ctx, code.Manual, origin, phone.Pin)
		if err != nil {
			t.Fatal(err)
		}
		var rec mobilelink.PairedHost
		if err := json.Unmarshal([]byte(raw), &rec); err != nil {
			t.Fatal(err)
		}
		return rec
	}
	connect := func(phone *remotelink.Identity, rec mobilelink.PairedHost) *mobilelink.Client {
		t.Helper()
		for {
			room, err := mobilelink.LookupRoom(ctx, origin, rec.Lookup)
			if err == nil {
				client, err := mobilelink.ConnectClient(ctx, origin, room.ID, room.ClientToken, phone, rec.HostPin)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { client.Close() })
				return client
			}
			if !errors.Is(err, mobilelink.ErrNoInvitation) || ctx.Err() != nil {
				t.Fatal(err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	phone1, err := remotelink.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	phone2, err := remotelink.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}

	rec1 := pair(phone1)
	rec2 := pair(phone2)
	client1 := connect(phone1, rec1)
	client2 := connect(phone2, rec2)

	for i, client := range []*mobilelink.Client{client1, client2} {
		reply, err := client.Do(ctx, "GET", "/api/mobile/cli/projects", nil)
		if err != nil || reply.Status != 200 {
			t.Fatalf("device %d projects: %+v %v", i+1, reply, err)
		}
	}

	if reply, err := client1.Do(ctx, "POST", "/api/mobile/device", []byte(`{"platform":"ios","model":"iPhone Air"}`)); err != nil || reply.Status != 200 {
		t.Fatalf("device 1 report: %+v %v", reply, err)
	}
	if reply, err := client2.Do(ctx, "POST", "/api/mobile/device", []byte(`{"platform":"ios","model":"iPad Pro"}`)); err != nil || reply.Status != 200 {
		t.Fatalf("device 2 report: %+v %v", reply, err)
	}

	var status struct {
		Paired     bool              `json:"paired"`
		MaxDevices int               `json:"max_devices"`
		Devices    []pairedPhoneView `json:"devices"`
	}
	w := manage(s, "GET", "")
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &status) != nil {
		t.Fatalf("status failed: %d %s", w.Code, w.Body.String())
	}
	if !status.Paired || status.MaxDevices != maxPairedPhones || len(status.Devices) != 2 {
		t.Fatalf("unexpected multi-device status: %+v", status)
	}
	if status.Devices[0].Device == nil || status.Devices[0].Device.Model != "iPhone Air" ||
		status.Devices[1].Device == nil || status.Devices[1].Device.Model != "iPad Pro" {
		t.Fatalf("device models not preserved independently: %+v", status.Devices)
	}
	if status.Devices[0].ID == "" || status.Devices[1].ID == "" || status.Devices[0].ID == status.Devices[1].ID {
		t.Fatalf("expected distinct non-empty device IDs: %+v", status.Devices)
	}
	for _, secret := range []string{rec1.Lookup, rec2.Lookup, phone1.Pin, phone2.Pin} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("pairing status leaked device secret")
		}
	}

	// Both devices open independent remote browser sessions with separate guest profiles.
	if reply, err := client1.Do(ctx, "POST", "/api/mobile/browser/open", []byte(`{"id":"11111111-1111-4111-8111-111111111111","width":390,"height":640}`)); err != nil || reply.Status != 200 {
		t.Fatalf("device 1 browser open: %+v %v", reply, err)
	}
	if reply, err := client2.Do(ctx, "POST", "/api/mobile/browser/open", []byte(`{"id":"22222222-2222-4222-8222-222222222222","width":820,"height":1180}`)); err != nil || reply.Status != 200 {
		t.Fatalf("device 2 browser open: %+v %v", reply, err)
	}
	profileMu.Lock()
	profiles := append([]string(nil), openedProfiles...)
	profileMu.Unlock()
	if len(profiles) != 2 || profiles[0] == "" || profiles[1] == "" || profiles[0] == profiles[1] {
		t.Fatalf("expected distinct browser profiles per device, got %v", profiles)
	}

	// Re-pairing the same physical device replaces its slot rather than creating a duplicate.
	rec1Again := pair(phone1)
	client1.Close()
	client1 = connect(phone1, rec1Again)
	w = manage(s, "GET", "")
	if json.Unmarshal(w.Body.Bytes(), &status) != nil || len(status.Devices) != 2 {
		t.Fatalf("re-pairing same pin duplicated slot: %s", w.Body.String())
	}

	// Restarting the host under Remember-Me restores both devices and grants both access.
	savedBytes, err := os.ReadFile(s.phoneFile())
	if err != nil {
		t.Fatal(err)
	}
	client1.Close()
	client2.Close()
	s.stopPhone()

	restarted, _ := grantedToolUpdateServer(t)
	if err := os.WriteFile(restarted.phoneFile(), savedBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	resumeRememberedPhoneHost(t, restarted)
	serve(restarted)
	if err := restarted.restorePhone(); err != nil {
		t.Fatal(err)
	}
	client1 = connect(phone1, rec1Again)
	client2 = connect(phone2, rec2)
	for i, client := range []*mobilelink.Client{client1, client2} {
		reply, err := client.Do(ctx, "GET", "/api/mobile/cli/projects", nil)
		if err != nil || reply.Status != 200 {
			t.Fatalf("restored remembered device %d projects: %+v %v", i+1, reply, err)
		}
	}

	// Removing an unknown ID returns 404 without disturbing paired devices.
	if w := manage(restarted, "DELETE", `{"id":"nonexistent"}`); w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown device id, got %d", w.Code)
	}

	// Removing only device 1 revokes device 1 while keeping device 2 and keep-awake alive.
	w = manage(restarted, "GET", "")
	if json.Unmarshal(w.Body.Bytes(), &status) != nil || len(status.Devices) != 2 {
		t.Fatalf("unexpected devices before partial remove: %s", w.Body.String())
	}
	var id1, id2 string
	for _, d := range status.Devices {
		if d.Device != nil && d.Device.Model == "iPhone Air" {
			id1 = d.ID
		} else if d.Device != nil && d.Device.Model == "iPad Pro" {
			id2 = d.ID
		}
	}
	if id1 == "" || id2 == "" {
		t.Fatalf("could not identify both device IDs: %+v", status.Devices)
	}
	if w := manage(restarted, "DELETE", `{"id":"`+id1+`"}`); w.Code != 200 {
		t.Fatalf("remove device 1 failed: %d %s", w.Code, w.Body.String())
	}
	for {
		_, err := client1.Do(ctx, "GET", "/api/mobile/cli/projects", nil)
		if err != nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("revoked device 1 retained access")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if reply, err := client2.Do(ctx, "GET", "/api/mobile/cli/projects", nil); err != nil || reply.Status != 200 {
		t.Fatalf("device 2 must remain usable after removing device 1: %+v %v", reply, err)
	}
	restarted.phones.mu.Lock()
	powerAlive := restarted.phones.powerCtx != nil && restarted.phones.powerCtx.Err() == nil
	restarted.phones.mu.Unlock()
	if !powerAlive {
		t.Fatal("keep-awake stopped while device 2 is still paired")
	}

	// Removing the last remaining device removes the file and stops keep-awake.
	restarted.phones.mu.Lock()
	lastPowerDone := restarted.phones.powerDone
	restarted.phones.mu.Unlock()
	if w := manage(restarted, "DELETE", `{"id":"`+id2+`"}`); w.Code != 200 {
		t.Fatalf("remove device 2 failed: %d %s", w.Code, w.Body.String())
	}
	select {
	case <-lastPowerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("removing last device left keep-awake running")
	}
	if _, err := os.Stat(restarted.phoneFile()); !os.IsNotExist(err) {
		t.Fatal("phone file must be deleted after removing last device")
	}
}

func TestRestoreLegacySinglePhoneRecord(t *testing.T) {
	legacy := map[string]any{
		"Seed":   make([]byte, 32),
		"Pin":    "sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
		"Relay":  "https://relay.example.com",
		"Lookup": "legacy-lookup-token",
		"Admin":  "",
		"device": map[string]string{"platform": "ios", "model": "iPhone Legacy"},
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	records, err := parsePhoneRecords(raw)
	if err != nil {
		t.Fatalf("failed to parse legacy single record: %v", err)
	}
	if len(records) != 1 || records[0].ID == "" || records[0].Device == nil || records[0].Device.Model != "iPhone Legacy" {
		t.Fatalf("unexpected parsed legacy record: %+v", records)
	}
	// Deterministic legacy ID stays stable across reloads.
	records2, err := parsePhoneRecords(raw)
	if err != nil || records2[0].ID != records[0].ID {
		t.Fatalf("legacy ID not deterministic: %+v vs %+v", records, records2)
	}
}
