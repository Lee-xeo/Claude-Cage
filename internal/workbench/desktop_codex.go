package workbench

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/sylvinus/agent-vm/internal/containerenv"
)

//go:embed desktop_codex.py
var desktopCodexHelper string

// The host owns each project's grant. A tool invocation cannot supply a path,
// choose another account, or select a Codex thread. The adapter is installed
// in the guest user's private directory, outside every shared repository.
func (s *Server) desktopCodexRPC(ctx context.Context, mode string, input, output any) error {
	ctx, cancel := context.WithTimeout(ctx, 18*time.Second)
	defer cancel()
	const bootstrap = `import hashlib,os,pathlib,sys,tempfile
p=pathlib.Path.home()/".local/share/project-collaboration/connector.py"
if sys.argv[1]=="install":
 data=sys.stdin.buffer.read(65537)
 if hashlib.sha256(data).hexdigest()!=sys.argv[2]: sys.exit(1)
 p.parent.mkdir(mode=0o700,parents=True,exist_ok=True)
 with tempfile.NamedTemporaryFile(dir=p.parent,delete=False) as f:
  f.write(data)
 os.replace(f.name,p)
 sys.exit(0)
if not p.is_file() or hashlib.sha256(p.read_bytes()).hexdigest()!=sys.argv[2]: sys.exit(1)
os.execv(sys.executable,[sys.executable,"-I",str(p),sys.argv[1]])`
	cmd := s.sshCommand(ctx, "-T", s.sshTarget(), "--", shellescape.QuoteCommand([]string{"python3", "-I", "-c", bootstrap, mode, fmt.Sprintf("%x", sha256.Sum256([]byte(desktopCodexHelper)))}))
	if mode == "install" {
		cmd.Stdin = strings.NewReader(desktopCodexHelper)
	} else {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		cmd.Stdin = bytes.NewReader(data)
	}
	var buf bytes.Buffer
	cmd.Stdout = &boundedStatusWriter{buffer: &buf, remaining: 1 << 20}
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		return errors.New("Desktop Code connector unavailable")
	}
	if output != nil {
		return json.Unmarshal(buf.Bytes(), output)
	}
	return nil
}

type desktopCodexGrant struct {
	Workspace   string `json:"workspace"`
	Account     string `json:"account"`
	ComputerUse bool   `json:"computer_use,omitempty"`
}

type desktopCodexEndpoint struct{ Grant, Session string }

func (s *Server) monitorDesktopCodex(ctx context.Context) {
	// Local Linux guests use the same private SSH transport on supported hosts.
	if containerenv.Configured(s.profile) {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	type workerKey struct {
		desktopCodexEndpoint
		scope desktopCodexGrant
	}
	workers := map[workerKey]context.CancelFunc{}
	var group sync.WaitGroup
	stop := func() {
		for key, cancel := range workers {
			cancel()
			delete(workers, key)
		}
	}
	defer func() { stop(); group.Wait() }()
	var installed bool
	var lastEpoch context.Context
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		epoch, unlocked := s.account.access()
		if epoch != lastEpoch {
			stop()
			lastEpoch = epoch
		}
		s.mu.Lock()
		ready := s.ready && !s.closing
		s.mu.Unlock()
		bin := s.codexWindow()
		if !ready || !unlocked || bin == "" {
			stop()
			installed = false
			continue
		}
		var grants map[string]desktopCodexGrant
		data, err := os.ReadFile(filepath.Join(s.profile, "codex-desktop-projects.json"))
		if err != nil || len(data) > 64<<10 || json.Unmarshal(data, &grants) != nil {
			stop()
			continue
		}
		bindings := map[string]map[string]string{}
		for grant, scope := range grants {
			path := scope.Workspace
			allowed, err := s.projectWorkspace(path)
			if err != nil || path == "" || allowed != path || isClientMountPath(path) || !validCLIUUID(grant) || !validClaudeGuestID(scope.Account) {
				delete(grants, grant)
				continue
			}
			if bindings[scope.Account] == nil {
				bindings[scope.Account] = map[string]string{}
			}
			bindings[scope.Account][guestPath(path)] = grant
		}
		if len(bindings) == 0 {
			stop()
			continue
		}
		poll, cancel := context.WithCancel(ctx)
		after := context.AfterFunc(epoch, cancel)
		if !installed {
			installed = s.desktopCodexRPC(poll, "install", nil, nil) == nil
		}
		var endpoints []desktopCodexEndpoint
		err = s.desktopCodexRPC(poll, "sync", map[string]any{"bindings": bindings}, &endpoints)
		after()
		cancel()
		if err != nil || len(endpoints) > 128 || epoch.Err() != nil {
			stop()
			installed = false
			continue
		}
		active := map[workerKey]bool{}
		for _, endpoint := range endpoints {
			scope := grants[endpoint.Grant]
			key := workerKey{endpoint, scope}
			project := scope.Workspace
			if project == "" || !validCLIUUID(endpoint.Session) {
				continue
			}
			active[key] = true
			if workers[key] != nil {
				continue
			}
			life, end := context.WithCancel(ctx)
			revoke := context.AfterFunc(epoch, end)
			workers[key] = end
			group.Add(1)
			go func() {
				defer group.Done()
				defer revoke()
				defer end()
				s.runCodexRequests(life, bin, project, scope.ComputerUse && runtime.GOOS == "darwin", func() bool { return life.Err() == nil }, func(ctx context.Context, req map[string]string, out *codexDelivery) error {
					req["session"], req["endpoint"] = endpoint.Session, endpoint.Grant
					return s.desktopCodexRPC(ctx, "rpc", req, out)
				})
			}()
		}
		for key, cancel := range workers {
			if !active[key] {
				cancel()
				delete(workers, key)
			}
		}
	}
}
