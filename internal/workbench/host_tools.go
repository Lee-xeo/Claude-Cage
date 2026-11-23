package workbench

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/hostbridge"
	"github.com/sylvinus/agent-vm/internal/paths"
	"github.com/sylvinus/agent-vm/internal/version"
)

// hostToolsPort is where the environment reaches the tools, on its own
// loopback. It and the token stay the same across restarts so the entry in
// Claude's configuration is written once and then left alone: Claude rewrites
// that file itself, and a second writer would race with it.
const hostToolsPort = 18962

//go:embed host_tools_config.py
var hostToolsConfigScript string

// hostToolsRelayScript lets the desktop app, which can only start a server as
// a local program, reach the same tools.
//
//go:embed host_tools_relay.py
var hostToolsRelayScript string

// hostTools is the running tool server and the SSH process that forwards the
// environment's port to it.
type hostTools struct {
	bridge  *hostbridge.Bridge
	control *workspaceControlBridge
	plugins []*hostbridge.External
	jobs    *hostbridge.Jobs
	// problems are the reasons plugins were left out, for the user to read.
	problems        []string
	pluginProblems  map[string]string
	missingPrograms map[string]string
	cancel          context.CancelFunc
	// ctx ends when these tools are closed.
	ctx  context.Context
	done chan struct{}
	// configured is when the environment's clients were last given the entry.
	configured time.Time
	// serving is false when the tools for Claude are turned off: plugins
	// inside the environment still run, for their own pages, but there is
	// no server, no forward and nothing on this computer is started.
	serving bool
	// awaitingUnlock says a plugin was stopped, or not started, because Cage
	// is locked and the plugin receives secrets; unlocking starts them again.
	awaitingUnlock bool
}

// hostCommandsPlugin is the name of the built-in plugin that runs commands.
const hostCommandsPlugin = "commands"

func hostPluginsOff(p Preferences) []string {
	if p.HostPluginsOff == "" {
		return nil
	}
	return strings.Split(p.HostPluginsOff, ",")
}

// hostToolsConfigInterval is how often a running environment is checked for
// Claude profiles created since the entry was written.
const hostToolsConfigInterval = time.Minute

func (h *hostTools) close() {
	h.cancel()
	<-h.done
	if h.bridge != nil {
		_ = h.bridge.Close()
	}
	if h.control != nil {
		h.control.Close()
	}
	for _, plugin := range h.plugins {
		plugin.Close()
	}
	if h.jobs != nil {
		h.jobs.Close()
	}
}

// ensureHostTools brings what runs in line with the preference. With it on,
// the tools are served to Claude; with it off, only the plugins inside the
// environment run, for their own pages. Called under the lifecycle lock
// whenever the environment is known to be reachable, so a forward that
// dropped is replaced on the next check.
func (s *Server) ensureHostTools() {
	if s.clientOnly {
		return
	}
	s.mu.Lock()
	enabled, closing, ready, current := s.preferences.HostTools, s.closing, s.ready, s.hostTools
	s.mu.Unlock()
	if closing {
		s.closeHostTools()
		return
	}
	if !enabled {
		s.removeHostToolsConfig()
	}
	if current != nil {
		dropped := false
		select {
		case <-current.done:
			dropped = current.serving
		default:
		}
		if current.serving == enabled && !dropped {
			if enabled {
				s.refreshHostToolsConfig(current)
			}
			return
		}
		s.closeHostTools()
	}
	// Plugins inside the environment need it reachable; with the tools for
	// Claude on, starting has always been attempted regardless.
	if !enabled && !ready {
		return
	}
	if err := s.startHostTools(enabled); err != nil {
		diagnostics.Log("warn", "host-tools", fmt.Sprintf("start error=%v", err))
	}
}

