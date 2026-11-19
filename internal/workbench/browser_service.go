package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/sylvinus/agent-vm/internal/containerenv"
)

var (
	errBrowserNotReady = errors.New("环境尚未就绪")
	errBrowserBusy     = errors.New("环境正在执行其他操作")
)

type browserInputError struct{ error }

type browserInput struct {
	Incognito      bool     `json:"incognito,omitempty"`
	Enabled        *bool    `json:"enabled,omitempty"`
	CookieKey      string   `json:"cookieKey,omitempty"`
	CookieKeys     []string `json:"cookieKeys,omitempty"`
	Scope          string   `json:"scope,omitempty"`
	Query          string   `json:"query,omitempty"`
	Page           int      `json:"page,omitempty"`
	Left           string   `json:"left,omitempty"`
	Right          string   `json:"right,omitempty"`
	ID             string   `json:"id"`
	URL            string   `json:"url"`
	X              int      `json:"x,omitempty"`
	Y              int      `json:"y,omitempty"`
	DeltaX         int      `json:"deltaX,omitempty"`
	DeltaY         int      `json:"deltaY,omitempty"`
	Width          int      `json:"width,omitempty"`
	Height         int      `json:"height,omitempty"`
	Scale          float64  `json:"scale,omitempty"`
	Workspace      string   `json:"workspace,omitempty"`
	ProjectID      string   `json:"project_id,omitempty"`
	ProjectGrant   string   `json:"grant,omitempty"`
	Text           string   `json:"text,omitempty"`
	Mode           string   `json:"mode,omitempty"`
	Target         string   `json:"target,omitempty"`
	Session        string   `json:"session,omitempty"`
	Context        string   `json:"context,omitempty"`
	Sequence       int      `json:"sequence"`
	SelectionStart int      `json:"selectionStart,omitempty"`
	SelectionEnd   int      `json:"selectionEnd,omitempty"`
}

func (s *Server) browserClient() *browserClient {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.browserControl == nil {
		s.browserControl = newBrowserClient(s.lifetimeContextLocked())
		transport := s.browserControl.http.Transport.(*http.Transport)
		lifetime := s.browserControl.ctx
		if containerenv.Configured(s.profile) {
			transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				environment, err := containerenv.Load(s.profile)
				if err != nil {
					return nil, err
				}
				state, err := environment.State(ctx)
				if err != nil {
					return nil, err
				}
				if !state.Ready {
					return nil, errBrowserNotReady
				}
				return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, state.BrowserAddress)
			}
		} else {
			transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
				return s.dialGuestBrowser(ctx, lifetime)
			}
		}
	}
	return s.browserControl
}

func (s *Server) guestAPI(ctx context.Context, token, method, path string, body []byte) ([]byte, error) {
	return s.browserClient().request(ctx, token, method, path, body)
}

func (s *Server) setBrowserHome(ctx context.Context, token, home string) error {
	body, _ := json.Marshal(map[string]string{"url": home})
	_, err := s.guestAPI(ctx, token, http.MethodPost, "/homepage", body)
	return err
}

