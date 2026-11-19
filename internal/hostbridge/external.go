package hostbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ManifestFile is the file that makes a directory a plugin.
const ManifestFile = "plugin.json"

// Manifest describes a plugin that runs as its own program and speaks MCP
// on its standard input and output.
type Manifest struct {
	// Name identifies the plugin: lowercase letters, digits and hyphens.
	Name string `json:"name"`
	// DisplayName is the name shown to the user; Name when empty.
	DisplayName string `json:"displayName,omitempty"`
	Version     string `json:"version"`
	Description string `json:"description"`
	// Engines is required with Contributes.
	Engines *Engines `json:"engines,omitempty"`
	// Kind is host or guest. After parsing it is never empty.
	Kind string `json:"kind,omitempty"`
	// Platforms lists the systems a host plugin runs on, as darwin, linux or
	// windows. Empty means all.
	Platforms []string `json:"platforms,omitempty"`
	// Permissions declares what the plugin does on the computer, for the
	// user to read before enabling it. See KnownPermissions.
	Permissions []string `json:"permissions"`
	// Categories place the plugin in the market's sections, at most three of
	// KnownCategories. Ones this application does not know are kept but not
	// shown, so the list can grow without breaking older applications.
	Categories       []string `json:"categories,omitempty"`
	ActivationEvents []string `json:"activationEvents,omitempty"`
	// Command is the first format's single MCP server. After parsing, read
	// Contributes.MCPServers instead: it holds this one too.
	Command []string `json:"command,omitempty"`
	// Icon is a PNG or SVG picture inside the plugin's directory, shown
	// beside its name. Optional.
	Icon string `json:"icon,omitempty"`
	// Contributes makes the manifest the second format. After parsing it is
	// never nil.
	Contributes *Contributes `json:"contributes,omitempty"`
}

// validIcon reports whether name is a picture path inside the plugin.
func validIcon(name string) bool { return validFile(name, ".png", ".svg") }

// IconLimit is the largest picture shown for a plugin.
const IconLimit = 256 << 10

// ReadIcon returns the picture of the plugin in dir and its media type.
func ReadIcon(dir string) ([]byte, string, error) {
	manifest, err := LoadManifest(dir)
	if err != nil {
		return nil, "", err
	}
	if manifest.Icon == "" {
		return nil, "", os.ErrNotExist
	}
	file := filepath.Join(dir, filepath.FromSlash(manifest.Icon))
	// The picture must be a file of the plugin itself, not a link out of it.
	if info, err := os.Lstat(file); err != nil || !info.Mode().IsRegular() || info.Size() > IconLimit {
		return nil, "", os.ErrNotExist
	}
	data, err := os.ReadFile(file)
	return data, IconType(manifest.Icon), err
}

// IconType is the media type of a picture by its name.
func IconType(name string) string {
	if strings.ToLower(path.Ext(name)) == ".svg" {
		return "image/svg+xml"
	}
	return "image/png"
}

// KnownPermissions are the values a manifest may declare.
var KnownPermissions = []string{"files", "commands", "screen", "input", "network"}

// KnownCategories are the market's sections, in the order they are shown.
var KnownCategories = []string{"desktop", "developer", "system", "network", "files", "productivity", "example"}

// ShownCategories are those of categories this application knows, in their
// given order.
func ShownCategories(categories []string) []string {
	var shown []string
	for _, category := range categories {
		if slices.Contains(KnownCategories, category) && !slices.Contains(shown, category) {
			shown = append(shown, category)
		}
	}
	return shown
}

var pluginName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// LoadManifest reads and checks the manifest of the plugin in dir.
func LoadManifest(dir string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return Manifest{}, err
	}
	return parseManifest(data)
}

// Supported reports whether the plugin runs on this system.
func (m Manifest) Supported() bool {
	return len(m.Platforms) == 0 || slices.Contains(m.Platforms, runtime.GOOS)
}