func (s *Server) startHostTools(serving bool) error {
	token, err := s.hostToolsToken()
	if err != nil {
		return err
	}
	approvals := newHostApprovals()
	approvals.remoteAsk = s.remoteApprovals.ask
	approvals.neverAlways[workspaceControlPlugin+"/"+workspaceControlManageTool] = true
	// Preferences include the command every new terminal runs.
	approvals.neverAlways[workspaceControlPlugin+"/"+workspaceControlPreferencesTool] = true
	jobs := hostbridge.NewJobs()
	ctx, cancel := context.WithCancel(s.lifetimeContext())
	// Plugins the user installed run beside the built-in command tool. One
	// that fails to load is left out; the rest still work.
	s.mu.Lock()
	off := hostPluginsOff(s.preferences)
	s.mu.Unlock()
	var control *workspaceControlBridge
	if serving && !slices.Contains(off, workspaceControlPlugin) {
		if _, statErr := os.Lstat(filepath.Join(s.profile, "plugins", workspaceControlPlugin)); statErr == nil {
			if started, ctrlErr := s.startWorkspaceControlBridge(); ctrlErr == nil {
				control = started
			} else {
				diagnostics.Log("warn", "host-tools", fmt.Sprintf("workspace-control bridge error=%v", ctrlErr))
			}
		}
	}
	external, failures := hostbridge.LoadPlugins(ctx, filepath.Join(s.profile, "plugins"), hostbridge.LoadOptions{
		Taken: []string{"run_command", "command_job"}, TakenPrompts: []string{hostCommandsPlugin}, Off: off, Version: version.Version,
		ProjectDir: s.hostActiveProjectDir(),
		Approve:    approvals.approveTool, Guest: s.guestPluginCommand, GuestOnly: !serving, Env: s.workspaceControlPluginEnv(control)})
	var problems []string
	pluginProblems := map[string]string{}
	missingPrograms := map[string]string{}
	awaitingUnlock := false
	for _, failure := range failures {
		diagnostics.Log("warn", "host-tools", fmt.Sprintf("plugin error=%v", failure))
		var missing *hostbridge.MissingProgramError
		var noProject *hostbridge.MissingProjectDirError
		var noSettings *hostbridge.MissingSettingsError
		switch {
		case errors.As(failure, &noSettings):
			msg := pluginSettingsMissingMessage(noSettings.Settings)
			pluginProblems[noSettings.Plugin] = msg
			problems = append(problems, noSettings.Plugin+"："+msg)
		case errors.As(failure, &missing):
			missingPrograms[missing.Plugin] = missing.Program
			msg := missingProgramMessage(missing.Program)
			pluginProblems[missing.Plugin] = msg
			problems = append(problems, missing.Plugin+"："+msg)
		case errors.As(failure, &noProject):
			msg := hostPluginNeedsProject
			pluginProblems[noProject.Plugin] = msg
			problems = append(problems, noProject.Plugin+"："+msg)
		default:
			awaitingUnlock = awaitingUnlock || errors.Is(failure, errPluginSecretsLocked)
			text := failure.Error()
			problems = append(problems, text)
			if rest, ok := strings.CutPrefix(text, "plugin "); ok {
				if name, detail, found := strings.Cut(rest, ": "); found {
					pluginProblems[name] = detail
				}
			}
		}
	}
	var plugins []hostbridge.Plugin
	if !slices.Contains(off, hostCommandsPlugin) {
		plugins = append(plugins, hostbridge.Command{Dirs: s.hostToolDirs, MapDir: paths.FromGitBash, Approve: approvals.approve, Jobs: jobs})
	}
	for _, plugin := range external {
		plugins = append(plugins, plugin)
	}
	// What each plugin declares decides whether a grant still covers it.
	approvals.grants, approvals.permissions = s.hostGrantStore(), map[string][]string{}
	for _, plugin := range external {
		approvals.permissions[plugin.Manifest.Name] = plugin.Manifest.Permissions
		// A plugin that stays inside the environment, declaring nothing
		// that reaches out of it, acts only where Claude already does.
		if plugin.Manifest.Kind == hostbridge.KindGuest && len(plugin.Manifest.Permissions) == 0 {
			approvals.unasked[plugin.Manifest.Name] = true
		}
	}
	tools := &hostTools{cancel: cancel, ctx: ctx, done: make(chan struct{}), control: control, plugins: external, jobs: jobs,
		problems: problems, pluginProblems: pluginProblems, missingPrograms: missingPrograms, serving: serving, awaitingUnlock: awaitingUnlock}
	s.stopSecretPluginsOnLock(tools)
	if !serving {
		// Nothing is served and nothing forwarded; done only marks the end.
		close(tools.done)
		s.mu.Lock()
		s.hostTools = tools
		s.mu.Unlock()
		diagnostics.Log("info", "host-tools", fmt.Sprintf("started pages only plugins=%d left_out=%d", len(external), len(problems)))
		return nil
	}
	bridge, err := hostbridge.Start(token, plugins...)
	if err != nil {
		close(tools.done)
		tools.close()
		return err
	}
	tools.bridge = bridge
	s.reattachClientPluginBridge(bridge)
	forward := fmt.Sprintf("127.0.0.1:%d:%s", hostToolsPort, bridge.Addr())
	s.releaseHostToolsPort(ctx)
	cmd := s.sshCommandWithForwarding(ctx, "-o", "ExitOnForwardFailure=yes", "-N", "-T", "-R", forward, s.sshTarget())
	// Ask SSH to end the connection itself. Killed outright, it leaves the
	// environment holding the port for minutes, and no new forward can bind.
	cmd.Cancel = func() error {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 3 * time.Second
	if err = cmd.Start(); err != nil {
		close(tools.done)
		tools.close()
		return err
	}
	diagnostics.Log("info", "host-tools", fmt.Sprintf("started plugins=%d left_out=%d", len(external), len(problems)))
	go func() {
		defer close(tools.done)
		// Recorded so a forward that ended by itself can be told from one
		// this application stopped.
		diagnostics.Log("info", "host-tools", fmt.Sprintf("forward ended stopped=%t error=%v", ctx.Err() != nil, cmd.Wait()))
	}()
	configure, finish := context.WithTimeout(ctx, 15*time.Second)
	defer finish()
	if err = s.writeHostToolsConfig(configure, token, "add"); err != nil {
		tools.close()
		return err
	}
	tools.configured = time.Now()
	s.mu.Lock()
	s.hostTools = tools
	s.hostToolsRemoved = false
	s.mu.Unlock()
	return nil
}

// hostToolsReleaseScript ends the SSH session in the environment that still
// holds the port for a forward whose other end is gone, as after this
// application was stopped abruptly. Anything else on the port is left alone.
// Only root can see which process holds another session's socket.
const hostToolsReleaseScript = `pid=$(sudo -n ss -H -ltnp 'sport = :%d' 2>/dev/null | sed -n 's/.*pid=\([0-9]*\).*/\1/p' | head -n 1)
case "$(cat "/proc/$pid/comm" 2>/dev/null)" in sshd*) kill "$pid" ;; esac
exit 0`

func (s *Server) releaseHostToolsPort(ctx context.Context) {
	release, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := s.sshCommand(release, "-T", s.sshTarget(), "--", "exec /bin/sh")
	cmd.Stdin = strings.NewReader(fmt.Sprintf(hostToolsReleaseScript, hostToolsPort))
	if err := cmd.Run(); err != nil {
		diagnostics.Log("warn", "host-tools", fmt.Sprintf("release error=%v", err))
	}
}

// stopSecretPluginsOnLock stops, when Cage locks, the plugins whose programs
// received secrets, as a secret is readable only while Cage is unlocked.
// Unlocking again starts them; see restartPluginsAfterUnlock.
func (s *Server) stopSecretPluginsOnLock(tools *hostTools) {
	epoch, unlocked := s.account.access()
	if !unlocked || epoch == nil {
		return
	}
	for _, plugin := range tools.plugins {
		if !s.pluginUsesSecrets(plugin.Manifest) {
			continue
		}
		name := plugin.Manifest.Name
		stop := context.AfterFunc(epoch, func() {
			if tools.ctx.Err() != nil {
				return
			}
			plugin.Withdraw()
			s.recordProjectPluginProblem(tools, name, errPluginSecretsLocked)
			s.mu.Lock()
			tools.awaitingUnlock = true
			s.mu.Unlock()
		})
		context.AfterFunc(tools.ctx, func() { stop() })
	}
}

// restartPluginsAfterUnlock starts again, in the background, the plugins
// that stopped or could not start because Cage was locked.
func (s *Server) restartPluginsAfterUnlock() {
	s.mu.Lock()
	tools := s.hostTools
	waiting := tools != nil && tools.awaitingUnlock
	s.mu.Unlock()
	if !waiting {
		return
	}
	go func() {
		s.op.Lock()
		defer s.op.Unlock()
		s.mu.Lock()
		current, closing := s.hostTools, s.closing
		s.mu.Unlock()
		if current != tools || closing {
			return
		}
		s.closeHostTools()
		s.ensureHostTools()
	}()
}

// hostToolsServing reports whether host tools are currently served to Claude.
func (s *Server) hostToolsServing() bool {
	s.mu.Lock()
	serving := s.hostTools != nil && s.hostTools.serving
	s.mu.Unlock()
	return serving
}

// syncHostToolsConfigNow immediately writes the MCP entry to all existing
// Claude Code and Desktop profiles when host tools are active, so a newly
// started Desktop sees the tools on its first launch without waiting for the
// periodic refresh or requiring a restart.
func (s *Server) syncHostToolsConfigNow(parent context.Context) {
	if s.clientOnly || !s.hostToolsServing() {
		return
	}
	token, err := s.hostToolsToken()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	if err = s.writeHostToolsConfig(ctx, token, "add"); err != nil {
		diagnostics.Log("warn", "host-tools", fmt.Sprintf("sync error=%v", err))
	}
}

// refreshHostToolsConfig gives the entry to profiles created while the
// environment runs. Unchanged files are not rewritten.
func (s *Server) refreshHostToolsConfig(tools *hostTools) {
	if time.Since(tools.configured) < hostToolsConfigInterval {
		return
	}
	tools.configured = time.Now()
	token, err := s.hostToolsToken()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(s.lifetimeContext(), 15*time.Second)
	defer cancel()
	if err = s.writeHostToolsConfig(ctx, token, "add"); err != nil {
		diagnostics.Log("warn", "host-tools", fmt.Sprintf("refresh error=%v", err))
	}
}

// removeHostToolsConfig takes the entry back out of the environment once
// after the preference is turned off, so clients do not keep a server that
// no longer answers.
func (s *Server) removeHostToolsConfig() {
	s.mu.Lock()
	removed, ready := s.hostToolsRemoved, s.ready
	s.mu.Unlock()
	if removed || !ready {
		return
	}
	ctx, cancel := context.WithTimeout(s.lifetimeContext(), 15*time.Second)
	defer cancel()
	if err := s.writeHostToolsConfig(ctx, "", "remove"); err != nil {
		diagnostics.Log("warn", "host-tools", fmt.Sprintf("remove error=%v", err))
		return
	}
	s.mu.Lock()
	s.hostToolsRemoved = true
	s.mu.Unlock()
}

func (s *Server) closeHostTools() {
	s.mu.Lock()
	tools := s.hostTools
	s.hostTools = nil
	s.mu.Unlock()
	if tools != nil {
		tools.close()
	}
}

// hostToolDirs are the folders the user opened as projects, the current one
// first: the same folders the environment already reads and writes.
func (s *Server) hostToolDirs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var dirs []string
	for path := range s.projectShares {
		if path != s.preferences.Workspace {
			dirs = append(dirs, path)
		}
	}
	sort.Strings(dirs)
	if _, shared := s.projectShares[s.preferences.Workspace]; shared {
		dirs = append([]string{s.preferences.Workspace}, dirs...)
	}
	return dirs
}

