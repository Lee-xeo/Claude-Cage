package cage

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDebianMirrorFailureFallbackAndPreference(t *testing.T) {
	old := "https://mirrors.nju.edu.cn/debian\tpriority:1\nhttps://mirrors.ustc.edu.cn/debian\tpriority:2\nhttps://deb.debian.org/debian\tpriority:3\n"
	for _, mode := range []string{"reachable", "bookworm", "tls-failure", "official-failure", "nju-failure", "official", "custom", "old", "reboot", "invalid", "nju", "ustc", "ustc-nju-failure", "host-ustc", "host-invalid"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			mirrors := filepath.Join(dir, "mirrors")
			os.MkdirAll(mirrors, 0755)
			list := filepath.Join(mirrors, "debian.list")
			content := "https://deb.debian.org/debian\n"
			if mode == "custom" {
				content = "https://custom.invalid/debian\n"
			}
			if mode == "old" {
				content = old
			}
			if mode == "reboot" {
				content = "# Cage managed Debian mirrors\nhttps://deb.debian.org/debian\tpriority:1\n"
			}
			os.WriteFile(list, []byte(content), 0644)
			os.WriteFile(filepath.Join(mirrors, "debian-security.list"), []byte("https://deb.debian.org/debian-security\n"), 0644)
			preference := filepath.Join(dir, "preference")
			switch mode {
			case "official", "invalid", "nju", "ustc":
				os.WriteFile(preference, []byte(mode+"\n"), 0644)
			case "ustc-nju-failure":
				os.WriteFile(preference, []byte("ustc\n"), 0644)
			case "host-ustc":
				// The host's choice replaces an older saved preference.
				os.WriteFile(preference, []byte("official\n"), 0644)
			}
			for name, body := range map[string]string{
				"sudo": "#!/bin/sh\nexec \"$@\"\n",
				"apt-helper": `#!/bin/sh
printf '%s\n' "$*" >> "$FIXTURE/probes"
[ "$MODE" != tls-failure ] || exit 1
case "$*" in
 *deb.debian.org*) [ "$MODE" != nju-failure ] && [ "$MODE" != official-failure ] || exit 1;;
 *mirrors.nju*) [ "$MODE" != nju-failure ] && [ "$MODE" != ustc-nju-failure ] || exit 1;;
esac
exit 0
`,
				"timeout": "#!/bin/sh\nshift\nexec \"$@\"\n",
			} {
				os.WriteFile(filepath.Join(dir, name), []byte(body), 0755)
			}
			script := strings.ReplaceAll(DebianMirrorsScript, "/etc/apt/mirrors", mirrors)
			script = strings.ReplaceAll(script, "/etc/workbench/debian-mirror", preference)
			// The former location is read only when the current one is absent;
			// keep it inside the fixture so the machine running the test never
			// supplies a preference.
			script = strings.ReplaceAll(script, "/etc/claude-cage/debian-mirror", filepath.Join(dir, "former-preference"))
			script = strings.ReplaceAll(script, "/usr/lib/apt/apt-helper", filepath.Join(dir, "apt-helper"))
			suite := "trixie"
			if mode == "bookworm" {
				suite = "bookworm"
			}
			osRelease := filepath.Join(dir, "os-release")
			if err := os.WriteFile(osRelease, []byte("VERSION_CODENAME="+suite+"\n"), 0644); err != nil {
				t.Fatal(err)
			}
			script = strings.ReplaceAll(script, "/etc/os-release", osRelease)
			run := func(choice string) ([]byte, error) {
				cmd := exec.Command("sh", "-c", choice+script)
				cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "FIXTURE="+dir, "MODE="+mode)
				return cmd.CombinedOutput()
			}
			first := ""
			switch mode {
			case "host-ustc":
				first = debianMirrorChoice("ustc")
			case "host-invalid":
				// Go never emits an unknown value; the guest script still rejects one.
				first = "debian_mirror_choice=fastest\n"
			}
			out, err := run(first)
			if mode == "invalid" || mode == "host-invalid" {
				if err == nil {
					t.Fatal("invalid preference accepted")
				}
				return
			}
			if err != nil {
				t.Fatalf("%v %s", err, out)
			}
			data, _ := os.ReadFile(list)
			got := string(data)
			switch mode {
			case "custom":
				if got != content {
					t.Fatal("custom source overwritten")
				}
			case "official", "tls-failure", "reachable", "bookworm", "old", "reboot":
				if strings.Contains(got, "mirrors.") || !strings.Contains(got, "deb.debian.org") {
					t.Fatal(got)
				}
			case "nju-failure":
				if strings.Contains(got, "mirrors.nju") || !strings.Contains(got, "mirrors.ustc") {
					t.Fatal(got)
				}
			case "ustc", "host-ustc":
				// The mirror measured fastest comes first, not a fixed order.
				if got != "# Cage managed Debian mirrors\nhttps://mirrors.ustc.edu.cn/debian\tpriority:1\nhttps://mirrors.nju.edu.cn/debian\tpriority:2\nhttps://deb.debian.org/debian\tpriority:3\n" {
					t.Fatal(got)
				}
			case "ustc-nju-failure":
				if got != "# Cage managed Debian mirrors\nhttps://mirrors.ustc.edu.cn/debian\tpriority:1\nhttps://deb.debian.org/debian\tpriority:2\n" {
					t.Fatal(got)
				}
			default:
				// official-failure and nju: nju, ustc, then the official source.
				if got != "# Cage managed Debian mirrors\n"+old {
					t.Fatal(got)
				}
			}
			if mode == "nju" || mode == "ustc" || mode == "host-ustc" {
				if probes, _ := os.ReadFile(filepath.Join(dir, "probes")); strings.Contains(string(probes), "deb.debian.org") {
					t.Fatal("domestic preference probed the official source first")
				}
			}
			if mode == "host-ustc" {
				// The host's measured choice is kept for later runs without it.
				if saved, _ := os.ReadFile(preference); strings.TrimSpace(string(saved)) != "ustc" {
					t.Fatalf("host choice not persisted: %q", saved)
				}
			}
			if mode == "official" {
				if probes, _ := os.ReadFile(filepath.Join(dir, "probes")); len(probes) > 0 {
					t.Fatal("official preference contacted mirrors")
				}
			}
			if mode == "reachable" || mode == "bookworm" {
				probes, err := os.ReadFile(filepath.Join(dir, "probes"))
				if err != nil || !strings.Contains(string(probes), "/dists/"+suite+"/InRelease") || !strings.Contains(string(probes), "/dists/"+suite+"-security/InRelease") {
					t.Fatalf("wrong distribution probes: %s, %v", probes, err)
				}
				if strings.Contains(string(probes), "mirrors.") {
					t.Fatal("reachable official source still contacted domestic mirrors")
				}
			}
			if out, err := run(""); err != nil {
				t.Fatalf("repeat: %v %s", err, out)
			}
			after, _ := os.ReadFile(list)
			if string(after) != got {
				t.Fatal("preference changed on repeated setup")
			}
		})
	}
}

// The host's choice reaches only the root shell that rewrites the sources.
// It must not become an exported variable that later installers inherit.
func TestDebianPrepareScriptKeepsChoiceOutOfEnvironment(t *testing.T) {
	if DebianPrepareScriptFor("fastest") != DebianPrepareScript || strings.Contains(DebianPrepareScript, "debian_mirror_choice=") {
		t.Fatal("unknown or empty preference reached the guest")
	}
	script := DebianPrepareScriptFor("ustc")
	if !strings.Contains(script, "\ndebian_mirror_choice=ustc\n") || strings.Contains(script, "export debian_mirror_choice") || strings.Contains(script, DebianMirrorEnv+"=") {
		t.Fatalf("choice not passed as a plain shell variable: %q", script)
	}
}
