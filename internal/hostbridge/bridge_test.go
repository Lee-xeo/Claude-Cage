package hostbridge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func startBridge(t *testing.T, dirs ...string) *Bridge {
	t.Helper()
	token, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := Start(token, Command{Dirs: func() []string { return dirs }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	return bridge
}

func call(t *testing.T, bridge *Bridge, in commandInput) (commandOutput, *mcp.CallToolResult) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := bridge.connect(ctx, bridge.Token())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "run_command", Arguments: in})
	if err != nil {
		t.Fatal(err)
	}
	var out commandOutput
	if !result.IsError {
		raw, _ := json.Marshal(result.StructuredContent)
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
	}
	return out, result
}

func TestCommandRunsInTheProjectDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	list := "ls"
	if runtime.GOOS == "windows" {
		list = "Get-ChildItem -Name"
	}
	out, _ := call(t, startBridge(t, dir), commandInput{Command: list})
	if out.ExitCode != 0 || !strings.Contains(out.Output, "marker.txt") {
		t.Fatalf("got %+v", out)
	}
}

func TestCommandReportsExitCode(t *testing.T) {
	out, _ := call(t, startBridge(t, t.TempDir()), commandInput{Command: "exit 3"})
	if out.ExitCode != 3 {
		t.Fatalf("got %+v", out)
	}
}

func TestCommandRefusesDirectoryOutsideProjects(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	bridge := startBridge(t, dir)
	if _, result := call(t, bridge, commandInput{Command: "echo hi", Dir: outside}); !result.IsError {
		t.Fatal("a directory outside the projects was accepted")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip("symlinks are unavailable:", err)
	}
	if _, result := call(t, bridge, commandInput{Command: "echo hi", Dir: link}); !result.IsError {
		t.Fatal("a link leading outside the projects was accepted")
	}
}

func TestCommandStopsAtTimeoutWithItsChildren(t *testing.T) {
	sleep := "sleep 60 & sleep 60"
	if runtime.GOOS == "windows" {
		sleep = "Start-Sleep 60"
	}
	started := time.Now()
	out, _ := call(t, startBridge(t, t.TempDir()), commandInput{Command: sleep, TimeoutSeconds: 1})
	if !out.TimedOut || time.Since(started) > 20*time.Second {
		t.Fatalf("got %+v after %s", out, time.Since(started))
	}
}