// hostPluginNeedsProject is shown for a plugin using ${PROJECT_DIR} while
// the current project is not a folder shared from this computer.
const hostPluginNeedsProject = "请先打开一个项目文件夹以使用此插件"

// hostActiveProjectDir is the current project's folder on this computer, or
// empty when the current project is not a folder shared from here (none
// open, the environment's own directory, or a remote client's folder). It
// never stands in another shared folder for the current one.
func (s *Server) hostActiveProjectDir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clientWorkspace != "" {
		return ""
	}
	if _, shared := s.projectShares[s.preferences.Workspace]; shared {
		return s.preferences.Workspace
	}
	return ""
}

// hostPluginRestartTimeout bounds restarting the plugins that take the
// project folder after the current project changes.
const hostPluginRestartTimeout = 60 * time.Second

// restartProjectPlugins runs again, in the background, only the plugins
// whose command takes ${PROJECT_DIR}, for the project now current. Other
// plugins, their calls in progress and project operations are not held up.
// Restarts for successive switches run one after another, each reading the
// project current when its plugin starts.
func (s *Server) restartProjectPlugins() <-chan struct{} {
	done := make(chan struct{})
	s.mu.Lock()
	tools := s.hostTools
	s.mu.Unlock()
	var affected []*hostbridge.External
	if tools != nil {
		for _, plugin := range tools.plugins {
			if plugin.UsesProjectDir() {
				affected = append(affected, plugin)
			}
		}
	}
	if len(affected) == 0 {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		s.projectPluginRestarts.Lock()
		defer s.projectPluginRestarts.Unlock()
		ctx, cancel := context.WithTimeout(tools.ctx, hostPluginRestartTimeout)
		defer cancel()
		for _, plugin := range affected {
			if tools.ctx.Err() != nil {
				return
			}
			var dir func() string
			if plugin.Manifest.Kind != hostbridge.KindGuest {
				dir = s.hostActiveProjectDir
			}
			err := plugin.Restart(ctx, dir)
			if err == nil {
				err = s.checkRestartedToolNames(tools, plugin)
			}
			if tools.ctx.Err() != nil {
				// Closed meanwhile: leave nothing running.
				plugin.Close()
				return
			}
			s.recordProjectPluginProblem(tools, plugin.Manifest.Name, err)
		}
	}()
	return done
}