func (s *Server) browserAction(ctx context.Context, action string, input *browserInput) ([]byte, error) {
	s.mu.Lock()
	ready, token := s.ready, s.credentials.Token
	s.mu.Unlock()
	if !ready {
		return nil, errBrowserNotReady
	}
	method := http.MethodGet
	var body []byte
	if input != nil {
		method = http.MethodPost
		if action == "cookies" && (input.ID == "" ||
			(input.Mode != "list" && input.Mode != "details" && input.Mode != "delete" && input.Mode != "delete-selected" && input.Mode != "clear") ||
			(input.Scope != "site" && input.Scope != "all") || len(input.Query) > 800 || input.Page < 0 ||
			((input.Mode == "delete" || input.Mode == "details") && len(input.CookieKey) != 64)) {
			return nil, browserInputError{errors.New("Cookie 操作或目标标签无效")}
		}
		if action == "cookies" && input.Mode == "delete-selected" {
			if len(input.CookieKeys) == 0 {
				return nil, browserInputError{errors.New("请选择要删除的 Cookie")}
			}
			for _, key := range input.CookieKeys {
				if len(key) != 64 || strings.Trim(key, "0123456789abcdef") != "" {
					return nil, browserInputError{errors.New("所选 Cookie 无效")}
				}
			}
		}
		if (action == "copy-context" || action == "copy" || action == "cut") && (input.ID == "" || !validInputToken(input.Context)) {
			return nil, browserInputError{errors.New("复制操作或目标标签无效")}
		}
		if action == "resize" && !validDisplayDensity(input.Scale) {
			return nil, browserInputError{errors.New("画面像素密度无效")}
		}
		if action == "wheel" {
			// The guest's controller scrolls a page by a distance.
			if !validWheel(input) {
				return nil, browserInputError{errors.New("滚动位置或距离无效")}
			}
			body, _ := json.Marshal(map[string]int{"x": input.X, "y": input.Y, "deltaX": input.DeltaX, "deltaY": input.DeltaY})
			return s.guestAPI(ctx, token, method, "/wheel", body)
		}
		if action == "insert-text" && (input.ID == "" || input.Text == "" || len(input.Text) > 8192) {
			return nil, browserInputError{errors.New("输入文字或目标标签无效")}
		}
		if action == "ime" {
			selectionLimit := 0
			for _, char := range input.Text {
				selectionLimit++
				if char > 0xffff {
					selectionLimit++
				}
			}
			validMode := input.Mode == "status" || input.Mode == "begin" || input.Mode == "update" || input.Mode == "commit" || input.Mode == "cancel"
			validTarget := input.Target == "browser" && input.ID != "" || input.Target == "desktop" && input.ID == "desktop"
			validSession := input.Mode == "status" || validInputToken(input.Session)
			if !validTarget || !validMode || !validSession || input.Mode == "begin" && !validInputToken(input.Context) || input.Sequence < 0 || len(input.Text) > 8192 || input.SelectionStart < 0 || input.SelectionEnd < input.SelectionStart || input.SelectionEnd > selectionLimit {
				return nil, browserInputError{errors.New("输入法组合状态无效")}
			}
		}
		if action == "new" && input.URL == "" {
			s.mu.Lock()
			input.URL = browserHomeURL(s.preferences)
			s.mu.Unlock()
		}
		if action == "editor" || action == "editor-restart" {
			if !s.op.TryLock() {
				return nil, errBrowserBusy
			}
			defer s.op.Unlock()
			s.mu.Lock()
			closing := s.closing
			s.mu.Unlock()
			if closing {
				return nil, errJobsClosed
			}
			workspace, err := s.connectProject(input.Workspace, false)
			if err != nil {
				return nil, browserInputError{err}
			}
			input.Workspace = guestPath(workspace)
		}
		body, _ = json.Marshal(input)
	}
	data, err := s.guestAPI(ctx, token, method, "/"+action, body)
	if err != nil {
		return nil, err
	}
	if action == "tabs" {
		data, err = s.browserStateForHost(data)
	}
	if len(data) > browserResponseLimit {
		return nil, errors.New("浏览器响应超出大小限制")
	}
	return data, err
}

// browserWheelLimit bounds a position and a scrolled distance, both in
// framebuffer pixels. The display itself is at most this wide and high.
const browserWheelLimit = 8192

func validWheel(input *browserInput) bool {
	within := func(value, low int) bool { return value >= low && value <= browserWheelLimit }
	return within(input.X, 0) && within(input.Y, 0) && within(input.DeltaX, -browserWheelLimit) &&
		within(input.DeltaY, -browserWheelLimit) && (input.DeltaX != 0 || input.DeltaY != 0)
}

func validInputToken(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func (s *Server) subscribeBrowser(ctx context.Context) (io.ReadCloser, error) {
	s.mu.Lock()
	ready, token := s.ready, s.credentials.Token
	s.mu.Unlock()
	if !ready {
		return nil, errBrowserNotReady
	}
	return s.browserClient().events(ctx, token)
}