func TestRefusedCommandDoesNotRun(t *testing.T) {
	dir := t.TempDir()
	token, _ := NewToken()
	var asked CommandRequest
	refuse := true
	bridge, err := Start(token, Command{
		Dirs: func() []string { return []string{dir} },
		Approve: func(_ context.Context, request CommandRequest) error {
			asked = request
			if refuse {
				return errors.New("the user refused this command")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close() })
	create := "echo x > made.txt"
	if runtime.GOOS == "windows" {
		create = "Set-Content made.txt x"
	}
	_, result := call(t, bridge, commandInput{Command: create})
	if !result.IsError {
		t.Fatal("a refused command returned a result")
	}
	if text, _ := json.Marshal(result.Content); !strings.Contains(string(text), "the user refused this command") {
		t.Fatalf("the refusal is not reported: %s", text)
	}
	if _, err := os.Stat(filepath.Join(dir, "made.txt")); err == nil {
		t.Fatal("a refused command ran")
	}
	real, _ := filepath.EvalSymlinks(dir)
	if asked.Command != create || asked.Dir != real || asked.Project != real {
		t.Fatalf("asked about %+v", asked)
	}
	refuse = false
	if out, result := call(t, bridge, commandInput{Command: create}); result.IsError || out.ExitCode != 0 {
		t.Fatalf("an approved command failed: %+v", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "made.txt")); err != nil {
		t.Fatal("an approved command did not run")
	}
}

func TestLongOutputKeepsItsStartAndEnd(t *testing.T) {
	buffer := &cappedBuffer{limit: 40}
	_, _ = buffer.Write([]byte("start-----"))
	_, _ = buffer.Write([]byte(strings.Repeat("m", 200)))
	_, _ = buffer.Write([]byte("end"))
	got := buffer.String()
	if !buffer.truncated() || !strings.HasPrefix(got, "start-----") || !strings.HasSuffix(got, "end") {
		t.Fatalf("got %q", got)
	}
}

func TestRequestWithoutTheTokenIsRefused(t *testing.T) {
	bridge := startBridge(t, t.TempDir())
	for _, token := range []string{"", "wrong"} {
		request, _ := http.NewRequest(http.MethodPost, "http://"+bridge.Addr(), strings.NewReader("{}"))
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token %q: status %d", token, response.StatusCode)
		}
	}
}

func TestToolsDescribeOnlyWhatTheyDo(t *testing.T) {
	bridge := startBridge(t, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := bridge.connect(ctx, bridge.Token())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) == 0 {
		t.Fatal("no tools listed")
	}
	for _, tool := range tools.Tools {
		if word, found := CheckWording(tool); found {
			t.Errorf("tool %s mentions %q", tool.Name, word)
		}
	}
	for _, text := range []string{"cage_run", "Runs inside the VM", "在虚拟机里运行", "guest files"} {
		if _, found := CheckWording(&mcp.Tool{Name: "x", Description: text}); !found {
			t.Errorf("%q passed the wording check", text)
		}
	}
	if word, found := CheckWording(&mcp.Tool{Name: "package_info", Description: "Show vmstat-free package details."}); found {
		t.Errorf("an ordinary description was refused for %q", word)
	}
	// A parameter's description is shown to the model as well.
	parameter := map[string]any{"type": "object", "properties": map[string]any{
		"path": map[string]any{"type": "string", "description": "A file in the guest"}}}
	if _, found := CheckWording(&mcp.Tool{Name: "read", Description: "Reads a file.", InputSchema: parameter}); !found {
		t.Error("a parameter description passed the wording check")
	}
}

func TestCommandPromptIsOfferedAndNeutral(t *testing.T) {
	bridge := startBridge(t, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := bridge.connect(ctx, bridge.Token())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	listed, err := session.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Prompts) != 1 || listed.Prompts[0].Name != "commands" {
		t.Fatalf("prompts = %+v, want [commands]", listed.Prompts)
	}
	if word, found := CheckPromptWording(listed.Prompts[0]); found {
		t.Fatalf("commands prompt mentions reserved word %q", word)
	}

	got, err := session.GetPrompt(ctx, &mcp.GetPromptParams{
		Name:      "commands",
		Arguments: map[string]string{"request": "list files"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if word, found := CheckPromptResultWording(got); found {
		t.Fatalf("commands prompt result mentions reserved word %q", word)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("messages = %+v", got.Messages)
	}
	text, ok := got.Messages[0].Content.(*mcp.TextContent)
	if !ok || !strings.Contains(text.Text, "run_command") || !strings.Contains(text.Text, "list files") {
		t.Fatalf("unexpected prompt body: %#v", got.Messages[0].Content)
	}
}

func TestBackgroundCommandIsReadLater(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the commands are for a POSIX shell")
	}
	dir := t.TempDir()
	jobs := NewJobs()
	defer jobs.Close()
	command := Command{Dirs: func() []string { return []string{dir} }, Jobs: jobs}
	ctx := context.Background()
	started, err := command.run(ctx, commandInput{Command: "echo first; sleep 1; echo second; exit 4", Background: true})
	if err != nil || started.Job == "" || !started.Running {
		t.Fatalf("start: %+v %v", started, err)
	}
	finished, err := jobs.read(ctx, jobInput{Job: started.Job, Wait: 20})
	if err != nil || finished.Running || finished.ExitCode != 4 || !strings.Contains(finished.Output, "first") || !strings.Contains(finished.Output, "second") {
		t.Fatalf("finished: %+v %v", finished, err)
	}
	if _, err := jobs.read(ctx, jobInput{Job: "job-999"}); err == nil {
		t.Fatal("an unknown job was read")
	}
}

func TestBackgroundCommandShowsOutputWhileRunningAndCanBeStopped(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the commands are for a POSIX shell")
	}
	dir := t.TempDir()
	jobs := NewJobs()
	defer jobs.Close()
	command := Command{Dirs: func() []string { return []string{dir} }, Jobs: jobs}
	ctx := context.Background()
	started, err := command.run(ctx, commandInput{Command: "echo begun; sleep 60 & sleep 60", Background: true})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		now, err := jobs.read(ctx, jobInput{Job: started.Job})
		if err != nil || !now.Running {
			t.Fatalf("while running: %+v %v", now, err)
		}
		if strings.Contains(now.Output, "begun") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no output was visible while the command ran")
		}
		time.Sleep(50 * time.Millisecond)
	}
	begun := time.Now()
	stopped, err := jobs.read(ctx, jobInput{Job: started.Job, Stop: true})
	if err != nil || stopped.Running || stopped.ExitCode != -1 || time.Since(begun) > 10*time.Second {
		t.Fatalf("stopped: %+v %v after %s", stopped, err, time.Since(begun))
	}
}

func TestBackgroundCommandsAreBoundedAndEndWithTheBridge(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the commands are for a POSIX shell")
	}
	dir := t.TempDir()
	jobs := NewJobs()
	command := Command{Dirs: func() []string { return []string{dir} }, Jobs: jobs}
	for range maxRunningJobs {
		if _, err := command.run(context.Background(), commandInput{Command: "sleep 60", Background: true}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := command.run(context.Background(), commandInput{Command: "sleep 60", Background: true}); err == nil {
		t.Fatal("more background commands than the limit were started")
	}
	begun := time.Now()
	jobs.Close()
	if time.Since(begun) > 15*time.Second {
		t.Fatalf("closing took %s", time.Since(begun))
	}
	if _, err := command.run(context.Background(), commandInput{Command: "true", Background: true}); err == nil {
		t.Fatal("a background command started after closing")
	}
	if _, err := (Command{Dirs: func() []string { return []string{dir} }}).run(context.Background(), commandInput{Command: "true", Background: true}); err == nil {
		t.Fatal("a background command started without Jobs")
	}
}
