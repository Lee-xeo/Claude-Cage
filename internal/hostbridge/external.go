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

// ProjectDirPlaceholder is replaced in plugin command arguments with the active
// project directory (the host path for host plugins, the guest workspace path
// for guest plugins).
const ProjectDirPlaceholder = "${PROJECT_DIR}"

// ErrNoProjectDir is returned when a plugin's command uses ProjectDirPlaceholder
// but no project folder is currently open.
var ErrNoProjectDir = errors.New("no project folder is open")

// MissingProgramError reports that a host plugin's runtime program (such as uv,
// uvx, npx, node or python3) could not be found on PATH.
type MissingProgramError struct {
	Plugin  string
	Program string
	Err     error
}

func (e *MissingProgramError) Error() string {
	hint := programInstallHint(e.Program)
	if hint != "" {
		return fmt.Sprintf("plugin %s: %s was not found on PATH (%s)", e.Plugin, e.Program, hint)
	}
	return fmt.Sprintf("plugin %s: %s was not found on PATH", e.Plugin, e.Program)
}

func (e *MissingProgramError) Unwrap() error { return e.Err }

func programInstallHint(program string) string {
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(program), ".exe"))
	switch base {
	case "uv", "uvx":
		return "install uv from https://docs.astral.sh/uv/ and reload plugins"
	case "npx", "node", "npm":
		return "install Node.js from https://nodejs.org/ and reload plugins"
	case "python3", "python":
		return "install Python 3 from https://www.python.org/ and reload plugins"
	default:
		return ""
	}
}

// MissingSettingsError reports that a plugin is not started because settings
// it requires have no value.
type MissingSettingsError struct {
	Plugin string
	// Settings are the titles of the settings to fill in.
	Settings []string
}

func (e *MissingSettingsError) Error() string {
	return fmt.Sprintf("plugin %s needs these settings: %s", e.Plugin, strings.Join(e.Settings, ", "))
}

// MissingProjectDirError reports that a plugin needs an open project folder to
// expand ProjectDirPlaceholder.
type MissingProjectDirError struct {
	Plugin string
}

func (e *MissingProjectDirError) Error() string {
	return fmt.Sprintf("plugin %s requires an open project folder (%s)", e.Plugin, ProjectDirPlaceholder)
}

func (e *MissingProjectDirError) Unwrap() error { return ErrNoProjectDir }

// CommandUsesProjectDir reports whether any argument in command references
// ProjectDirPlaceholder.
func CommandUsesProjectDir(command []string) bool {
	for _, arg := range command[min(1, len(command)):] {
		if strings.Contains(arg, ProjectDirPlaceholder) {
			return true
		}
	}
	return false
}

// ExpandCommand returns a copy of command with ProjectDirPlaceholder in
// arguments (never the executable name itself) replaced by projectDir.
func ExpandCommand(command []string, projectDir string) ([]string, error) {
	if len(command) == 0 {
		return nil, nil
	}
	out := slices.Clone(command)
	for i := 1; i < len(out); i++ {
		if !strings.Contains(out[i], ProjectDirPlaceholder) {
			continue
		}
		if strings.TrimSpace(projectDir) == "" {
			return nil, ErrNoProjectDir
		}
		out[i] = strings.ReplaceAll(out[i], ProjectDirPlaceholder, projectDir)
	}
	return out, nil
}

// External is a plugin run as a child program. Its tools are offered under
// their own names once they pass CheckWording; every call first goes
// through Approve, if set.
type External struct {
	Manifest Manifest
	Dir      string
	// ProjectDir is substituted for ${PROJECT_DIR} in host plugin arguments.
	ProjectDir string
	Approve    func(context.Context, ToolRequest) error
	// Env, if set, gives the variables added for the plugin's program on
	// this computer, read again each time it starts.
	Env func(context.Context) ([]string, error)
	// Spawn, if set, makes the command that runs the plugin's program
	// somewhere other than this computer; Dir is then only where its files
	// are kept here.
	Spawn func(context.Context) (*exec.Cmd, error)

	mu      sync.Mutex
	session *mcp.ClientSession
	tools   []*mcp.Tool
	prompts []*mcp.Prompt
	// restarting serializes Restart, which starts the program outside mu.
	restarting sync.Mutex
}

