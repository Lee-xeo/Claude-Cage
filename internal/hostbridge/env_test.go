package hostbridge

import (
	"slices"
	"testing"
)

func TestProgramsDoNotReceiveTheApplicationsVariables(t *testing.T) {
	env := []string{
		"HOME=/home/user", "PATH=/usr/bin", "LANG=en_US.UTF-8", "=C:=C:\\work",
		"CAGE_DESKTOP_START_ID=1", "CLAUDE_CAGE_CONFIG=/x/config.json", "AGENT_VM_STATE_DIR=/x",
		"LIMA_HOME=/x/lima", "__CFBundleIdentifier=app.example", "XPC_SERVICE_NAME=application.app.example.1",
	}
	kept := WithoutAppEnv(env)
	if want := []string{"HOME=/home/user", "PATH=/usr/bin", "LANG=en_US.UTF-8", "=C:=C:\\work"}; !slices.Equal(kept, want) {
		t.Fatalf("kept %q, want %q", kept, want)
	}
}
