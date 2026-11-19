// Package hostbridge serves tools that act on the user's computer over MCP.
//
// A Bridge listens on loopback only and requires a bearer token on every
// request. Each capability is a Plugin that adds its own tools, so one
// capability can be developed, tested and replaced without touching the rest.
package hostbridge

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A Plugin adds one capability's tools to the server.
//
// Tool names and descriptions are sent to the model provider with every
// request, so they describe only what the tool does; see CheckWording.
type Plugin interface {
	Register(server *mcp.Server)
}

// Bridge is a running MCP endpoint.
type Bridge struct {
	token    string
	listener net.Listener
	server   *http.Server
	once     sync.Once
}

// NewToken returns a random bearer token.
func NewToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// Start serves the plugins on a free loopback port to clients presenting token.
func Start(token string, plugins ...Plugin) (*Bridge, error) {
	if len(token) < 32 {
		return nil, errors.New("token is too short")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	b := &Bridge{token: token, listener: listener}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		// One server per request keeps clients from seeing each other's state.
		server := mcp.NewServer(&mcp.Implementation{Name: "computer", Version: "1"}, nil)
		for _, plugin := range plugins {
			plugin.Register(server)
		}
		return server
		// Without sessions: a client that connected before this endpoint was
		// started again carries on, instead of failing on a session that no
		// longer exists. The tools never ask the client anything back.
	}, &mcp.StreamableHTTPOptions{Stateless: true})
	b.server = &http.Server{Handler: b.authorize(handler), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = b.server.Serve(listener) }()
	return b, nil
}

// Addr is the loopback host:port the bridge listens on.
func (b *Bridge) Addr() string { return b.listener.Addr().String() }

// Token is the bearer token a client must present.
func (b *Bridge) Token() string { return b.token }

// Close stops the bridge and ends running tool calls.
func (b *Bridge) Close() error {
	var err error
	b.once.Do(func() { err = b.server.Close() })
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (b *Bridge) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(presented), []byte(b.token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// reservedWords must not appear in anything a tool shows the model: they
// describe this product or where it runs rather than what the tool does.
var reservedWords = []string{"cage", "guest", "virtual machine", "vm", "虚拟机", "客机"}

// CheckWording reports the first reserved word found in what a tool shows
// the model: its name, title and description, and the names and wording of
// its parameters and results. Latin words are matched whole.
func CheckWording(tool *mcp.Tool) (word string, found bool) {
	text := tool.Name + " " + tool.Title + " " + tool.Description
	if tool.Annotations != nil {
		text += " " + tool.Annotations.Title
	}
	for _, schema := range []any{tool.InputSchema, tool.OutputSchema} {
		if schema != nil {
			data, _ := json.Marshal(schema)
			text += " " + string(data)
		}
	}
	text = strings.ToLower(text)
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r < 0x80 && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
	for _, reserved := range reservedWords {
		if strings.ContainsRune(reserved, ' ') || reserved[0] >= 0x80 {
			if strings.Contains(text, reserved) {
				return reserved, true
			}
			continue
		}
		for _, field := range fields {
			if field == reserved {
				return reserved, true
			}
		}
	}
	return "", false
}

// connect is shared by tests and callers that need a client for the bridge.
func (b *Bridge) connect(ctx context.Context, token string) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "1"}, nil)
	return client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   "http://" + b.Addr(),
		HTTPClient: &http.Client{Transport: bearer{token}},
		MaxRetries: -1,
	}, nil)
}

type bearer struct{ token string }

func (t bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return http.DefaultTransport.RoundTrip(r)
}
