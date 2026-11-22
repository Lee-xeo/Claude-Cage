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
	"fmt"
	"net"
	"net/http"
	"slices"
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

// namedPlugin is an optional interface implemented by plugins that can report
// the model tool names they currently register, so ephemeral bridges never
// shadow base tools.
type namedPlugin interface {
	Tools() []string
}

// namedPromptPlugin is an optional interface implemented by plugins that can
// report the prompt names they register, so ephemeral bridges never shadow
// base prompts.
type namedPromptPlugin interface {
	Prompts() []string
}

// PromptInfo is a prompt's name and what it says it does.
type PromptInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Bridge is a running MCP endpoint.
type Bridge struct {
	token       string
	listener    net.Listener
	server      *http.Server
	base        []Plugin
	ephemeralMu sync.RWMutex
	ephemeral   []Plugin
	once        sync.Once
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
	base := append([]Plugin(nil), plugins...)
	b := &Bridge{token: token, listener: listener, base: base}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		// One server per request keeps clients from seeing each other's state.
		server := mcp.NewServer(&mcp.Implementation{Name: "computer", Version: "1"}, &mcp.ServerOptions{
			Capabilities: &mcp.ServerCapabilities{
				Tools:   &mcp.ToolCapabilities{ListChanged: true},
				Prompts: &mcp.PromptCapabilities{ListChanged: true},
			},
		})
		for _, plugin := range b.base {
			plugin.Register(server)
		}
		b.ephemeralMu.RLock()
		ephemeral := append([]Plugin(nil), b.ephemeral...)
		b.ephemeralMu.RUnlock()
		for _, plugin := range ephemeral {
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

// BaseToolNames returns the tool names currently registered by base plugins.
func (b *Bridge) BaseToolNames() map[string]bool {
	taken := map[string]bool{}
	for _, plugin := range b.base {
		if named, ok := plugin.(namedPlugin); ok {
			for _, name := range named.Tools() {
				taken[name] = true
			}
		}
	}
	return taken
}

// BasePromptNames returns the prompt names currently registered by base plugins.
func (b *Bridge) BasePromptNames() map[string]bool {
	taken := map[string]bool{}
	for _, plugin := range b.base {
		if named, ok := plugin.(namedPromptPlugin); ok {
			for _, name := range named.Prompts() {
				taken[name] = true
			}
		}
	}
	return taken
}

// SetEphemeral replaces the temporary plugins exposed alongside the base
// plugins. Passing no plugins clears all temporary tools immediately.
func (b *Bridge) SetEphemeral(plugins ...Plugin) {
	b.ephemeralMu.Lock()
	b.ephemeral = append([]Plugin(nil), plugins...)
	b.ephemeralMu.Unlock()
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

// CheckTextWording reports the first reserved word found in text that will be
// shown to the model. Latin words are matched whole.
func CheckTextWording(text string) (word string, found bool) {
	lower := strings.ToLower(text)
	fields := strings.FieldsFunc(lower, func(r rune) bool {
		return r < 0x80 && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
	for _, reserved := range reservedWords {
		if strings.ContainsRune(reserved, ' ') || reserved[0] >= 0x80 {
			if strings.Contains(lower, reserved) {
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
	return CheckTextWording(text)
}

// CheckPromptWording reports the first reserved word found in what a prompt
// shows the model: its name, title, description and arguments.
func CheckPromptWording(prompt *mcp.Prompt) (word string, found bool) {
	if prompt == nil {
		return "", false
	}
	text := prompt.Name + " " + prompt.Title + " " + prompt.Description
	for _, arg := range prompt.Arguments {
		if arg == nil {
			continue
		}
		text += " " + arg.Name + " " + arg.Title + " " + arg.Description
	}
	return CheckTextWording(text)
}

// CheckPromptResultWording reports the first reserved word found in the
// description or text messages returned by prompts/get.
func CheckPromptResultWording(result *mcp.GetPromptResult) (word string, found bool) {
	if result == nil {
		return "", false
	}
	if w, ok := CheckTextWording(result.Description); ok {
		return w, true
	}
	for _, message := range result.Messages {
		if message == nil || message.Content == nil {
			continue
		}
		switch content := message.Content.(type) {
		case *mcp.TextContent:
			if w, ok := CheckTextWording(content.Text); ok {
				return w, true
			}
		case *mcp.EmbeddedResource:
			if content.Resource != nil {
				if w, ok := CheckTextWording(content.Resource.URI + " " + content.Resource.Text); ok {
					return w, true
				}
			}
		}
	}
	return "", false
}

// buildPluginPrompt creates an explicit slash-command MCP prompt for a group of
// model-visible tools so clients can invoke the plugin directly via prompts.
func buildPluginPrompt(name, title, description string, toolNames []string) (*mcp.Prompt, mcp.PromptHandler) {
	name = strings.TrimSpace(name)
	if name == "" || len(toolNames) == 0 {
		return nil, nil
	}
	if _, bad := CheckTextWording(name); bad {
		return nil, nil
	}
	var cleanTools []string
	for _, toolName := range toolNames {
		toolName = strings.TrimSpace(toolName)
		if toolName == "" || slices.Contains(cleanTools, toolName) {
			continue
		}
		if _, bad := CheckTextWording(toolName); bad {
			continue
		}
		cleanTools = append(cleanTools, toolName)
	}
	if len(cleanTools) == 0 {
		return nil, nil
	}
	slices.Sort(cleanTools)

	cleanTitle := strings.TrimSpace(title)
	if cleanTitle == "" {
		cleanTitle = name
	} else if _, bad := CheckTextWording(cleanTitle); bad {
		cleanTitle = name
	}
	cleanDesc := strings.Join(strings.Fields(description), " ")
	if cleanDesc == "" {
		cleanDesc = fmt.Sprintf("Use the %s tools.", name)
	} else if _, bad := CheckTextWording(cleanDesc); bad {
		cleanDesc = fmt.Sprintf("Use the %s tools.", name)
	}

	prompt := &mcp.Prompt{
		Name:        name,
		Title:       cleanTitle,
		Description: cleanDesc,
		Arguments: []*mcp.PromptArgument{
			{
				Name:        "request",
				Description: "What to do with these tools.",
				Required:    false,
			},
		},
	}
	toolList := strings.Join(cleanTools, ", ")
	handler := func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		var request string
		if req != nil && req.Params != nil && req.Params.Arguments != nil {
			request = strings.TrimSpace(req.Params.Arguments["request"])
		}
		var body string
		if request != "" {
			body = fmt.Sprintf("Use the available tools (%s) to handle this request:\n\n%s", toolList, request)
		} else {
			body = fmt.Sprintf("Use the available tools (%s) to help with the user's request, or briefly list what you can do with them if no task was given.", toolList)
		}
		return &mcp.GetPromptResult{
			Description: cleanDesc,
			Messages: []*mcp.PromptMessage{
				{
					Role:    "user",
					Content: &mcp.TextContent{Text: body},
				},
			},
		}, nil
	}
	return prompt, handler
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
