package hostbridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultCommandTimeout = 2 * time.Minute
	maxCommandTimeout     = 30 * time.Minute
	commandOutputLimit    = 64 << 10

	defaultBackgroundTimeout = 30 * time.Minute
	maxBackgroundTimeout     = 6 * time.Hour
)

// Command runs shell commands on this computer.
//
// Dirs reports the directories a command may start in; the first is the
// default. This limits where a command starts, not what it can reach: a
// command runs with the user's own permissions.
//
// MapDir, if set, turns the directory a caller names into this computer's
// spelling of it, for callers that see the same directory under another path.
//
// Approve, if set, is asked before each command and refuses it by returning
// an error, whose text the caller sees.
//
// Jobs, if set, lets a command run in the background: the call returns at
// once and the caller reads the outcome later. Without it every call waits.
type Command struct {
	Dirs    func() []string
	MapDir  func(string) string
	Approve func(context.Context, CommandRequest) error
	Jobs    *Jobs
}

// CommandRequest is one command awaiting approval. Dir is the directory it
// would run in and Project the allowed directory that contains it, both with
// symlinks resolved.
type CommandRequest struct {
	Command string
	Dir     string
	Project string
}

type commandInput struct {
	Command        string `json:"command" jsonschema:"the shell command to run"`
	Dir            string `json:"dir,omitempty" jsonschema:"absolute directory to run in; defaults to the first project directory"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"stop the command after this many seconds; default 120, at most 1800; a background command defaults to 1800, at most 21600"`
	Background     bool   `json:"background,omitempty" jsonschema:"return at once with a job id instead of waiting; use for builds, test suites and anything that may take more than a minute, then read the result with command_job"`
}

type commandOutput struct {
	Job       string `json:"job,omitempty"`
	Running   bool   `json:"running,omitempty"`
	ExitCode  int    `json:"exit_code"`
	Output    string `json:"output"`
	Truncated bool   `json:"truncated,omitempty"`
	TimedOut  bool   `json:"timed_out,omitempty"`
}

func (c Command) tool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "run_command",
		Description: "Run a shell command on the user's computer and return its combined output and exit code.",
	}
}

// Register adds the run_command tool.
func (c Command) Register(server *mcp.Server) {
	mcp.AddTool(server, c.tool(), func(ctx context.Context, _ *mcp.CallToolRequest, in commandInput) (*mcp.CallToolResult, commandOutput, error) {
		out, err := c.run(ctx, in)
		return nil, out, err
	})
	if c.Jobs != nil {
		c.Jobs.register(server)
	}
}

func (c Command) run(ctx context.Context, in commandInput) (commandOutput, error) {
	if strings.TrimSpace(in.Command) == "" {
		return commandOutput{}, errors.New("command is empty")
	}
	dir, project, err := c.resolveDir(in.Dir)
	if err != nil {
		return commandOutput{}, err
	}
	if c.Approve != nil {
		if err := c.Approve(ctx, CommandRequest{Command: in.Command, Dir: dir, Project: project}); err != nil {
			return commandOutput{}, err
		}
	}
	if in.Background {
		if c.Jobs == nil {
			return commandOutput{}, errors.New("background commands are not available")
		}
		timeout := defaultBackgroundTimeout
		if in.TimeoutSeconds > 0 {
			timeout = min(time.Duration(in.TimeoutSeconds)*time.Second, maxBackgroundTimeout)
		}
		return c.Jobs.start(in.Command, dir, timeout)
	}
	timeout := defaultCommandTimeout
	if in.TimeoutSeconds > 0 {
		timeout = min(time.Duration(in.TimeoutSeconds)*time.Second, maxCommandTimeout)
	}
	output := &cappedBuffer{limit: commandOutputLimit}
	return execute(ctx, in.Command, dir, timeout, output)
}

// execute runs the command until it exits, the timeout passes or ctx ends,
// collecting its output in output, which may be read meanwhile.
func execute(ctx context.Context, command, dir string, timeout time.Duration, output *cappedBuffer) (commandOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := shellCommand(command)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		return commandOutput{}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		// The command may have started children that hold the output open,
		// so end the whole tree rather than only the shell.
		killTree(cmd)
		waitErr = <-done
	}

	result := commandOutput{Output: output.String(), Truncated: output.truncated()}
	if ctx.Err() != nil {
		result.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		result.ExitCode = -1
		return result, nil
	}
	var exit *exec.ExitError
	switch {
	case waitErr == nil:
	case errors.As(waitErr, &exit):
		result.ExitCode = exit.ExitCode()
	default:
		return result, waitErr
	}
	return result, nil
}

// resolveDir returns the directory to run in and the allowed directory that
// contains it, refusing one outside Dirs.
func (c Command) resolveDir(requested string) (dir, project string, err error) {
	dirs := c.Dirs()
	if len(dirs) == 0 {
		return "", "", errors.New("no project directory is available")
	}
	if requested == "" {
		requested = dirs[0]
	} else if c.MapDir != nil {
		requested = c.MapDir(requested)
	}
	if !filepath.IsAbs(requested) {
		return "", "", fmt.Errorf("dir must be an absolute path: %s", requested)
	}
	// Symlinks are resolved on both sides so a link inside a project
	// directory cannot stand in for somewhere outside it.
	real, err := filepath.EvalSymlinks(requested)
	if err != nil {
		return "", "", err
	}
	for _, allowed := range dirs {
		root, err := filepath.EvalSymlinks(allowed)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(root, real); err == nil && filepath.IsLocal(rel) {
			return real, root, nil
		}
	}
	return "", "", fmt.Errorf("dir is outside the project directories: %s", requested)
}

// cappedBuffer keeps the start and the end of the output, since the end
// usually holds the error and the start says what ran.
type cappedBuffer struct {
	limit   int
	mu      sync.Mutex
	head    bytes.Buffer
	tail    []byte
	dropped bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(p)
	headLimit, tailLimit := b.limit/4, b.limit-b.limit/4
	if room := headLimit - b.head.Len(); room > 0 {
		n := min(room, len(p))
		b.head.Write(p[:n])
		p = p[n:]
	}
	b.tail = append(b.tail, p...)
	if over := len(b.tail) - tailLimit; over > 0 {
		b.tail = append(b.tail[:0], b.tail[over:]...)
		b.dropped = true
	}
	return written, nil
}

func (b *cappedBuffer) truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.dropped
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.dropped {
		return b.head.String() + string(b.tail)
	}
	return b.head.String() + "\n[... output truncated ...]\n" + string(b.tail)
}
