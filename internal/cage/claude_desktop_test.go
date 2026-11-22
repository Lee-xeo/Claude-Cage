package cage

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopCallbackFocusAfterHandoff(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	for _, identity := range []string{"cage", "cage-browser"} {
		for _, tc := range []struct {
			name, url, path, login string
			fail, parameter        bool
		}{
			{"callback", "claude://login?code=private-test-value", "/desktop-focus", "", false, false},
			{"failed-callback", "claude://login?code=private-test-value", "", "", true, false},
			{"web-link", "https://example.com/login", "/new", "", false, false},
			{"cli-login", "https://claude.ai/oauth/authorize?state=private-test-value", "/login-open", "1234567890abcdef", false, false},
			{"finish-cli-login", "--finish-login", "/login-finish", "1234567890abcdef", false, false},
			{"cli-login-argument", "https://claude.ai/oauth/authorize?state=private-test-value", "/login-open", strings.Repeat("a", 24), false, true},
			{"finish-cli-login-argument", "--finish-login", "/login-finish", strings.Repeat("a", 24), false, true},
		} {
			t.Run(identity+"/"+tc.name, func(t *testing.T) {
				dir := t.TempDir()
				type request struct{ Path, Auth, URL, Login string }
				requests := make(chan request, 2)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body struct {
						URL   string `json:"url"`
						Login string `json:"login"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					requests <- request{r.URL.Path, r.Header.Get("Authorization"), body.URL, body.Login}
					w.Write([]byte(`{"ok":true}`))
				}))
				defer server.Close()
				config := filepath.Join(dir, "control.json")
				if err := os.WriteFile(config, []byte(`{"token":"test-only"}`), 0600); err != nil {
					t.Fatal(err)
				}
				// Preserve the production shell flow; sudo only substitutes the two
				// guest identities and the app. Node performs the real HTTP request.
				sudo := "#!/bin/sh\nshift\nuser=$1\nshift\nif [ \"$user\" = cage ]; then exit \"$CALLBACK_EXIT\"; fi\nif [ \"$TEST_IDENTITY\" = cage-browser ]; then echo 'sudo denied' >&2; exit 1; fi\nshift\nexec \"$TEST_NODE\" \"$@\"\n"
				if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(sudo), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "id"), []byte("#!/bin/sh\nprintf '%s\\n' \"$TEST_IDENTITY\"\n"), 0755); err != nil {
					t.Fatal(err)
				}
				script := strings.ReplaceAll(guestOpen, "/etc/claude-cage/browser-control.json", config)
				script = strings.ReplaceAll(script, "http://127.0.0.1:18500", server.URL)
				script = strings.ReplaceAll(script, "/usr/bin/node", "\"$TEST_NODE\"")
				code := "0"
				if tc.fail {
					code = "1"
				}
				args := []string{"-c", script, "open"}
				login := tc.login
				if tc.parameter {
					args = append(args, "--login-id", tc.login)
					login = ""
				}
				args = append(args, tc.url)
				cmd := exec.Command("sh", args...)
				cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "TEST_NODE="+node, "TEST_IDENTITY="+identity, "CALLBACK_EXIT="+code, "CAGE_CLAUDE_LOGIN="+login)
				out, err := cmd.CombinedOutput()
				if tc.fail {
					if err == nil {
						t.Fatal("failed handoff reported success")
					}
					select {
					case r := <-requests:
						t.Fatalf("failed handoff emitted focus: %+v", r)
					default:
					}
					return
				}
				if err != nil {
					t.Fatalf("%v: %s", err, out)
				}
				select {
				case r := <-requests:
					wantURL := ""
					if tc.path == "/new" || tc.path == "/login-open" {
						wantURL = tc.url
					}
					if r.Path != tc.path || r.Auth != "Bearer test-only" || r.URL != wantURL || r.Login != tc.login {
						t.Fatalf("unexpected request: %+v", r)
					}
				default:
					t.Fatal("handoff notification missing")
				}
			})
		}
	}
}

func TestTailnetBrowserOpenPassesOnlyReturnSignal(t *testing.T) {
	bin := t.TempDir()
	helper := filepath.Join(bin, "return-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\""), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "sudo"), []byte("#!/bin/sh\nshift 4\nexec \"$@\""), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "id"), []byte("#!/bin/sh\nprintf '%s' \"$TEST_IDENTITY\""), 0755); err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(guestOpen, "/usr/local/bin/tailnet", helper)
	for _, identity := range []string{"cage", "cage-browser"} {
		cmd := exec.Command("sh", "-c", script, "open", "tailscale://return?code=private-fixture")
		cmd.Env = append(os.Environ(), "PATH="+bin+":/usr/bin:/bin", "TEST_IDENTITY="+identity)
		if out, err := cmd.CombinedOutput(); err != nil || string(out) != "--browser-return\n" {
			t.Fatalf("unexpected handoff for %s: %q %v", identity, out, err)
		}
	}
}

func TestClaudeDesktopInstallerRejectsUntrustedKey(t *testing.T) {
	dir := t.TempDir()
	commands := map[string]string{
		"sudo":      "#!/bin/sh\nif [ \"$1\" = env ]; then exec \"$@\"; fi\necho blocked-privileged-operation >&2; exit 97\n",
		"apt-get":   "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$HOME/apt-requests\"\n",
		"systemctl": "#!/bin/sh\nexit 3\n",
		"flock":     "#!/bin/sh\nexit 0\n",
		"dpkg":      "#!/bin/sh\necho arm64\n",
		"curl":      "#!/bin/sh\nexit 0\n",
		"gpg":       "#!/bin/sh\necho 'fpr:::::::::UNTRUSTEDKEY:'\n",
	}
	for name, body := range commands {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("bash", "-s")
	cmd.Stdin = strings.NewReader(ClaudeDesktopInstallScript())
	cmd.Env = append(os.Environ(), "HOME="+dir, "PATH="+dir+":"+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "signing key did not match") {
		t.Fatalf("untrusted key was not rejected: %v %s", err, out)
	}
	requests, _ := os.ReadFile(filepath.Join(dir, "apt-requests"))
	if strings.Contains(string(requests), "claude-desktop") {
		t.Fatal("desktop package requested before source authentication")
	}
}

func TestClaudeDesktopScriptsParse(t *testing.T) {
	for name, script := range map[string]string{"install": ClaudeDesktopInstallScript(), "provision": ClaudeDesktopProvisionScript(), "session": claudeDesktopSession, "finish": desktopFinish, "start": claudeDesktopStart, "open": claudeDesktopOpen, "browser-open": guestOpen} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command("bash", "-n")
			cmd.Stdin = strings.NewReader(script)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
		})
	}
}

func TestDesktopAuthBrowserHardeningAndNoIncognito(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	home := filepath.Join(dir, "home")
	runtimeDir := filepath.Join(dir, "runtime")
	for _, p := range []string{bin, home, runtimeDir} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	scriptPath := filepath.Join(dir, "desktop-auth-browser.py")
	if err := os.WriteFile(scriptPath, []byte(desktopAuthBrowser), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "browser-language"), []byte("ja-JP\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	capture := filepath.Join(dir, "capture.json")
	fakeChromium := "#!" + py + "\nimport json, os, pathlib, sys\n" +
		"profile = ''\n" +
		"for arg in sys.argv[1:]:\n" +
		"    if arg.startswith('--user-data-dir='):\n" +
		"        profile = arg.split('=', 1)[1]\n" +
		"prefs = {}\n" +
		"p = pathlib.Path(profile) / 'Default' / 'Preferences'\n" +
		"if p.is_file():\n" +
		"    prefs = json.loads(p.read_text(encoding='utf-8'))\n" +
		"cage_keys = sorted(k for k in os.environ if k.upper().startswith('CAGE_'))\n" +
		"pathlib.Path(os.environ['TEST_CAPTURE']).write_text(json.dumps({'args': sys.argv[1:], 'prefs': prefs, 'cage_keys': cage_keys, 'tz': os.environ.get('TZ', '')}), encoding='utf-8')\n"
	if err := os.WriteFile(filepath.Join(bin, "chromium"), []byte(fakeChromium), 0o755); err != nil {
		t.Fatal(err)
	}

	runCase := func(targetURL string) (args []string, prefs map[string]any, cageKeys []string, tz string) {
		t.Helper()
		_ = os.Remove(capture)
		cmd := exec.Command(py, "-I", scriptPath, targetURL)
		cmd.Env = []string{
			"PATH=" + bin + ":/usr/bin:/bin",
			"HOME=" + home,
			"XDG_RUNTIME_DIR=" + runtimeDir,
			"TZ=Asia/Tokyo",
			"CAGE_LANGUAGE=en-US",
			"CAGE_INTERNAL_MARKER=leak",
			"TEST_CAPTURE=" + capture,
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("desktop-auth-browser.py failed: %v\n%s", err, out)
		}
		raw, err := os.ReadFile(capture)
		if err != nil {
			t.Fatalf("capture missing: %v", err)
		}
		var got struct {
			Args     []string       `json:"args"`
			Prefs    map[string]any `json:"prefs"`
			CageKeys []string       `json:"cage_keys"`
			TZ       string         `json:"tz"`
		}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		return got.Args, got.Prefs, got.CageKeys, got.TZ
	}

	// Sign-in URL uses a disposable profile dir in XDG_RUNTIME_DIR, never --incognito,
	// applies WebRTC/QUIC/AutomationControlled/lang flags, and strips CAGE_* env vars.
	args, prefs, cageKeys, tz := runCase("https://claude.ai/oauth/authorize?state=test")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--disable-quic",
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
		"--disable-blink-features=AutomationControlled",
		"--lang=ja-JP",
		"--accept-lang=ja-JP",
		"--user-data-dir=" + filepath.Join(runtimeDir, "login-"),
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("sign-in args missing %q: %v", want, args)
		}
	}
	if strings.Contains(joined, "--incognito") {
		t.Fatalf("sign-in args must not enable --incognito: %v", args)
	}
	if len(cageKeys) != 0 {
		t.Fatalf("CAGE_* env leaked into chromium: %v", cageKeys)
	}
	if tz != "Asia/Tokyo" {
		t.Fatalf("TZ not preserved: %q", tz)
	}
	intl, _ := prefs["intl"].(map[string]any)
	if intl["accept_languages"] != "ja-JP" || intl["selected_languages"] != "ja-JP" {
		t.Fatalf("unexpected Preferences intl: %+v", prefs)
	}
	// Temp profile must be cleaned up after sign-in browser exits.
	matches, _ := filepath.Glob(filepath.Join(runtimeDir, "login-*"))
	if len(matches) != 0 {
		t.Fatalf("temporary sign-in profile was not cleaned up: %v", matches)
	}

	// Regular URL reuses HOME/.config/chromium with the same hardening flags.
	regArgs, _, regCageKeys, _ := runCase("https://example.org/docs")
	regJoined := strings.Join(regArgs, " ")
	for _, want := range []string{
		"--user-data-dir=" + filepath.Join(home, ".config/chromium"),
		"--disable-quic",
		"--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
		"--disable-blink-features=AutomationControlled",
		"--lang=ja-JP",
		"--accept-lang=ja-JP",
	} {
		if !strings.Contains(regJoined, want) {
			t.Fatalf("regular args missing %q: %v", want, regArgs)
		}
	}
	if len(regCageKeys) != 0 {
		t.Fatalf("CAGE_* env leaked into regular chromium: %v", regCageKeys)
	}
}
