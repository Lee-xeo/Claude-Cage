package workbench

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const (
	audioInputBridgeCommand = "exec /usr/local/bin/cage-audio-input cage_input"
	audioInputMaxFrameBytes = 16384
)

var audioInputFormat = struct {
	Format     string `json:"format"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
}{"s16le", 48000, 1}

type audioInputHandshake struct {
	Format     string `json:"format"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
	Demand     bool   `json:"demand"`
}

type audioInputDemand struct {
	Demand bool `json:"demand"`
}

// audioInput bridges host microphone capture into the guest virtual microphone
// strictly on demand: the guest feeder reports when a guest application opens
// or closes the virtual input source, and PCM frames are only accepted and
// forwarded while demand is active.
func (s *Server) audioInput(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	available := !s.closing && !s.guestUpdateRequired
	s.mu.Unlock()
	if !available || !s.vmRunning() {
		http.Error(w, "请先启动并更新 Cage 环境", http.StatusConflict)
		return
	}
	if !websocket.IsWebSocketUpgrade(r) {
		jsonReply(w, audioInputFormat)
		return
	}
	s.mu.Lock()
	if s.audioInputClients >= 1 {
		s.mu.Unlock()
		http.Error(w, "麦克风连接已由其他工作台窗口占用", http.StatusConflict)
		return
	}
	s.audioInputClients++
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.audioInputClients--; s.mu.Unlock() }()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	command, renderErr := s.linuxTemplate(audioInputBridgeCommand)
	if renderErr != nil {
		http.Error(w, "音频创建配置无效", http.StatusConflict)
		return
	}
	cmd := s.sshCommand(ctx, "-T", s.sshTarget(), "--", command)
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		http.Error(w, "麦克风连接未能建立", http.StatusBadGateway)
		return
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdinPipe.Close()
		http.Error(w, "麦克风连接未能建立", http.StatusBadGateway)
		return
	}
	if err = cmd.Start(); err != nil {
		_ = stdinPipe.Close()
		_ = stdoutPipe.Close()
		http.Error(w, "麦克风连接未能建立", http.StatusBadGateway)
		return
	}
	defer func() {
		cancel()
		_ = stdinPipe.Close()
		_ = stdoutPipe.Close()
		_ = cmd.Wait()
	}()
	s.streamAudioInput(w, r.WithContext(ctx), stdoutPipe, stdinPipe)
}

func (s *Server) streamAudioInput(w http.ResponseWriter, r *http.Request, guestStdout io.ReadCloser, guestStdin io.WriteCloser) {
	defer guestStdout.Close()
	defer guestStdin.Close()
	ws, err := s.upgradeWithCompression(w, r, false)
	if err != nil {
		return
	}
	defer ws.Close()
	ws.SetReadLimit(audioInputMaxFrameBytes)

	_ = ws.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if ws.WriteJSON(audioInputHandshake{
		Format:     audioInputFormat.Format,
		SampleRate: audioInputFormat.SampleRate,
		Channels:   audioInputFormat.Channels,
		Demand:     false,
	}) != nil {
		return
	}

	var demand atomic.Bool
	done := make(chan struct{})
	finished := make(chan struct{})
	defer close(finished)

	go func() {
		select {
		case <-r.Context().Done():
			_ = ws.Close()
			_ = guestStdin.Close()
			_ = guestStdout.Close()
		case <-finished:
		}
	}()

	go func() {
		defer close(done)
		defer ws.Close()
		scanner := bufio.NewScanner(guestStdout)
		scanner.Buffer(make([]byte, 256), 4096)
		for scanner.Scan() {
			var event audioInputDemand
			if json.Unmarshal(scanner.Bytes(), &event) != nil {
				continue
			}
			demand.Store(event.Demand)
			_ = ws.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if ws.WriteJSON(event) != nil {
				return
			}
		}
	}()

	defer func() {
		_ = guestStdin.Close()
		_ = guestStdout.Close()
		_ = ws.Close()
		<-done
	}()

	frame := make([]byte, 2+audioInputMaxFrameBytes)
	for {
		kind, data, readErr := ws.ReadMessage()
		if readErr != nil {
			return
		}
		if kind != websocket.BinaryMessage {
			return
		}
		if len(data) == 0 || len(data) > audioInputMaxFrameBytes || len(data)%2 != 0 {
			return
		}
		if !demand.Load() {
			continue
		}
		binary.BigEndian.PutUint16(frame[:2], uint16(len(data)))
		copy(frame[2:], data)
		if _, writeErr := guestStdin.Write(frame[:2+len(data)]); writeErr != nil {
			return
		}
	}
}