// ToolRequest is one call to a plugin's tool awaiting approval.
type ToolRequest struct {
	Plugin    string
	Tool      string
	Arguments string
	// FromView says the plugin's own page made the call, not the model.
	FromView bool
}

// RefusedError is a call the user, or the rules, did not let through.
type RefusedError struct{ Reason error }

func (e *RefusedError) Error() string { return e.Reason.Error() }
func (e *RefusedError) Unwrap() error { return e.Reason }

// ErrNotForPage is a page asking for a tool its plugin does not offer pages.
var ErrNotForPage = errors.New("the tool is not offered to pages")

// External is a plugin run as a child program. Its tools are offered under
// their own names once they pass CheckWording; every call first goes
// through Approve, if set.
type External struct {
	Manifest Manifest
	Dir      string
	Approve  func(context.Context, ToolRequest) error
	// Spawn, if set, makes the command that runs the plugin's program
	// somewhere other than this computer; Dir is then only where its files
	// are kept here.
	Spawn func(context.Context) (*exec.Cmd, error)

	mu      sync.Mutex
	session *mcp.ClientSession
	tools   []*mcp.Tool
}

// Start runs the program and reads its tools. It fails, leaving nothing
// running, if a tool's wording is not acceptable.
func (e *External) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.startLocked(ctx)
}

func (e *External) startLocked(ctx context.Context) error {
	if !e.Manifest.Supported() {
		return fmt.Errorf("plugin %s does not support %s", e.Manifest.Name, runtime.GOOS)
	}
	command := e.Manifest.serverCommand()
	if len(command) == 0 {
		return fmt.Errorf("plugin %s has no MCP server", e.Manifest.Name)
	}
	var cmd *exec.Cmd
	if e.Spawn != nil {
		var err error
		if cmd, err = e.Spawn(ctx); err != nil {
			return fmt.Errorf("plugin %s: %w", e.Manifest.Name, err)
		}
	} else {
		program := command[0]
		if !filepath.IsAbs(program) && (filepath.Base(program) != program || fileExists(filepath.Join(e.Dir, program))) {
			program = filepath.Join(e.Dir, program)
		}
		// The program outlives the call that starts it.
		cmd = exec.Command(program, command[1:]...)
		cmd.Dir = e.Dir
		cmd.Env = userEnv()
		hideWindow(cmd)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "computer", Version: "1"}, nil)
	connect, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	session, err := client.Connect(connect, &mcp.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}, nil)
	if err != nil {
		return fmt.Errorf("plugin %s: %w", e.Manifest.Name, err)
	}
	listed, err := session.ListTools(connect, nil)
	if err != nil {
		_ = session.Close()
		return fmt.Errorf("plugin %s: %w", e.Manifest.Name, err)
	}
	for _, tool := range listed.Tools {
		if word, found := CheckWording(tool); found {
			_ = session.Close()
			return fmt.Errorf("plugin %s: tool %s mentions %q; tools describe only what they do", e.Manifest.Name, tool.Name, word)
		}
	}
	e.session, e.tools = session, listed.Tools
	return nil
}

// Tools are the names of the tools the plugin offers the model, once
// started.
func (e *External) Tools() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var names []string
	for _, tool := range e.tools {
		if toolFor(tool, forModel) {
			names = append(names, tool.Name)
		}
	}
	return names
}

// ToolInfo is a tool's name and what it says it does.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// PageOnly says the tool is for the plugin's own page and is not
	// offered to the model.
	PageOnly bool `json:"page_only,omitempty"`
}

// Who a tool is for, as the tool itself says in _meta.ui.visibility, the
// field MCP Apps defines: a list of "model" and "app". A tool that says
// nothing is for both.
const (
	forModel = "model"
	forPage  = "app"
)

func toolFor(tool *mcp.Tool, who string) bool {
	ui, _ := tool.Meta["ui"].(map[string]any)
	list, stated := ui["visibility"].([]any)
	if !stated {
		return true
	}
	return slices.Contains(list, any(who))
}

