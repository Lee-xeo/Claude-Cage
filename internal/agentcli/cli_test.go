package agentcli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sylvinus/agent-vm/internal/cage"
)

func TestAgentDoctorReportsLimaSocketPathWithServiceStopped(t *testing.T) {
	profile := filepath.Join(t.TempDir(), strings.Repeat("private-path-", 9))
	path := filepath.Join(profile, "config.json")
	var output bytes.Buffer
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(cage.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	checks := func() []map[string]string {
		t.Helper()
		output.Reset()
		code := Main([]string{"doctor", "--config", path}, strings.NewReader(""), &output)
		var reply struct {
			Profile string `json:"config_path"`
			Data    struct {
				Checks []map[string]string `json:"checks"`
			} `json:"data"`
		}
		if err := json.Unmarshal(output.Bytes(), &reply); code != 0 || err != nil || reply.Profile != path {
			t.Fatalf("invalid doctor reply (exit %d): %v", code, err)
		}
		return reply.Data.Checks
	}
	find := func(checks []map[string]string, code string) map[string]string {
		for _, check := range checks {
			if check["code"] == code {
				return check
			}
		}
		return nil
	}
	code := "lima_socket_path_too_long"
	failure := find(checks(), code)
	if failure["status"] != "blocked" || failure["next_action"] != "use_shorter_local_config_or_storage_path" ||
		!strings.Contains(failure["message"], cage.LimaSocketPathReason) ||
		!strings.Contains(failure["message"], "更短的本地目录") || strings.Contains(failure["message"], profile) {
		t.Fatalf("missing actionable, path-free doctor result: %+v", failure)
	}
	if _, err := os.Stat(filepath.Join(profile, "state")); !os.IsNotExist(err) {
		t.Fatalf("doctor created VM state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(profile, "container.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := find(checks(), code); got != nil {
		t.Fatalf("container reported a Lima path check: %+v", got)
	}
}

func TestAgentCLIProtocolAndInit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile", "config.json")
	run := func(args ...string) (int, agentReply) {
		t.Helper()
		var output bytes.Buffer
		code := Main(append(args, "--config", path), strings.NewReader(""), &output)
		var reply agentReply
		if json.Unmarshal(output.Bytes(), &reply) != nil || reply.Protocol != 1 {
			t.Fatalf("not one protocol reply: %s", output.String())
		}
		return code, reply
	}
	if code, reply := run("capabilities"); code != 0 {
		t.Fatal(code)
	} else {
		raw, _ := json.Marshal(reply.Data)
		for _, token := range []string{"initiator", "canceled_by", "desktop_active"} {
			if !strings.Contains(string(raw), token) {
				t.Fatalf("capabilities missing %q: %s", token, raw)
			}
		}
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatal("discovery created profile")
	}
	if code, _ := run("init"); code != 0 {
		t.Fatal(code)
	}
	before, _ := os.ReadFile(path)
	if code, _ := run("init"); code != 0 {
		t.Fatal("not idempotent")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("repeat init overwrote config")
	}
	for _, args := range [][]string{{"call", "status", "--input", "-"}, {"wat"}, {"doctor", "--wait", "1s"}, {"capabilities", "--wait", "-1s"}, {"call"}} {
		if code, _ := run(args...); code != 2 {
			t.Fatalf("usage accepted %v: %d", args, code)
		}
	}
	if err := os.WriteFile(path, []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if code, reply := run("init"); code != 1 || reply.Error.Code != "config_invalid" {
		t.Fatal("damaged config silently replaced")
	}
}

func TestAgentCLIRejectsSecretArgumentsAndMalformedJSON(t *testing.T) {
	for _, input := range []string{`null`, `[]`, `{} {}`, `{"password":"secret"} trailing`} {
		var output bytes.Buffer
		code := Main([]string{"call", "account.unlock", "--input", "-", "--config", filepath.Join(t.TempDir(), "config.json")}, strings.NewReader(input), &output)
		if code != 2 || strings.Contains(output.String(), "secret") {
			t.Fatal(code, output.String())
		}
	}
	var output bytes.Buffer
	code := Main([]string{"call", "account.unlock", "--password", "do-not-echo"}, strings.NewReader(""), &output)
	if code != 2 || strings.Contains(output.String(), "do-not-echo") {
		t.Fatal(output.String())
	}
}

func TestAgentCLIAcceptsUTF8BOMJSONInput(t *testing.T) {
	bom := []byte{0xEF, 0xBB, 0xBF}
	dir := t.TempDir()
	inputFile := filepath.Join(dir, "input.json")
	if err := os.WriteFile(inputFile, append(append([]byte(nil), bom...), []byte(`{"unexpected":true}`)...), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "profile", "config.json")
	// Read operations reject non-empty JSON objects with a distinct message
	// ("Read operations accept no input fields."), proving the BOM-prefixed
	// JSON object parsed cleanly instead of failing with invalid_input.
	for _, tc := range []struct {
		name  string
		args  []string
		stdin string
	}{
		{"file", []string{"call", "status", "--input", inputFile, "--config", configPath}, ""},
		{"stdin", []string{"call", "status", "--input", "-", "--config", configPath}, string(bom) + `{"unexpected":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			code := Main(tc.args, strings.NewReader(tc.stdin), &output)
			if code != 2 || !strings.Contains(output.String(), "Read operations accept no input fields.") {
				t.Fatalf("BOM JSON was not parsed: code=%d output=%s", code, output.String())
			}
		})
	}
	// A BOM alone or BOM + non-object still fails validation.
	for _, badInput := range []string{string(bom), string(bom) + `[]`, string(bom) + `null`} {
		var output bytes.Buffer
		code := Main([]string{"call", "account.unlock", "--input", "-", "--config", configPath}, strings.NewReader(badInput), &output)
		if code != 2 || !strings.Contains(output.String(), "Input must be one JSON object") {
			t.Fatalf("invalid BOM input accepted: code=%d output=%s", code, output.String())
		}
	}
}

func TestAgentWaitTimeoutKeepsOriginalJobID(t *testing.T) {
	data, err := agentWait(context.Background(), filepath.Join(t.TempDir(), "config.json"), "012345abcdef", time.Millisecond)
	if err == nil || data != nil || !strings.Contains(err.Error(), "job may still be running") {
		t.Fatal(string(data), err)
	}
}