// Start runs the program and reads its tools and prompts. It fails, leaving
// nothing running, if a tool or prompt's wording is not acceptable.
func (e *External) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.startLocked(ctx)
}

func (e *External) startLocked(ctx context.Context) error {
	session, tools, prompts, err := e.open(ctx, e.ProjectDir)
	if err != nil {
		return err
	}
	e.session, e.tools, e.prompts = session, tools, prompts
	return nil
}

// UsesProjectDir reports whether the plugin's server command takes
// ProjectDirPlaceholder, so it has to run again when the project changes.
func (e *External) UsesProjectDir() bool {
	return CommandUsesProjectDir(e.Manifest.serverCommand())
}

// Running reports whether the plugin's program is up.
func (e *External) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.session != nil
}

// Restart stops the program and runs it again, as for a new project folder.
// projectDir, if set, gives the folder when the program starts. Calls to
// this plugin fail while it restarts; other plugins are not affected, and
// its tools stay readable meanwhile. On failure nothing runs and the plugin
// offers no tools until it is restarted.
func (e *External) Restart(ctx context.Context, projectDir func() string) error {
	e.restarting.Lock()
	defer e.restarting.Unlock()
	e.mu.Lock()
	old := e.session
	e.session = nil
	if projectDir != nil {
		e.ProjectDir = projectDir()
	}
	dir := e.ProjectDir
	e.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	session, tools, prompts, err := e.open(ctx, dir)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		e.tools, e.prompts = nil, nil
		return err
	}
	e.session, e.tools, e.prompts = session, tools, prompts
	return nil
}

