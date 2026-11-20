package managedproxy

import (
	"strings"
	"testing"
	"time"
)

func TestLaunchdStartKickstartsTheRegisteredJob(t *testing.T) {
	bootstrap, kickstart := launchdStartArgs(501, "local.example.job", "/private/service.plist")
	if strings.Join(bootstrap, " ") != "bootstrap gui/501 /private/service.plist" {
		t.Fatalf("bootstrap: %q", bootstrap)
	}
	// No -k: a job that RunAtLoad already started must not be killed.
	if strings.Join(kickstart, " ") != "kickstart gui/501/local.example.job" {
		t.Fatalf("kickstart: %q", kickstart)
	}
	if coreReadyTimeout < 15*time.Second {
		t.Fatalf("core readiness window shorter than other platforms: %s", coreReadyTimeout)
	}
}
