package workbench

import (
	"bytes"
	"context"
	"encoding/base64"
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

	"github.com/google/uuid"
	"github.com/sylvinus/agent-vm/internal/mobilelink"
	"github.com/sylvinus/agent-vm/internal/relay"
	"github.com/sylvinus/agent-vm/internal/remotelink"
)

func TestMobileDesktopPhotoUploadLifecycle(t *testing.T) {
	edge, err := relay.New(relay.Config{PublicRooms: true, AdminToken: strings.Repeat("x", 32)})
	if err != nil {
		t.Fatal(err)
	}
	relayHTTP := httptest.NewServer(edge)
	t.Cleanup(func() { edge.Close(); relayHTTP.Close() })
	origin := relayHTTP.URL

	s, _ := grantedToolUpdateServer(t)
	s.preferences.MobileRelay = origin
	s.desktopDisplayUpdate = func(bool) error { return nil }

	var mu sync.Mutex
	var uploadedBytes []byte
	var guestOps []string
	var cancelledTransfers []string
	s.mobileDesktopGuestFiles = func(_ context.Context, method, path string, body io.Reader, name string) (map[string]any, error) {
		mu.Lock()
		defer mu.Unlock()
		guestOps = append(guestOps, method+" "+path)
		switch {
		case path == "/start":
			return map[string]any{"id": uuid.NewString()}, nil
		case strings.HasPrefix(path, "/upload?id="):
			data, err := io.ReadAll(body)
			if err != nil {
				return nil, err
			}
			uploadedBytes = data
			if name != "image.png" {
				t.Errorf("unexpected upload name %q", name)
			}
			return map[string]any{"ok": true}, nil
		case path == "/finish-image":
			return map[string]any{"ok": true}, nil
		case path == "/cancel":
			var req struct{ ID string }
			_ = json.NewDecoder(body).Decode(&req)
			cancelledTransfers = append(cancelledTransfers, req.ID)
			return map[string]any{"ok": true}, nil
		default:
			t.Errorf("unexpected guestFiles path %s", path)
			return nil, errors.New("unexpected")
		}
	}

	sessionID := uuid.NewString()
	s.browserControl = browserClientForTest(t, s, browserTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/phone-desktop/open" || r.URL.Path == "/phone-desktop/close" {
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"id":"` + sessionID + `"}`)),
			}, nil
		}
		if r.URL.Path == "/phone-desktop/frame" {
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(
					`{"id":"` + sessionID + `","document":"doc-1","width":390,"height":640,"image":"ZGVza3RvcA==","frame_tag":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","frame_tag_control":true,"quality_control":true,"quality":"standard","window":"desktop","shared":true}`,
				)),
			}, nil
		}
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	}))

	server := httptest.NewUnstartedServer(s.handler())
	s.origin = "http://" + server.Listener.Addr().String()
	server.Start()
	t.Cleanup(server.Close)
	t.Cleanup(s.stopPhone)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Pair a phone through the real encrypted relay tunnel.
	phone, err := remotelink.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	pairReq := httptest.NewRequest(http.MethodPost, s.origin+"/api/mobile-pair", strings.NewReader("{}"))
	pairReq.Header.Set("Origin", s.origin)
	pairReq.Header.Set("Content-Type", "application/json")
	pairReq.AddCookie(&http.Cookie{Name: s.cookieName(), Value: s.session})
	pairReq.AddCookie(&http.Cookie{Name: s.cookieName() + "-desktop", Value: trustedViewProof(s.token)})
	pairW := httptest.NewRecorder()
	s.handler().ServeHTTP(pairW, pairReq)
	var code struct{ Manual string }
	if pairW.Code != 200 || json.Unmarshal(pairW.Body.Bytes(), &code) != nil {
		t.Fatalf("pair code failed: %d", pairW.Code)
	}
	creds, err := mobilelink.RedeemPair(ctx, code.Manual, origin, phone.Pin)
	if err != nil {
		t.Fatal(err)
	}
	var record mobilelink.PairedHost
	if err := json.Unmarshal([]byte(creds), &record); err != nil {
		t.Fatal(err)
	}
	var client *mobilelink.Client
	for {
		room, err := mobilelink.LookupRoom(ctx, origin, record.Lookup)
		if err == nil {
			client, err = mobilelink.ConnectClient(ctx, origin, room.ID, room.ClientToken, phone, record.HostPin)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { client.Close() })
			break
		}
		if !errors.Is(err, mobilelink.ErrNoInvitation) || ctx.Err() != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Open the remote desktop session and verify capabilities advertised.
	reply, err := client.Do(ctx, http.MethodPost, "/api/mobile/desktop/open", []byte(`{"id":"`+sessionID+`","width":390,"height":640}`))
	if err != nil || reply.Status != 200 {
		t.Fatalf("desktop open failed: status=%v err=%v", reply, err)
	}
	frameReply, err := client.Do(ctx, http.MethodGet, "/api/mobile/desktop/"+sessionID+"/frame", nil)
	if err != nil || frameReply.Status != 200 {
		t.Fatalf("desktop frame failed: status=%v err=%v", frameReply, err)
	}
	var frameObj map[string]any
	if err := json.Unmarshal(frameReply.Body, &frameObj); err != nil {
		t.Fatal(err)
	}
	if frameObj["photo_upload_supported"] != true || frameObj["frame_tag_supported"] != true || frameObj["quality_supported"] != true {
		t.Fatalf("missing advertised capabilities: %v", frameObj)
	}

	// 1. Full chunked PNG upload with metadata: verify sanitization & host temp cleanup.
	rawPNG := testPNGWithMetadata(t)
	uploadPath := "/api/mobile/desktop/" + sessionID + "/upload"
	startBody, _ := json.Marshal(map[string]any{"op": "start", "size": len(rawPNG)})
	startReply, err := client.Do(ctx, http.MethodPost, uploadPath, startBody)
	if err != nil || startReply.Status != 200 {
		t.Fatalf("upload start failed: %v %v", startReply, err)
	}
	var started struct {
		UploadID  string `json:"upload_id"`
		ChunkSize int    `json:"chunk_size"`
	}
	if err := json.Unmarshal(startReply.Body, &started); err != nil || started.UploadID == "" || started.ChunkSize <= 0 {
		t.Fatalf("invalid start reply: %s", startReply.Body)
	}

	// Record host temp path so we can assert it is removed after finish.
	s.phones.desktop.op.Lock()
	tempPath := s.phones.desktop.session.uploads[started.UploadID].tempPath
	s.phones.desktop.op.Unlock()
	if _, err := os.Stat(tempPath); err != nil {
		t.Fatalf("expected staging file to exist during upload: %v", err)
	}

	// Send in 2 chunks to exercise multi-chunk offset checking.
	mid := len(rawPNG) / 2
	for _, part := range []struct {
		off  int
		data []byte
	}{{0, rawPNG[:mid]}, {mid, rawPNG[mid:]}} {
		chunkBody, _ := json.Marshal(map[string]any{
			"op":        "chunk",
			"upload_id": started.UploadID,
			"offset":    part.off,
			"data":      base64.StdEncoding.EncodeToString(part.data),
		})
		r, err := client.Do(ctx, http.MethodPost, uploadPath, chunkBody)
		if err != nil || r.Status != 200 {
			t.Fatalf("chunk at %d failed: %v %v", part.off, r, err)
		}
	}

	finishBody, _ := json.Marshal(map[string]any{"op": "finish", "upload_id": started.UploadID})
	finishReply, err := client.Do(ctx, http.MethodPost, uploadPath, finishBody)
	if err != nil || finishReply.Status != 200 {
		t.Fatalf("upload finish failed: %v %v", finishReply, err)
	}
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("staging temp file was not cleaned up after finish: %s", tempPath)
	}
	mu.Lock()
	gotBytes := append([]byte(nil), uploadedBytes...)
	mu.Unlock()
	if len(gotBytes) == 0 || bytes.Contains(gotBytes, []byte("GPSLatitude")) {
		t.Fatalf("uploaded PNG empty or retained GPS metadata")
	}

	// 2. Cancel mid-upload: verify host temp file deleted and guest /cancel called.
	startBody2, _ := json.Marshal(map[string]any{"op": "start", "size": 100})
	startReply2, err := client.Do(ctx, http.MethodPost, uploadPath, startBody2)
	if err != nil || startReply2.Status != 200 {
		t.Fatalf("second upload start failed: %v %v", startReply2, err)
	}
	var started2 struct {
		UploadID string `json:"upload_id"`
	}
	_ = json.Unmarshal(startReply2.Body, &started2)
	s.phones.desktop.op.Lock()
	tempPath2 := s.phones.desktop.session.uploads[started2.UploadID].tempPath
	s.phones.desktop.op.Unlock()

	cancelBody, _ := json.Marshal(map[string]any{"op": "cancel", "upload_id": started2.UploadID})
	cancelReply, err := client.Do(ctx, http.MethodPost, uploadPath, cancelBody)
	if err != nil || cancelReply.Status != 200 {
		t.Fatalf("upload cancel failed: %v %v", cancelReply, err)
	}
	if _, err := os.Stat(tempPath2); !os.IsNotExist(err) {
		t.Fatalf("staging temp file survived cancel: %s", tempPath2)
	}
	mu.Lock()
	cancelCount := len(cancelledTransfers)
	mu.Unlock()
	if cancelCount != 1 {
		t.Fatalf("expected 1 guest /cancel call, got %d", cancelCount)
	}
}
