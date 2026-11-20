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
	regexp.MustCompile(`\bCAGE_(?:LANGUAGE|WORKBENCH|TERMINAL_COMMAND|CLAUDE_ACCOUNT_ID|CLAUDE_SUBJECT|CLAUDE_LOGIN|LOGIN_CONNECTION|LOGIN_OWNER|ACCOUNT_BROWSER|DESKTOP_SESSION|NOTIFY_SOCKET|DISPLAY_ACTIVITY|DISPLAY_OUTPUT|CAPTURE_ID|CCS_CAPTURE|PHONE_[A-Z_]+)\b`),
	regexp.MustCompile(`\bcage-password-vault\.json\b`),
	regexp.MustCompile(`\bglobalThis\.cage[A-Za-z0-9_]*\b`),
	regexp.MustCompile(`worldName:\s*["'][^"']*(?:cage|Cage)[^"']*["']`),
	regexp.MustCompile(`\bconst\s+(?:world|binding)\s*=\s*["'][^"']*(?:cage|Cage)[^"']*["']`),
	regexp.MustCompile(`--class=[^\s'"]*(?:cage|Cage)[^\s'"]*`),
	regexp.MustCompile(`__gtype_name__\s*=\s*['"][^'"]*(?:cage|Cage)[^'"]*['"]`),
	regexp.MustCompile(`\.cage-download-`),
	regexp.MustCompile(`handle_token[^\n]*'cage_'`),
	regexp.MustCompile(`printf\s+'# Cage managed Debian mirrors`),
	regexp.MustCompile("const\\s+name\\s*=\\s*`cage-"),
	regexp.MustCompile(`name\s*=\s*f'cage-phone-`),
}

// Explicit exceptions. Each entry is an exact match together with the reason
// it may remain in what the environment receives.
var identityMarkerAllowed = map[string]string{
	// Chromium derives the unpacked extension ID from its path. Existing
	// environments keep it so the extension keeps its state; environments
	// created with a guest profile receive a neutral path instead.
	"/usr/local/lib/cage-split": "legacy extension path",
	// The Debian mirror preference is written by the user. The preparation
	// that runs before the components are applied still finds it under the
	// former name until the update has copied it.
	"/etc/claude-cage/debian-mirror": "read of a user preference before it is copied",
	// Pre-relocation plugin registries may still record the former data path;
	// claude-account recognizes it on read and writes back the neutral path.
	".local/share/cage/claude-accounts": "read-only normalization of pre-relocation plugin registries",
}

func identityMarkers(script string, p *guestprofile.Profile) []string {
	for _, oneOff := range []string{guestMigrationScript, guestLegacyCleanup} {
		script = strings.ReplaceAll(script, p.Linux(oneOff), "")
	}
	found := map[string]bool{}
	for _, pattern := range identityMarkerPatterns {
		for _, match := range pattern.FindAllString(script, -1) {
			if _, ok := identityMarkerAllowed[match]; ok {
				continue
			}
			found[match] = true
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
			if markers := identityMarkers(script, p); len(markers) > 0 {
				t.Errorf("%s script (profile %v) still carries identity markers:\n  %s", name, p != nil, strings.Join(markers, "\n  "))
			}
		}
	}
}

func TestMigrationScriptIsNotInstalled(t *testing.T) {
	start := DefaultConfig().StartScript()
	for name, oneOff := range map[string]string{"migration": guestMigrationScript, "cleanup": guestLegacyCleanup} {
		if strings.TrimSpace(oneOff) == "" {
			t.Fatalf("the one-off %s script is empty", name)
		}
		if strings.Count(start, oneOff) != 1 {
			t.Fatalf("the start script must run the one-off %s exactly once", name)
		}
	}
	if strings.Index(start, guestLegacyCleanup) < strings.Index(start, guestStart) {
		t.Fatal("former programs must be removed only after the current services started")
	}
	// The migration runs from the SSH session that applies the components;
	// it must never be written to a file inside the environment.
	if regexp.MustCompile(`tee [^\n]*<<'?LEGACY_`).MatchString(start) {
		t.Fatal("the migration script is installed as a file")
	}
}
