package hostbridge

import (
	"os"
	"runtime"
	"strings"
)

// The application's own settings, and the variables the system adds when it
// launches the application, describe the application rather than the user's
// session. Programs run on the user's behalf receive the rest.
var appEnvPrefixes = []string{"CAGE_", "CLAUDE_CAGE_", "AGENT_VM_", "LIMA_"}

var appEnvNames = []string{"__CFBundleIdentifier", "XPC_SERVICE_NAME"}

func appEnv(key string) bool {
	if runtime.GOOS == "windows" {
		key = strings.ToUpper(key)
	}
	for _, prefix := range appEnvPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	for _, name := range appEnvNames {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

// WithoutAppEnv returns env without the application's own variables.
func WithoutAppEnv(env []string) []string {
	kept := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		// Windows keeps per-drive folders in variables named "=C:".
		if key == "" || !appEnv(key) {
			kept = append(kept, entry)
		}
	}
	return kept
}

// UserEnv is the environment for programs run on the user's behalf: this
// process's own without the application's variables, with the PATH of the
// user's terminal where the system gives the application a shorter one.
func UserEnv() []string { return userEnv() }

func baseEnv() []string { return WithoutAppEnv(os.Environ()) }
