package workbench

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
)

const (
	maxMobileDesktopUploads      = 4
	mobileDesktopUploadChunkSize = 44 << 10
	mobileDesktopUploadMaxChunk  = 48 << 10
)

type mobileDesktopUpload struct {
	id           string
	transfer     string
	tempPath     string
	file         *os.File
	expectedSize int64
	written      int64
	created      time.Time
}

type mobileDesktopUploadRequest struct {
	Op       string `json:"op"`
	UploadID string `json:"upload_id,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Offset   int64  `json:"offset,omitempty"`
	Data     string `json:"data,omitempty"`
}

func (s *Server) desktopUploadGuestFiles(ctx context.Context, method, path string, body io.Reader, name string) (map[string]any, error) {
	if s.mobileDesktopGuestFiles != nil {
		return s.mobileDesktopGuestFiles(ctx, method, path, body, name)
	}
	return s.guestFiles(ctx, "desktop", method, path, body, name)
}

func (s *Server) cancelDesktopUploadTransfer(transfer string) {
	if !browserFileID.MatchString(transfer) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"id": transfer})
	_, _ = s.desktopUploadGuestFiles(ctx, http.MethodPost, "/cancel", bytes.NewReader(body), "")
}

func (u *mobileDesktopUpload) closeAndRemove() {
	if u.file != nil {
		_ = u.file.Close()
		u.file = nil
	}
	if u.tempPath != "" {
		_ = os.Remove(u.tempPath)
		u.tempPath = ""
	}
}

func (s *Server) cleanupMobileDesktopUploads(b *phoneSurfaceSession) {
	if b == nil || len(b.uploads) == 0 {
		return
	}
	for id, up := range b.uploads {
		delete(b.uploads, id)
		up.closeAndRemove()
		s.cancelDesktopUploadTransfer(up.transfer)
	}
}

func (s *Server) serveMobileDesktopUpload(w http.ResponseWriter, r *http.Request) {
	p := &s.phones
	p.mu.Lock()
	slot := p.matchPhoneLocked(r)
	var state *phoneSurfaceState
	var room context.Context
	if slot != nil {
		state = p.surfaceForSlot(phoneDesktop, slot)
		room = slot.roomCtx
	}
	p.mu.Unlock()
	if slot == nil {
		http.Error(w, "请通过电脑 Cage 的二维码或配对码连接这台主机，再添加图片。", http.StatusForbidden)
		return
	}
	epoch, _ := r.Context().Value(accountEpochKey{}).(context.Context)
	if epoch == nil || epoch.Err() != nil {
		http.Error(w, "请先解锁 Cage", http.StatusLocked)
		return
	}
	sessionID := r.PathValue("id")
	if !mobileID(sessionID) {
		http.Error(w, "手机会话编号无效", http.StatusBadRequest)
		return
	}
	var req mobileDesktopUploadRequest
	if !readJSON(w, r, &req, 72<<10) {
		return
	}
	if req.Op != "start" && req.Op != "chunk" && req.Op != "finish" && req.Op != "cancel" {
		http.Error(w, "图片上传操作无效", http.StatusBadRequest)
		return
	}

	state.op.Lock()
	defer state.op.Unlock()
	if r.Context().Err() != nil || room.Err() != nil || epoch.Err() != nil {
		http.Error(w, "Claude Desktop 授权已结束", http.StatusForbidden)
		return
	}
	b := state.session
	if b == nil || b.ctx.Err() != nil || b.id != sessionID {
		http.Error(w, "Claude Desktop 会话已结束，请重新打开", http.StatusGone)
		return
	}
	if !s.phoneSurfaceNetwork(r.Context()) {
		s.retirePhoneSurfaceState(state, phoneDesktop, b)
		http.Error(w, "请在电脑启动 Cage，并确保网络检查已放行。", http.StatusConflict)
		return
	}
	if b.uploads == nil {
		b.uploads = make(map[string]*mobileDesktopUpload)
	}

	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(b.ctx, cancel)
	defer func() { stop(); cancel() }()

	switch req.Op {
	case "start":
		if req.Size <= 0 || req.Size > maxImageBytes {
			http.Error(w, "图片大小无效，单张图片不能超过 20 MB", http.StatusBadRequest)
			return
		}
		if len(b.uploads) >= maxMobileDesktopUploads {
			http.Error(w, "同时上传的图片数量已达上限，请等待当前上传完成", http.StatusTooManyRequests)
			return
		}
		startCtx, startCancel := context.WithTimeout(ctx, 10*time.Second)
		defer startCancel()
		body, _ := json.Marshal(map[string]any{"kind": "image"})
		result, err := s.desktopUploadGuestFiles(startCtx, http.MethodPost, "/start", bytes.NewReader(body), "")
		transfer, _ := result["id"].(string)
		if err != nil || !browserFileID.MatchString(transfer) {
			http.Error(w, "图片接收位置尚未聚焦，请先点击对话输入框后再添加图片", http.StatusConflict)
			return
		}
		tmp, err := os.CreateTemp("", "cage-mobile-img-*")
		if err != nil {
			s.cancelDesktopUploadTransfer(transfer)
			http.Error(w, "无法准备图片临时缓冲，请稍后重试", http.StatusInternalServerError)
			return
		}
		_ = tmp.Chmod(0o600)
		uploadID := uuid.NewString()
		b.uploads[uploadID] = &mobileDesktopUpload{
			id:           uploadID,
			transfer:     transfer,
			tempPath:     tmp.Name(),
			file:         tmp,
			expectedSize: req.Size,
			created:      time.Now(),
		}
		jsonReply(w, map[string]any{
			"upload_id":  uploadID,
			"chunk_size": mobileDesktopUploadChunkSize,
		})

	case "chunk":
		up := b.uploads[req.UploadID]
		if up == nil {
			http.Error(w, "图片上传会话不存在或已结束，请重试", http.StatusGone)
			return
		}
		if req.Offset != up.written {
			http.Error(w, "图片分片顺序不匹配，请重试", http.StatusConflict)
			return
		}
		raw, err := base64.StdEncoding.Strict().DecodeString(req.Data)
		if err != nil || len(raw) == 0 || len(raw) > mobileDesktopUploadMaxChunk || up.written+int64(len(raw)) > up.expectedSize {
			http.Error(w, "图片分片数据无效或超出声明大小", http.StatusBadRequest)
			return
		}
		if _, err := up.file.Write(raw); err != nil {
			delete(b.uploads, up.id)
			up.closeAndRemove()
			s.cancelDesktopUploadTransfer(up.transfer)
			http.Error(w, "写入图片缓冲失败，请重试", http.StatusInternalServerError)
			return
		}
		up.written += int64(len(raw))
		jsonReply(w, map[string]any{
			"upload_id": up.id,
			"received":  up.written,
		})

	case "cancel":
		up := b.uploads[req.UploadID]
		if up != nil {
			delete(b.uploads, up.id)
			up.closeAndRemove()
			s.cancelDesktopUploadTransfer(up.transfer)
		}
		jsonReply(w, map[string]bool{"ok": true})

	case "finish":
		up := b.uploads[req.UploadID]
		if up == nil {
			http.Error(w, "图片上传会话不存在或已结束，请重试", http.StatusGone)
			return
		}
		delete(b.uploads, up.id)
		transfer := up.transfer
		expected := up.expectedSize
		written := up.written
		tempPath := up.tempPath
		if up.file != nil {
			_ = up.file.Close()
			up.file = nil
		}
		up.tempPath = ""
		defer func() {
			if tempPath != "" {
				_ = os.Remove(tempPath)
			}
		}()
		if written != expected {
			s.cancelDesktopUploadTransfer(transfer)
			http.Error(w, "图片未完整收到，请重试", http.StatusBadRequest)
			return
		}
		raw, err := os.ReadFile(tempPath)
		if err != nil {
			s.cancelDesktopUploadTransfer(transfer)
			http.Error(w, "读取暂存图片失败，请重试", http.StatusInternalServerError)
			return
		}
		clean, err := validateInputImage(raw)
		if err != nil {
			s.cancelDesktopUploadTransfer(transfer)
			requestError(w, err)
			return
		}
		deliverCtx, deliverCancel := context.WithTimeout(ctx, 25*time.Second)
		defer deliverCancel()
		if _, err := s.desktopUploadGuestFiles(deliverCtx, http.MethodPost, "/upload?id="+transfer, bytes.NewReader(clean), "image.png"); err != nil {
			s.cancelDesktopUploadTransfer(transfer)
			http.Error(w, "图片未能传给远程桌面，请重试", http.StatusBadGateway)
			return
		}
		body, _ := json.Marshal(map[string]string{"id": transfer})
		if _, err := s.desktopUploadGuestFiles(deliverCtx, http.MethodPost, "/finish-image", bytes.NewReader(body), ""); err != nil {
			s.cancelDesktopUploadTransfer(transfer)
			http.Error(w, "图片未能添加到当前对话，请确认输入框已聚焦后重试", http.StatusConflict)
			return
		}
		jsonReply(w, map[string]bool{"ok": true})
	}
}