// open runs the program for projectDir and reads its tools and prompts. It
// fails, leaving nothing running, if a tool or prompt's wording is not acceptable.
func (e *External) open(ctx context.Context, projectDir string) (*mcp.ClientSession, []*mcp.Tool, []*mcp.Prompt, error) {
	if !e.Manifest.Supported() {
		return nil, nil, nil, fmt.Errorf("plugin %s does not support %s", e.Manifest.Name, runtime.GOOS)
	}
	command := e.Manifest.serverCommand()
	if len(command) == 0 {
		return nil, nil, nil, fmt.Errorf("plugin %s has no MCP server", e.Manifest.Name)
	}
	var cmd *exec.Cmd
	bareProgram := ""
	if e.Spawn != nil {
		var err error
		if cmd, err = e.Spawn(ctx); err != nil {
			if errors.Is(err, ErrNoProjectDir) {
				return nil, nil, nil, &MissingProjectDirError{Plugin: e.Manifest.Name}
			}
			if missing, ok := errors.AsType[*MissingSettingsError](err); ok {
				return nil, nil, nil, missing
			}
			return nil, nil, nil, fmt.Errorf("plugin %s: %w", e.Manifest.Name, err)
		}
	} else {
		expanded, err := ExpandCommand(command, projectDir)
		if err != nil {
			if errors.Is(err, ErrNoProjectDir) {
				return nil, nil, nil, &MissingProjectDirError{Plugin: e.Manifest.Name}
			}
			return nil, nil, nil, fmt.Errorf("plugin %s: %w", e.Manifest.Name, err)
		}
		program := expanded[0]
		localFile := !filepath.IsAbs(program) && (filepath.Base(program) != program || fileExists(filepath.Join(e.Dir, program)))
		if localFile {
			program = filepath.Join(e.Dir, program)
		} else if filepath.Base(program) == program {
			bareProgram = program
		}
		env := userEnv()
		if e.Env != nil {
			extra, err := e.Env(ctx)
			if err != nil {
				if missing, ok := errors.AsType[*MissingSettingsError](err); ok {
					return nil, nil, nil, missing
				}
				return nil, nil, nil, fmt.Errorf("plugin %s: %w", e.Manifest.Name, err)
			}
			env = withEnv(env, extra)
		}
		// A bare name is found on the PATH the program is given, which may
		// be longer than this process's own.
		args := expanded[1:]
		if filepath.Base(program) == program {
			resolved, prefix, err := resolveHostCommand(ctx, program, env)
			if err != nil {
				if bareProgram != "" && (errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)) {
					return nil, nil, nil, &MissingProgramError{Plugin: e.Manifest.Name, Program: bareProgram, Err: err}
				}
				return nil, nil, nil, fmt.Errorf("plugin %s: %w", e.Manifest.Name, err)
			}
			program = resolved
			if len(prefix) > 0 {
				args = append(slices.Clone(prefix), args...)
			}
		}
		// The program outlives the call that starts it.
		cmd = exec.Command(program, args...)
		cmd.Dir = e.Dir
		cmd.Env = env
		hideWindow(cmd)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "computer", Version: "1"}, nil)
	connect, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	session, err := client.Connect(connect, &mcp.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}, nil)
	if err != nil {
		if bareProgram != "" && (errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)) {
			return nil, nil, nil, &MissingProgramError{Plugin: e.Manifest.Name, Program: bareProgram, Err: err}
		}
		return nil, nil, nil, fmt.Errorf("plugin %s: %w", e.Manifest.Name, err)
	}
	listed, err := session.ListTools(connect, nil)
	if err != nil {
		_ = session.Close()
		return nil, nil, nil, fmt.Errorf("plugin %s: %w", e.Manifest.Name, err)
	}
	for _, tool := range listed.Tools {
		if word, found := CheckWording(tool); found {
			_ = session.Close()
			return nil, nil, nil, fmt.Errorf("plugin %s: tool %s mentions %q; tools describe only what they do", e.Manifest.Name, tool.Name, word)
		}
	}
	var prompts []*mcp.Prompt
	if initRes := session.InitializeResult(); initRes != nil && initRes.Capabilities != nil && initRes.Capabilities.Prompts != nil {
		listedPrompts, err := session.ListPrompts(connect, nil)
		if err != nil {
			_ = session.Close()
			return nil, nil, nil, fmt.Errorf("plugin %s: %w", e.Manifest.Name, err)
		}
		seenPrompts := map[string]bool{}
		for _, prompt := range listedPrompts.Prompts {
			if prompt == nil || strings.TrimSpace(prompt.Name) == "" || seenPrompts[prompt.Name] {
				continue
			}
			if word, found := CheckPromptWording(prompt); found {
				_ = session.Close()
				return nil, nil, nil, fmt.Errorf("plugin %s: prompt %s mentions %q; prompts describe only what they do", e.Manifest.Name, prompt.Name, word)
			}
			seenPrompts[prompt.Name] = true
			prompts = append(prompts, prompt)
		}
	}
	return session, listed.Tools, prompts, nil
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

// Prompts are the names of the prompts the plugin offers the model, once
// started. This includes prompts declared by the plugin itself and an
// automatic slash prompt for plugins that expose model tools.
func (e *External) Prompts() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var names []string
	for _, prompt := range e.prompts {
		names = append(names, prompt.Name)
	}
	if fallback, _ := e.defaultPromptLocked(); fallback != nil {
		names = append(names, fallback.Name)
	}
	return names
}

// PromptList describes the plugin's prompts, once started.
func (e *External) PromptList() []PromptInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	list := make([]PromptInfo, 0, len(e.prompts)+1)
	for _, prompt := range e.prompts {
		list = append(list, PromptInfo{Name: prompt.Name, Description: prompt.Description})
	}
	if fallback, _ := e.defaultPromptLocked(); fallback != nil {
		list = append(list, PromptInfo{Name: fallback.Name, Description: fallback.Description})
	}
	return list
}

func (e *External) defaultPromptLocked() (*mcp.Prompt, mcp.PromptHandler) {
	var modelTools []string
	for _, tool := range e.tools {
		if toolFor(tool, forModel) {
			modelTools = append(modelTools, tool.Name)
		}
	}
	if len(modelTools) == 0 {
		return nil, nil
	}
	for _, prompt := range e.prompts {
		if prompt.Name == e.Manifest.Name {
			return nil, nil
		}
	}
	return buildPluginPrompt(e.Manifest.Name, e.Manifest.DisplayName, e.Manifest.Description, modelTools)
}

