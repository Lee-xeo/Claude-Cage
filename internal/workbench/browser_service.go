package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sylvinus/agent-vm/internal/containerenv"
)

var (
	errBrowserNotReady = errors.New("环境尚未就绪")
	errBrowserBusy     = errors.New("环境正在执行其他操作")
)

type browserInputError struct{ error }

// browserCookieInput is one cookie to add or edit. Value is sensitive: it is
// forwarded to the guest only and never logged or echoed in errors.
type browserCookieInput struct {
	Name        string  `json:"name"`
	Value       string  `json:"value"`
	Domain      string  `json:"domain"`
	Path        string  `json:"path"`
	Secure      bool    `json:"secure,omitempty"`
	HTTPOnly    bool    `json:"httpOnly,omitempty"`
	SameSite    string  `json:"sameSite,omitempty"`
	Expires     float64 `json:"expires,omitempty"`
	Partitioned bool    `json:"partitioned,omitempty"`
}

type browserInput struct {
	Incognito      bool                `json:"incognito,omitempty"`
	Enabled        *bool               `json:"enabled,omitempty"`
	CookieKey      string              `json:"cookieKey,omitempty"`
	CookieKeys     []string            `json:"cookieKeys,omitempty"`
	Cookie         *browserCookieInput `json:"cookie,omitempty"`
	Scope          string              `json:"scope,omitempty"`
	Query          string              `json:"query,omitempty"`
	Page           int                 `json:"page,omitempty"`
	Limit          int                 `json:"limit,omitempty"`
	Range          string              `json:"range,omitempty"`
	StartTime      float64             `json:"startTime,omitempty"`
	EndTime        float64             `json:"endTime,omitempty"`
	Left           string              `json:"left,omitempty"`
	Right          string              `json:"right,omitempty"`
	ID             string              `json:"id"`
	URL            string              `json:"url"`
	X              int                 `json:"x,omitempty"`
	Y              int                 `json:"y,omitempty"`
	DeltaX         int                 `json:"deltaX,omitempty"`
	DeltaY         int                 `json:"deltaY,omitempty"`
	Width          int                 `json:"width,omitempty"`
	Height         int                 `json:"height,omitempty"`
	Scale          float64             `json:"scale,omitempty"`
	Workspace      string              `json:"workspace,omitempty"`
	ProjectID      string              `json:"project_id,omitempty"`
	ProjectGrant   string              `json:"grant,omitempty"`
	Text           string              `json:"text,omitempty"`
	Mode           string              `json:"mode,omitempty"`
	Target         string              `json:"target,omitempty"`
	Session        string              `json:"session,omitempty"`
	Context        string              `json:"context,omitempty"`
	Sequence       int                 `json:"sequence"`
	SelectionStart int                 `json:"selectionStart,omitempty"`
	SelectionEnd   int                 `json:"selectionEnd,omitempty"`
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
			(input.Mode != "list" && input.Mode != "details" && input.Mode != "delete" && input.Mode != "delete-selected" && input.Mode != "clear" && input.Mode != "set") ||
			(input.Scope != "site" && input.Scope != "all") || len(input.Query) > 800 || input.Page < 0 ||
			((input.Mode == "delete" || input.Mode == "details") && len(input.CookieKey) != 64) ||
			(input.Mode == "set" && input.CookieKey != "" && !validCookieKey(input.CookieKey)) ||
			(input.Mode != "set" && input.Cookie != nil)) {
			return nil, browserInputError{errors.New("Cookie 操作或目标标签无效")}
		}
		if action == "cookies" && input.Mode == "set" {
			if err := validBrowserCookie(input.Cookie); err != nil {
				return nil, browserInputError{err}
			}
		}
		if action == "cookies" && input.Mode == "delete-selected" {
			if len(input.CookieKeys) == 0 {
				return nil, browserInputError{errors.New("请选择要删除的 Cookie")}
			}
			for _, key := range input.CookieKeys {
				if !validCookieKey(key) {
					return nil, browserInputError{errors.New("所选 Cookie 无效")}
				}
			}
		}
		if action == "history" {
			if err := validBrowserHistory(input); err != nil {
				return nil, browserInputError{err}
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

func validCookieKey(key string) bool {
	return len(key) == 64 && strings.Trim(key, "0123456789abcdef") == ""
}

func validBrowserHistory(input *browserInput) error {
	if input == nil {
		return errors.New("历史记录操作无效")
	}
	if input.Mode == "" {
		input.Mode = "list"
	}
	switch input.Mode {
	case "list":
		input.Query = strings.TrimSpace(input.Query)
		if len(input.Query) > 500 {
			return errors.New("搜索词过长，请缩短后重试")
		}
		if input.Limit < 0 || input.Limit > 500 {
			return errors.New("历史记录条数无效")
		}
		if math.IsNaN(input.StartTime) || math.IsInf(input.StartTime, 0) || input.StartTime < 0 ||
			math.IsNaN(input.EndTime) || math.IsInf(input.EndTime, 0) || input.EndTime < 0 {
			return errors.New("历史记录时间范围无效")
		}
		if input.URL != "" || input.Range != "" {
			return errors.New("历史记录查询参数无效")
		}
		return nil
	case "delete":
		input.URL = strings.TrimSpace(input.URL)
		parsed, err := url.Parse(input.URL)
		if err != nil || len(input.URL) == 0 || len(input.URL) > 16384 ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") ||
			parsed.Hostname() == "" || parsed.User != nil ||
			(parsed.Hostname() == "127.0.0.1" && parsed.Port() == "18080") {
			return errors.New("历史记录地址无效")
		}
		return nil
	case "clear":
		if input.Range == "" {
			input.Range = "all"
		}
		switch input.Range {
		case "hour", "day", "week", "all":
			return nil
		default:
			return errors.New("清除范围无效")
		}
	default:
		return errors.New("历史记录操作无效")
	}
}

// validBrowserCookie checks the format of a cookie to write. The guest owns
// the site, prefix and SameSite rules because they depend on the tab's page.
func validBrowserCookie(cookie *browserCookieInput) error {
	if cookie == nil {
		return errors.New("Cookie 内容无效")
	}
	if cookie.Partitioned {
		return errors.New("暂不支持新增或编辑分区 Cookie")
	}
	invalid := func(text string) bool {
		return strings.ContainsFunc(text, func(r rune) bool { return r < 0x20 || r == 0x7f || r == ';' })
	}
	switch {
	case cookie.Name == "" || strings.TrimSpace(cookie.Name) != cookie.Name || invalid(cookie.Name) || strings.Contains(cookie.Name, "="):
		return errors.New("Cookie 名称不能为空，且不能包含空格开头结尾、控制字符、分号或等号")
	case strings.TrimSpace(cookie.Value) != cookie.Value || invalid(cookie.Value):
		return errors.New("Cookie 值不能以空格开头结尾，且不能包含控制字符或分号")
	case len(cookie.Name)+len(cookie.Value) > 4096:
		return errors.New("Cookie 名称与值合计不能超过 4096 字节")
	case cookie.Domain == "" || len(cookie.Domain) > 254 || invalid(cookie.Domain):
		return errors.New("只能为当前网站的主机名或其上级域名设置 Cookie")
	case !strings.HasPrefix(cookie.Path, "/") || len(cookie.Path) > 1024 || invalid(cookie.Path):
		return errors.New("Cookie 路径必须以 / 开头，且不超过 1024 字节")
	case cookie.SameSite != "" && cookie.SameSite != "Strict" && cookie.SameSite != "Lax" && cookie.SameSite != "None":
		return errors.New("SameSite 取值无效")
	case cookie.Expires < 0:
		return errors.New("过期时间必须晚于当前时间；删除 Cookie 请使用删除")
	}
	return nil
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