// ToolList describes the plugin's tools, once started.
func (e *External) ToolList() []ToolInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	list := make([]ToolInfo, len(e.tools))
	for i, tool := range e.tools {
		list[i] = ToolInfo{Name: tool.Name, Description: tool.Description, PageOnly: !toolFor(tool, forModel)}
	}
	return list
}

// Register offers the plugin's tools on server.
func (e *External) Register(server *mcp.Server) {
	e.mu.Lock()
	tools := slices.Clone(e.tools)
	e.mu.Unlock()
	for _, tool := range tools {
		// A tool for the plugin's page alone is not the model's to see or call.
		if !toolFor(tool, forModel) {
			continue
		}
		server.AddTool(tool, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			result, err := e.call(ctx, tool.Name, request.Params.Arguments, false)
			if err != nil {
				refused := &mcp.CallToolResult{}
				refused.SetError(err)
				return refused, nil
			}
			return result, nil
		})
	}
}

// PageTools are the tools the plugin's own page may call, as the plugin
// describes them, once started.
func (e *External) PageTools() []*mcp.Tool {
	e.mu.Lock()
	defer e.mu.Unlock()
	var tools []*mcp.Tool
	for _, tool := range e.tools {
		if toolFor(tool, forPage) {
			tools = append(tools, tool)
		}
	}
	return tools
}

// ErrCallTimedOut is a tool that did not answer within the time it was given.
var ErrCallTimedOut = errors.New("the tool did not answer in time")

// CallFromView calls one of the plugin's tools for the plugin's own page.
// It goes through the same approval as a call from the model. A call not
// let through fails with a RefusedError. The tool has wait to answer once
// the call is let through, or it fails with ErrCallTimedOut; the time the
// user takes to decide does not count.
func (e *External) CallFromView(ctx context.Context, name string, arguments json.RawMessage, wait time.Duration) (*mcp.CallToolResult, error) {
	if !slices.ContainsFunc(e.PageTools(), func(tool *mcp.Tool) bool { return tool.Name == name }) {
		return nil, &RefusedError{fmt.Errorf("plugin %s, tool %s: %w", e.Manifest.Name, name, ErrNotForPage)}
	}
	return e.callWithin(ctx, name, arguments, true, wait)
}

func (e *External) call(ctx context.Context, name string, arguments json.RawMessage, fromView bool) (*mcp.CallToolResult, error) {
	return e.callWithin(ctx, name, arguments, fromView, 0)
}

func (e *External) callWithin(ctx context.Context, name string, arguments json.RawMessage, fromView bool, wait time.Duration) (*mcp.CallToolResult, error) {
	if e.Approve != nil {
		if err := e.Approve(ctx, ToolRequest{Plugin: e.Manifest.Name, Tool: name, Arguments: string(arguments), FromView: fromView}); err != nil {
			return nil, &RefusedError{err}
		}
	}
	if wait > 0 {
		outer := ctx
		limited, cancel := context.WithTimeout(ctx, wait)
		defer cancel()
		ctx = limited
		result, err := e.run(ctx, name, arguments)
		if err != nil && limited.Err() != nil && outer.Err() == nil {
			return nil, ErrCallTimedOut
		}
		return result, err
	}
	return e.run(ctx, name, arguments)
}

// run calls the tool, starting the plugin's program again once if it is gone.
func (e *External) run(ctx context.Context, name string, arguments json.RawMessage) (*mcp.CallToolResult, error) {
	params := &mcp.CallToolParams{Name: name}
	if len(arguments) > 0 {
		params.Arguments = arguments
	}
	e.mu.Lock()
	session := e.session
	e.mu.Unlock()
	if session == nil {
		return nil, fmt.Errorf("plugin %s is not running", e.Manifest.Name)
	}
	result, err := session.CallTool(ctx, params)
	if err == nil || ctx.Err() != nil {
		return result, err
	}
	// The program may have exited; run it again once. Its tools must be the
	// ones already offered, or the call is not what was approved.
	e.mu.Lock()
	if e.session == session {
		_ = session.Close()
		e.session = nil
		offered := e.tools
		if restart := e.startLocked(ctx); restart != nil {
			e.mu.Unlock()
			return nil, errors.Join(err, restart)
		}
		if !sameTools(offered, e.tools) {
			_ = e.session.Close()
			e.session, e.tools = nil, offered
			e.mu.Unlock()
			return nil, fmt.Errorf("plugin %s changed its tools while running", e.Manifest.Name)
		}
	}
	session = e.session
	e.mu.Unlock()
	if session == nil {
		return nil, err
	}
	return session.CallTool(ctx, params)
}