// Register offers the plugin's tools and prompts on server.
func (e *External) Register(server *mcp.Server) {
	e.mu.Lock()
	tools := slices.Clone(e.tools)
	prompts := slices.Clone(e.prompts)
	fallbackPrompt, fallbackHandler := e.defaultPromptLocked()
	e.mu.Unlock()
	for _, tool := range tools {
		// A tool for the plugin's page alone is not the model's to see or call.
		if !toolFor(tool, forModel) {
			continue
		}
		toolName := tool.Name
		server.AddTool(tool, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			result, err := e.call(ctx, toolName, request.Params.Arguments, false)
			if err != nil {
				refused := &mcp.CallToolResult{}
				refused.SetError(err)
				return refused, nil
			}
			return result, nil
		})
	}
	for _, prompt := range prompts {
		promptName := prompt.Name
		server.AddPrompt(prompt, func(ctx context.Context, request *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			var args map[string]string
			if request != nil && request.Params != nil {
				args = request.Params.Arguments
			}
			return e.getPrompt(ctx, promptName, args)
		})
	}
	if fallbackPrompt != nil && fallbackHandler != nil {
		server.AddPrompt(fallbackPrompt, fallbackHandler)
	}
}

// ModelTools are the tools the plugin offers the model, once started.
func (e *External) ModelTools() []*mcp.Tool {
	e.mu.Lock()
	defer e.mu.Unlock()
	var tools []*mcp.Tool
	for _, tool := range e.tools {
		if toolFor(tool, forModel) {
			tools = append(tools, tool)
		}
	}
	return tools
}