// checkRestartedToolNames keeps a plugin that first started now from
// offering a tool or prompt name another plugin already offers.
func (s *Server) checkRestartedToolNames(tools *hostTools, restarted *hostbridge.External) error {
	taken := []string{"run_command", "command_job"}
	takenPrompts := []string{hostCommandsPlugin}
	for _, plugin := range tools.plugins {
		if plugin != restarted {
			taken = append(taken, plugin.Tools()...)
			takenPrompts = append(takenPrompts, plugin.Prompts()...)
		}
	}
	for _, tool := range restarted.Tools() {
		if slices.Contains(taken, tool) {
			restarted.Withdraw()
			return fmt.Errorf("plugin %s: tool name %s is already in use", restarted.Manifest.Name, tool)
		}
	}
	for _, prompt := range restarted.Prompts() {
		if slices.Contains(takenPrompts, prompt) {
			restarted.Withdraw()
			return fmt.Errorf("plugin %s: prompt name %s is already in use", restarted.Manifest.Name, prompt)
		}
	}
	return nil
}

// recordProjectPluginProblem updates what the plugins page says about a
// restarted plugin.
func (s *Server) recordProjectPluginProblem(tools *hostTools, name string, err error) {
	if err != nil {
		diagnostics.Log("warn", "host-tools", fmt.Sprintf("plugin restart error=%v", err))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	problems := maps.Clone(tools.pluginProblems)
	if problems == nil {
		problems = map[string]string{}
	}
	var missing *hostbridge.MissingProgramError
	switch {
	case err == nil:
		delete(problems, name)
	case errors.As(err, new(*hostbridge.MissingProjectDirError)):
		problems[name] = hostPluginNeedsProject
	case errors.As(err, &missing):
		problems[name] = missingProgramMessage(missing.Program)
	default:
		problems[name] = err.Error()
	}
	tools.pluginProblems = problems
	// The page's list of problems, without what it said about this plugin.
	list := slices.DeleteFunc(slices.Clone(tools.problems), func(text string) bool {
		return strings.HasPrefix(text, name+"：") || strings.HasPrefix(text, "plugin "+name+" ") || strings.HasPrefix(text, "plugin "+name+":")
	})
	if message := problems[name]; message != "" {
		list = append(list, name+"："+message)
	}
	tools.problems = list
}

// guestActiveProjectDir is the active project folder as seen inside the
// environment, or empty when no project is open.
func (s *Server) guestActiveProjectDir() string {
	s.mu.Lock()
	clientWorkspace := s.clientWorkspace
	s.mu.Unlock()
	if clientWorkspace != "" {
		return clientWorkspace
	}
	hostDir := s.hostActiveProjectDir()
	if hostDir == "" {
		return ""
	}
	return guestPath(hostDir)
}

// missingProgramMessage explains what runtime is missing on this computer and
// how to install it, across macOS, Windows and Linux.
func missingProgramMessage(program string) string {
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(program), ".exe"))
	switch base {
	case "uv", "uvx":
		return fmt.Sprintf("未找到 %s。请先在本机安装 uv（https://docs.astral.sh/uv/）并确保终端中可运行，然后点击「重新加载插件」。", program)
	case "npx", "node", "npm":
		return fmt.Sprintf("未找到 %s。请先在本机安装 Node.js（https://nodejs.org/）并确保终端中可运行，然后点击「重新加载插件」。", program)
	case "python3", "python":
		return fmt.Sprintf("未找到 %s。请先在本机安装 Python 3（https://www.python.org/）并确保终端中可运行，然后点击「重新加载插件」。", program)
	default:
		return fmt.Sprintf("未找到 %s。请先在本机安装该程序并确保终端中可运行，然后点击「重新加载插件」。", program)
	}
}

