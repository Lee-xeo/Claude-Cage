package workbench

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestAudioInputBoundary(t *testing.T) {
	s := testServer(t)
	if w := request(s, "GET", "/api/audio/input", "", "", false); w.Code != 401 {
		t.Fatal("audio input allowed without session", w.Code)
	}
	s.account.lock()
	if w := request(s, "GET", "/api/audio/input", "", "", true); w.Code != 423 {
		t.Fatal("audio input allowed while locked", w.Code)
	}
	r := httptest.NewRequest("GET", s.origin+"/api/audio/input", nil)
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Origin", "https://other.example")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin audio input allowed", w.Code)
	}
}

func TestAudioInputDemandGatesPCMAndClosesOnCancellation(t *testing.T) {
	s := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	guestOutR, guestOutW := io.Pipe()
	guestInR, guestInW := io.Pipe()
	defer guestOutR.Close()
	defer guestOutW.Close()
	defer guestInR.Close()
	defer guestInW.Close()

	finished := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		s.streamAudioInput(w, r.WithContext(ctx), guestOutR, guestInW)
	}))
	defer server.Close()
	s.origin = server.URL

	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), http.Header{"Origin": {server.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	pong := make(chan struct{}, 1)
	ws.SetPongHandler(func(string) error {
		select {
		case pong <- struct{}{}:
		default:
		}
		return nil
	})
	type wsReply struct {
		data []byte
		err  error
	}
	replies := make(chan wsReply, 8)
	go func() {
		for {
			_, data, readErr := ws.ReadMessage()
			replies <- wsReply{data: data, err: readErr}
			if readErr != nil {
				return
			}
		}
	}()
	readReply := func() []byte {
		t.Helper()
		select {
		case msg := <-replies:
			if msg.err != nil {
				t.Fatal(msg.err)
			}
			return msg.data
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for websocket message")
			return nil
		}
	}

	// Handshake announces format and initial demand=false.
	rawHandshake := readReply()
	var handshake audioInputHandshake
	if json.Unmarshal(rawHandshake, &handshake) != nil || handshake.Format != "s16le" || handshake.SampleRate != 48000 || handshake.Channels != 1 || handshake.Demand {
		t.Fatalf("unexpected handshake: %s", rawHandshake)
	}

	// PCM sent while demand=false is dropped and never reaches guestStdin.
	// Synchronize with a WebSocket ping/pong so the server read loop has
	// consumed and discarded the frame before demand flips to true.
	dropped := []byte{1, 2, 3, 4}
	if err := ws.WriteMessage(websocket.BinaryMessage, dropped); err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteControl(websocket.PingMessage, []byte("sync"), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pong:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not process pre-demand frame before ping")
	}

	// Activate demand from guest stdout.
	go func() {
		_, _ = io.WriteString(guestOutW, "{\"demand\":true}\n")
	}()
	rawDemand := readReply()
	var demandMsg audioInputDemand
	if json.Unmarshal(rawDemand, &demandMsg) != nil || !demandMsg.Demand {
		t.Fatalf("expected demand=true, got %s", rawDemand)
	}

	// PCM sent while demand=true arrives framed with uint16 big-endian length.
	pcm := bytes.Repeat([]byte{0x34, 0x12}, 960)
	if err := ws.WriteMessage(websocket.BinaryMessage, pcm); err != nil {
		t.Fatal(err)
	}
	wantHeader := make([]byte, 2)
	binary.BigEndian.PutUint16(wantHeader, uint16(len(pcm)))
	got := make([]byte, 2+len(pcm))
	if _, err := io.ReadFull(guestInR, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[:2], wantHeader) || !bytes.Equal(got[2:], pcm) {
		t.Fatal("forwarded PCM frame did not match")
	}

	// Guest deactivates demand.
	go func() {
		_, _ = io.WriteString(guestOutW, "{\"demand\":false}\n")
	}()
	rawDemand = readReply()
	if json.Unmarshal(rawDemand, &demandMsg) != nil || demandMsg.Demand {
		t.Fatalf("expected demand=false, got %s", rawDemand)
	}

	// Cancelling request context tears down stream cleanly without goroutine leaks.
	cancel()
	select {
	case msg := <-replies:
		if msg.err == nil {
			t.Fatal("cancelled audio input stream stayed open")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled audio input stream stayed open")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("audio input handler leaked")
	}
}