// CallForModel calls one of the plugin's model-visible tools through the
// plugin's normal approval gate.
func (e *External) CallForModel(ctx context.Context, name string, arguments json.RawMessage) (*mcp.CallToolResult, error) {
	if !slices.ContainsFunc(e.ModelTools(), func(tool *mcp.Tool) bool { return tool.Name == name }) {
		return nil, &RefusedError{fmt.Errorf("plugin %s, tool %s: not offered to the model", e.Manifest.Name, name)}
	}
	return e.call(ctx, name, arguments, false)
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
		offered, offeredPrompts := e.tools, e.prompts
		if restart := e.startLocked(ctx); restart != nil {
			e.mu.Unlock()
			return nil, errors.Join(err, restart)
		}
		if !sameTools(offered, e.tools) || !samePrompts(offeredPrompts, e.prompts) {
			_ = e.session.Close()
			e.session, e.tools, e.prompts = nil, offered, offeredPrompts
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

// getPrompt fetches a prompt declared by the plugin's own server, restarting
// the program once if it exited, and refuses responses containing reserved wording.
func (e *External) getPrompt(ctx context.Context, name string, arguments map[string]string) (*mcp.GetPromptResult, error) {
	params := &mcp.GetPromptParams{Name: name, Arguments: arguments}
	e.mu.Lock()
	session := e.session
	e.mu.Unlock()
	if session == nil {
		return nil, fmt.Errorf("plugin %s is not running", e.Manifest.Name)
	}
	result, err := session.GetPrompt(ctx, params)
	if err != nil && ctx.Err() == nil {
		e.mu.Lock()
		if e.session == session {
			_ = session.Close()
			e.session = nil
			offered, offeredPrompts := e.tools, e.prompts
			if restart := e.startLocked(ctx); restart != nil {
				e.mu.Unlock()
				return nil, errors.Join(err, restart)
			}
			if !sameTools(offered, e.tools) || !samePrompts(offeredPrompts, e.prompts) {
				_ = e.session.Close()
				e.session, e.tools, e.prompts = nil, offered, offeredPrompts
				e.mu.Unlock()
				return nil, fmt.Errorf("plugin %s changed its tools while running", e.Manifest.Name)
			}
		}
		session = e.session
		e.mu.Unlock()
		if session == nil {
			return nil, err
		}
		result, err = session.GetPrompt(ctx, params)
	}
	if err != nil {
		return nil, err
	}
	if word, found := CheckPromptResultWording(result); found {
		return nil, fmt.Errorf("prompt %s returned disallowed wording %q", name, word)
	}
	return result, nil
}

// Withdraw stops the program and offers none of its tools or prompts until it
// is restarted.
func (e *External) Withdraw() {
	e.mu.Lock()
	session := e.session
	e.session, e.tools, e.prompts = nil, nil, nil
	e.mu.Unlock()
	if session != nil {
		_ = session.Close()
	}
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

func samePrompts(a, b []*mcp.Prompt) bool {
	return slices.EqualFunc(a, b, func(x, y *mcp.Prompt) bool {
		return x.Name == y.Name && x.Description == y.Description
	})
}

// LoadOptions says which plugins LoadPlugins starts and how.
type LoadOptions struct {
	// Taken are tool names already in use, as those of the built-in plugins.
	Taken []string
	// TakenPrompts are prompt names already in use, as those of the built-in plugins.
	TakenPrompts []string
	// Off names the plugins the user turned off.
	Off []string
	// Version is this application's, for the plugins' engines ranges.
	Version string
	// ProjectDir is the active project folder on this computer, substituted for
	// ${PROJECT_DIR} in host plugin arguments.
	ProjectDir string
	Approve    func(context.Context, ToolRequest) error
	// Env gives the variables from the user's settings for a plugin that
	// runs on this computer; see External.Env.
	Env func(ctx context.Context, manifest Manifest) ([]string, error)
	// Guest makes the command that runs a guest plugin's server, given its
	// manifest and where its files are here. Nil where there is nowhere to
	// run one.
	Guest func(ctx context.Context, manifest Manifest, dir string) (*exec.Cmd, error)
	// GuestOnly leaves the plugins that run on this computer unstarted.
	GuestOnly bool
}

// LoadPlugins starts every plugin found in the subdirectories of dir that
// supports this system. A plugin that fails to load, or offers a tool or prompt
// name already taken, is left out and reported; the others still load. Not
// started either: those turned off, those this application cannot run (see
// Unavailable), and a plugin handling a link scheme that one loaded before
// it already handles. A plugin that needs a project folder while none is
// open is reported and returned without running, so it can be restarted.
func LoadPlugins(ctx context.Context, dir string, options LoadOptions) ([]*External, []error) {
	taken, takenPrompts, off, version, approve := options.Taken, options.TakenPrompts, options.Off, options.Version, options.Approve
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
	promptNames := slices.Clone(takenPrompts)
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
		plugin := &External{Manifest: manifest, Dir: path, ProjectDir: options.ProjectDir, Approve: approve}
		if manifest.Kind == KindGuest {
			plugin.Spawn = func(ctx context.Context) (*exec.Cmd, error) { return options.Guest(ctx, manifest, path) }
		} else if options.Env != nil {
			plugin.Env = func(ctx context.Context) ([]string, error) { return options.Env(ctx, manifest) }
		}
		if err := plugin.Start(ctx); err != nil {
			failures = append(failures, err)
			// One that needs a project folder waits, not running, for the
			// next project to be opened (see Restart).
			if _, waiting := errors.AsType[*MissingProjectDirError](err); waiting {
				loaded = append(loaded, plugin)
			}
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
		promptClash := ""
		for _, prompt := range plugin.Prompts() {
			if slices.Contains(promptNames, prompt) {
				promptClash = prompt
				break
			}
		}
		if promptClash != "" {
			plugin.Close()
			failures = append(failures, fmt.Errorf("plugin %s: prompt name %s is already in use", manifest.Name, promptClash))
			continue
		}
		names = append(names, plugin.Tools()...)
		promptNames = append(promptNames, plugin.Prompts()...)
		loaded = append(loaded, plugin)
	}
	return loaded, failures
}

// withEnv is env with the variables of extra set, replacing any of the same
// name. Windows does not tell names apart by case.
func withEnv(env, extra []string) []string {
	if len(extra) == 0 {
		return env
	}
	same := func(a, b string) bool {
		if runtime.GOOS == "windows" {
			return strings.EqualFold(a, b)
		}
		return a == b
	}
	out := slices.Clone(env)
	for _, entry := range extra {
		name, _, _ := strings.Cut(entry, "=")
		out = slices.DeleteFunc(out, func(old string) bool {
			oldName, _, _ := strings.Cut(old, "=")
			return same(oldName, name)
		})
		out = append(out, entry)
	}
	return out
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