// Close stops the program.
func (e *External) Close() {
	e.mu.Lock()
	session := e.session
	e.session = nil
	e.mu.Unlock()
	if session != nil {
		_ = session.Close()
	}
}

func sameTools(a, b []*mcp.Tool) bool {
	return slices.EqualFunc(a, b, func(x, y *mcp.Tool) bool {
		return x.Name == y.Name && x.Description == y.Description
	})
}

// LoadOptions says which plugins LoadPlugins starts and how.
type LoadOptions struct {
	// Taken are tool names already in use, as those of the built-in plugins.
	Taken []string
	// Off names the plugins the user turned off.
	Off []string
	// Version is this application's, for the plugins' engines ranges.
	Version string
	Approve func(context.Context, ToolRequest) error
	// Guest makes the command that runs a guest plugin's server, given its
	// manifest and where its files are here. Nil where there is nowhere to
	// run one.
	Guest func(ctx context.Context, manifest Manifest, dir string) (*exec.Cmd, error)
	// GuestOnly leaves the plugins that run on this computer unstarted.
	GuestOnly bool
}

// LoadPlugins starts every plugin found in the subdirectories of dir that
// supports this system. A plugin that fails to load, or offers a tool name
// already taken, is left out and reported; the others still load. Not
// started either: those turned off, those this application cannot run (see
// Unavailable), and a plugin handling a link scheme that one loaded before
// it already handles.
func LoadPlugins(ctx context.Context, dir string, options LoadOptions) ([]*External, []error) {
	taken, off, version, approve := options.Taken, options.Off, options.Version, options.Approve
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{err}
	}
	var loaded []*External
	var failures []error
	names := slices.Clone(taken)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		manifest, err := LoadManifest(path)
		if err != nil {
			if !os.IsNotExist(err) {
				failures = append(failures, fmt.Errorf("%s: %w", entry.Name(), err))
			}
			continue
		}
		if manifest.Name != entry.Name() {
			failures = append(failures, fmt.Errorf("%s: the directory must be named after the plugin, %s", entry.Name(), manifest.Name))
			continue
		}
		if !manifest.Supported() || slices.Contains(off, manifest.Name) || manifest.Unavailable(version, true) == NeedsNewerApplication {
			continue
		}
		if manifest.Unavailable(version, options.Guest != nil) != "" || len(manifest.serverCommand()) == 0 {
			continue
		}
		if options.GuestOnly && manifest.Kind != KindGuest {
			continue
		}
		plugin := &External{Manifest: manifest, Dir: path, Approve: approve}
		if manifest.Kind == KindGuest {
			plugin.Spawn = func(ctx context.Context) (*exec.Cmd, error) { return options.Guest(ctx, manifest, path) }
		}
		if err := plugin.Start(ctx); err != nil {
			failures = append(failures, err)
			continue
		}
		clash := ""
		for _, tool := range plugin.Tools() {
			if slices.Contains(names, tool) {
				clash = tool
				break
			}
		}
		if clash != "" {
			plugin.Close()
			failures = append(failures, fmt.Errorf("plugin %s: tool name %s is already in use", manifest.Name, clash))
			continue
		}
		names = append(names, plugin.Tools()...)
		loaded = append(loaded, plugin)
	}
	return loaded, failures
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
