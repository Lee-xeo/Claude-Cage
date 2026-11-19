package hostbridge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// maxRunningJobs bounds what a caller can leave running at once.
	maxRunningJobs = 8
	// keptJobs is how many finished jobs stay readable.
	keptJobs = 32
)

// Jobs are the commands running in the background, shared by every session
// of a Bridge. Close ends those still running.
type Jobs struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	next  int
	jobs  map[string]*job
	order []string
}

type job struct {
	output *cappedBuffer
	cancel context.CancelFunc
	done   chan struct{}
	// result and err are set before done closes.
	result commandOutput
	err    error
}

// NewJobs returns an empty set of background commands.
func NewJobs() *Jobs {
	ctx, cancel := context.WithCancel(context.Background())
	return &Jobs{ctx: ctx, cancel: cancel, jobs: map[string]*job{}}
}

// Close ends every running command and waits for them.
func (j *Jobs) Close() {
	j.cancel()
	j.mu.Lock()
	jobs := make([]*job, 0, len(j.jobs))
	for _, each := range j.jobs {
		jobs = append(jobs, each)
	}
	j.mu.Unlock()
	for _, each := range jobs {
		<-each.done
	}
}

func (j *Jobs) start(command, dir string, timeout time.Duration) (commandOutput, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.ctx.Err() != nil {
		return commandOutput{}, errors.New("background commands have stopped")
	}
	running := 0
	for _, each := range j.jobs {
		select {
		case <-each.done:
		default:
			running++
		}
	}
	if running >= maxRunningJobs {
		return commandOutput{}, fmt.Errorf("%d background commands are already running; wait for one or stop it", running)
	}
	j.next++
	id := fmt.Sprintf("job-%d", j.next)
	ctx, cancel := context.WithCancel(j.ctx)
	started := &job{output: &cappedBuffer{limit: commandOutputLimit}, cancel: cancel, done: make(chan struct{})}
	j.jobs[id] = started
	j.order = append(j.order, id)
	// Forget the oldest finished jobs beyond what is kept.
	for len(j.order) > keptJobs {
		dropped := false
		for i, old := range j.order {
			select {
			case <-j.jobs[old].done:
				delete(j.jobs, old)
				j.order = append(j.order[:i], j.order[i+1:]...)
				dropped = true
			default:
				continue
			}
			break
		}
		if !dropped {
			break
		}
	}
	go func() {
		defer close(started.done)
		defer cancel()
		started.result, started.err = execute(ctx, command, dir, timeout, started.output)
	}()
	return commandOutput{Job: id, Running: true}, nil
}

type jobInput struct {
	Job  string `json:"job" jsonschema:"the job id returned by run_command"`
	Stop bool   `json:"stop,omitempty" jsonschema:"end the command if it is still running"`
	Wait int    `json:"wait_seconds,omitempty" jsonschema:"wait up to this many seconds for the command to finish before answering; at most 50"`
}

func (j *Jobs) register(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "command_job",
		Description: "Read the output so far and the state of a background command started with run_command, optionally waiting briefly for it to finish or stopping it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in jobInput) (*mcp.CallToolResult, commandOutput, error) {
		out, err := j.read(ctx, in)
		return nil, out, err
	})
}

func (j *Jobs) read(ctx context.Context, in jobInput) (commandOutput, error) {
	j.mu.Lock()
	found := j.jobs[in.Job]
	j.mu.Unlock()
	if found == nil {
		return commandOutput{}, fmt.Errorf("no background command %q; finished ones are kept only for a while", in.Job)
	}
	if in.Stop {
		found.cancel()
		<-found.done
	} else if in.Wait > 0 {
		timer := time.NewTimer(time.Duration(min(in.Wait, 50)) * time.Second)
		defer timer.Stop()
		select {
		case <-found.done:
		case <-timer.C:
		case <-ctx.Done():
		}
	}
	select {
	case <-found.done:
		result := found.result
		result.Job = in.Job
		return result, found.err
	default:
		return commandOutput{Job: in.Job, Running: true, Output: found.output.String(), Truncated: found.output.truncated()}, nil
	}
}