// hostToolsToken is this profile's token, created on first use.
func (s *Server) hostToolsToken() (string, error) {
	path := filepath.Join(s.profile, "host-tools.token")
	if data, err := os.ReadFile(path); err == nil {
		if token := strings.TrimSpace(string(data)); len(token) >= 32 {
			return token, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	token, err := hostbridge.NewToken()
	if err != nil {
		return "", err
	}
	return token, os.WriteFile(path, []byte(token+"\n"), 0o600)
}

// hostToolsEntry is the server entry written into Claude's configuration.
func hostToolsEntry(token string) map[string]any {
	return map[string]any{
		"type":    "http",
		"url":     fmt.Sprintf("http://127.0.0.1:%d/", hostToolsPort),
		"headers": map[string]string{"Authorization": "Bearer " + token},
	}
}

// writeHostToolsConfig adds the entry to, or removes it from, each Claude
// Code profile and the desktop app in the environment. The token travels on SSH stdin, never argv.
func (s *Server) writeHostToolsConfig(ctx context.Context, token, action string) error {
	entry, err := json.Marshal(hostToolsEntry(token))
	if err != nil {
		return err
	}
	cmd := s.sshCommand(ctx, "-T", s.sshTarget(), "--", "exec python3 - computer "+action)
	cmd.Stdin = strings.NewReader(hostToolsConfigInput(entry))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && stderr.Len() > 0 {
			return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return err
	}
	return nil
}

// hostToolsConfigInput is the script preceded by the values it reads back
// from its own source, so the entry is never part of a command line.
func hostToolsConfigInput(entry []byte) string {
	// A JSON string of ASCII is also a Python string literal.
	literal, _ := json.Marshal(string(entry))
	relay, _ := json.Marshal(hostToolsRelayScript)
	return "ENTRY = " + string(literal) + "\nRELAY = " + string(relay) + "\n" + hostToolsConfigScript
}
