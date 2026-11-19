package cage

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/guestprofile"
)

// Names that programs inside the environment can observe: installed paths,
// unit names, runtime and configuration directories, journal namespaces,
// groups, audio devices and the variables their processes inherit. Account
// names of environments created before guest profiles stay as they are.
var identityMarkerPatterns = []*regexp.Regexp{
	regexp.MustCompile(`/usr/(?:local/)?(?:s?bin|lib|libexec|share)/[^\s'"\\:;)]*(?:cage|Cage)[^\s'"\\:;)]*`),
	regexp.MustCompile(`/etc/[^\s'"\\:;)]*(?:cage|Cage)[^\s'"\\:;)]*`),
	regexp.MustCompile(`/run/[^\s'"\\:;)]*(?:cage|Cage)[^\s'"\\:;)]*`),
	regexp.MustCompile(`\.(?:config|cache|local/share)/[^\s'"\\:;)]*(?:cage|Cage)[^\s'"\\:;)]*`),
	regexp.MustCompile(`\b[A-Za-z0-9_.@-]*cage[A-Za-z0-9_.@-]*\.(?:service|socket|timer|slice|desktop)\b`),
	regexp.MustCompile(`systemctl\b[^\n|;&]*\bcage-[a-z-]+`),
	regexp.MustCompile(`--unit=cage[a-z-]*`),
	regexp.MustCompile(`(?m)^(?:Wants|Requires|After|Before|PartOf)=[^\n]*\bcage-[a-z-]+`),
	regexp.MustCompile(`(?m)^Description=[^\n]*(?i:cage)[^\n]*`),
	regexp.MustCompile(`(?:LogNamespace|RuntimeDirectory)=cage[a-z-]*`),
	regexp.MustCompile(`journald@cage|namespace=cage`),
	regexp.MustCompile(`\bcage-audio\b`),
	regexp.MustCompile(`\bcage_output\b|device\.description=Cage`),
	regexp.MustCompile(`\bCAGE_(?:LANGUAGE|WORKBENCH|TERMINAL_COMMAND|CLAUDE_ACCOUNT_ID|CLAUDE_SUBJECT|CLAUDE_LOGIN|LOGIN_CONNECTION|LOGIN_OWNER|ACCOUNT_BROWSER|DESKTOP_SESSION|NOTIFY_SOCKET|DISPLAY_ACTIVITY|DISPLAY_OUTPUT|PHONE_[A-Z_]+)\b`),
}

// Explicit exceptions. Each entry is an exact match together with the reason
// it may remain in what the environment receives.
var identityMarkerAllowed = map[string]string{
	// Chromium derives the unpacked extension ID from its path. Existing
	// environments keep it so the extension keeps its state; environments
	// created with a guest profile receive a neutral path instead.
	"/usr/local/lib/cage-split": "legacy extension path",
}

// Persistent user data under ~/.local/share moves in a separate, verified
// migration step; until then its readers keep the existing location.
var identityMarkerDeferredPrefixes = []string{".local/share/cage"}

var identityMarkerPendingSeen = map[string]bool{}

func identityMarkers(script string) []string {
	script = strings.ReplaceAll(script, guestMigrationScript, "")
	found := map[string]bool{}
	for _, pattern := range identityMarkerPatterns {
		for _, match := range pattern.FindAllString(script, -1) {
			if identityMarkerPending[match] {
				identityMarkerPendingSeen[match] = true
				continue
			}
			if _, ok := identityMarkerAllowed[match]; ok {
				continue
			}
			deferred := false
			for _, prefix := range identityMarkerDeferredPrefixes {
				deferred = deferred || strings.HasPrefix(match, prefix)
			}
			if !deferred {
				found[match] = true
			}
		}
	}
	result := make([]string, 0, len(found))
	for match := range found {
		result = append(result, match)
	}
	sort.Strings(result)
	return result
}

func TestInstalledComponentsCarryNoProductIdentity(t *testing.T) {
	profile, err := guestprofile.New(guestprofile.Options{Username: "alex", BrowserUsername: "web"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []*guestprofile.Profile{nil, profile} {
		c := DefaultConfig()
		c.GuestProfile = p
		scripts := map[string]string{
			"setup":         c.SetupScript(),
			"start":         c.StartScript(),
			"desktop":       p.Linux(ClaudeDesktopInstallScript()),
			"tools":         p.Linux(ToolsUpdateScript()),
			"debianPrepare": DebianPrepareScript,
		}
		for name, script := range scripts {
			if markers := identityMarkers(script); len(markers) > 0 {
				t.Errorf("%s script (profile %v) still carries identity markers:\n  %s", name, p != nil, strings.Join(markers, "\n  "))
			}
		}
	}
	for match := range identityMarkerPending {
		if !identityMarkerPendingSeen[match] {
			t.Errorf("pending entry %q no longer occurs; remove it", match)
		}
	}
}

func TestMigrationScriptIsNotInstalled(t *testing.T) {
	if strings.TrimSpace(guestMigrationScript) == "" {
		t.Fatal("the one-off migration script is empty")
	}
	start := DefaultConfig().StartScript()
	if strings.Count(start, guestMigrationScript) != 1 {
		t.Fatal("the start script must run the one-off migration exactly once")
	}
	// The migration runs from the SSH session that applies the components;
	// it must never be written to a file inside the environment.
	if regexp.MustCompile(`tee [^\n]*<<'?GUEST_MIGRATION`).MatchString(start) {
		t.Fatal("the migration script is installed as a file")
	}
}
