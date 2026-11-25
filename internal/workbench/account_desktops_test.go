package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/exitpolicy"
	"github.com/sylvinus/agent-vm/internal/managedproxy"
)

func TestDesktopCatalogUsesStableNumbersWithoutStartingGuest(t *testing.T) {
	s := testServer(t)
	accounts := []claudeAccount{{ID: strings.Repeat("a", 24), Label: "工作", Number: 9, DesktopDedicatedNetwork: true}, {ID: strings.Repeat("b", 24), Label: "个人", Number: 3}}
	if err := s.saveClaudeAccounts(claudeAccountList{Accounts: accounts}); err != nil {
		t.Fatal(err)
	}
	var view struct {
		Supported bool
		Accounts  []desktopEntry
	}
	resp := request(s, "GET", "/api/desktop-instances", s.origin, "", true)
	if resp.Code != http.StatusOK || json.Unmarshal(resp.Body.Bytes(), &view) != nil || !view.Supported || len(view.Accounts) != 2 {
		t.Fatalf("catalog: %d %s", resp.Code, resp.Body.String())
	}
	if a := view.Accounts[0]; a.Number != 9 || a.ID != accounts[0].ID || a.Running || !a.Dedicated {
		t.Fatalf("incorrect account state: %+v", a)
	}
	if a := view.Accounts[1]; a.Number != 3 || a.Dedicated {
		t.Fatalf("second entry: %+v", a)
	}
}

func TestDesktopInstanceNeverFallsBackToNativeDefault(t *testing.T) {
	s := testServer(t)
	id := strings.Repeat("a", 24)
	if w := request(s, "POST", "/api/desktop-instances/"+id+"/start", "https://claude.ai", "{}", true); w.Code != 403 {
		t.Fatalf("cross-origin start: %d", w.Code)
	}
	if w := request(s, "POST", "/api/desktop-instances/default/start", s.origin, "{}", true); w.Code != 404 {
		t.Fatalf("invalid instance: %d", w.Code)
	}
	if w := request(s, "POST", "/api/desktop-instances/"+id+"/stop", s.origin, "{}", false); w.Code != 401 {
		t.Fatalf("unauthenticated stop: %d", w.Code)
	}
}

func TestBoundAccountProbeUsesSubscriptionAndGuestCapability(t *testing.T) {
	s := testServer(t)
	first := claudeAccount{ID: strings.Repeat("a", 24), Source: "default", Email: "one@example.org", ProxyNodeID: strings.Repeat("c", 24)}
	second := claudeAccount{ID: strings.Repeat("b", 24), Source: "default", Email: "two@example.org"}
	if err := s.saveClaudeAccounts(claudeAccountList{Accounts: []claudeAccount{first, second}}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []claudeAccount{first, second} {
		route, err := s.boundGuestAccount("default", claudeIdentityHash(a.Email, ""))
		if err != nil {
			t.Fatal(err)
		}
		if a.ID == first.ID && (route == nil || route.ID != first.ID) || a.ID == second.ID && route != nil {
			t.Fatal("default profile took another subscription's route")
		}
	}
}

func TestPendingLoginProbeKeepsSelectedNodeBeforeEnrollment(t *testing.T) {
	s := testServer(t)
	id, node := strings.Repeat("a", 24), strings.Repeat("b", 24)
	if err := s.saveClaudeAccounts(claudeAccountList{Accounts: []claudeAccount{}, PendingLogin: id, PendingProxyNode: node}); err != nil {
		t.Fatal(err)
	}
	route, err := s.boundGuestAccount(id, "")
	if err != nil || route == nil || route.ProxyNodeID != node || route.ID != id {
		t.Fatal("login completion probe lost its draft route", route, err)
	}
	other, err := s.boundGuestAccount(strings.Repeat("c", 24), "")
	if err != nil || other != nil {
		t.Fatal("unrelated login took draft route", other, err)
	}
}

func TestDesktopMetadataRenameHTTP(t *testing.T) {
	s := testServer(t)
	accountID := strings.Repeat("a", 24)
	instanceID := strings.Repeat("b", 24)
	pendingID := strings.Repeat("c", 24)
	if err := s.saveClaudeAccounts(claudeAccountList{
		Accounts:       []claudeAccount{{ID: accountID, Label: "工作", Number: 2, DesktopInstanceID: instanceID}},
		PendingDesktop: pendingID,
	}); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "POST", "/api/desktop-instances/"+instanceID+"/metadata", "https://claude.ai", `{"label":"研发"}`, true); w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin rename: %d", w.Code)
	}
	if w := request(s, "POST", "/api/desktop-instances/"+instanceID+"/metadata", s.origin, `{"label":"研发"}`, false); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated rename: %d", w.Code)
	}
	if w := request(s, "POST", "/api/desktop-instances/"+instanceID+"/metadata", s.origin, `{"label":"   "}`, true); w.Code != http.StatusBadRequest {
		t.Fatalf("blank rename: %d", w.Code)
	}
	if w := request(s, "POST", "/api/desktop-instances/"+strings.Repeat("f", 24)+"/metadata", s.origin, `{"label":"不存在"}`, true); w.Code != http.StatusNotFound {
		t.Fatalf("missing instance rename: %d", w.Code)
	}
	var updated desktopEntry
	if w := request(s, "POST", "/api/desktop-instances/"+instanceID+"/metadata", s.origin, `{"label":" 研发专用桌面 "}`, true); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &updated) != nil || updated.Label != "研发专用桌面" || updated.Number != 2 {
		t.Fatalf("rename bound desktop: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/desktop-instances/default/metadata", s.origin, `{"label":"主力桌面"}`, true); w.Code != http.StatusOK {
		t.Fatalf("rename default desktop: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "POST", "/api/desktop-instances/"+pendingID+"/metadata", s.origin, `{"label":"待登录工作台"}`, true); w.Code != http.StatusOK {
		t.Fatalf("rename pending desktop: %d %s", w.Code, w.Body.String())
	}
	var catalog struct {
		Accounts  []desktopEntry `json:"accounts"`
		Instances []desktopEntry `json:"instances"`
	}
	if w := request(s, "GET", "/api/desktop-instances", s.origin, "", true); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &catalog) != nil {
		t.Fatalf("catalog after rename: %d %s", w.Code, w.Body.String())
	}
	if len(catalog.Accounts) != 1 || catalog.Accounts[0].Label != "工作" {
		t.Fatalf("account label must stay unchanged: %+v", catalog.Accounts)
	}
	labels := map[string]string{}
	for _, item := range catalog.Instances {
		labels[item.ID] = item.Label
	}
	if labels["default"] != "主力桌面" || labels[instanceID] != "研发专用桌面" || labels[pendingID] != "待登录工作台" {
		t.Fatalf("unexpected catalog instance labels: %+v", labels)
	}
}

func TestDesktopDeleteHTTP(t *testing.T) {
	s := testServer(t)
	accountID := strings.Repeat("a", 24)
	instanceID := strings.Repeat("b", 24)
	pendingID := strings.Repeat("c", 24)
	if err := s.saveClaudeAccounts(claudeAccountList{
		Accounts:       []claudeAccount{{ID: accountID, Label: "工作", Number: 5, DesktopInstanceID: instanceID, DesktopStatus: "ready"}},
		PendingDesktop: pendingID,
		DesktopLabels:  map[string]string{instanceID: "自定义桌面", pendingID: "待删桌面"},
	}); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "DELETE", "/api/desktop-instances/"+instanceID, "https://claude.ai", "", true); w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin delete: %d", w.Code)
	}
	if w := request(s, "DELETE", "/api/desktop-instances/"+instanceID, s.origin, "", false); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated delete: %d", w.Code)
	}
	if w := request(s, "DELETE", "/api/desktop-instances/default", s.origin, "", true); w.Code != http.StatusConflict {
		t.Fatalf("stopped vm default delete must return 409: %d", w.Code)
	}
	if w := request(s, "DELETE", "/api/desktop-instances/"+instanceID, s.origin, "", true); w.Code != http.StatusConflict {
		t.Fatalf("stopped vm delete must return 409: %d", w.Code)
	}
	if err := writePrivate(filepath.Join(s.instanceDir(), "cage-running.json"), map[string]int{"pid": os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	// Guest cleanup failure keeps the registry entry intact for retry.
	var calls []string
	s.desktopAction = func(_ context.Context, id string, _ claudeAccount, action string) error {
		calls = append(calls, action+":"+id)
		return errors.New("无法清理客机内的 Desktop 实例数据，暂未删除记录，请重试")
	}
	if w := request(s, "DELETE", "/api/desktop-instances/default", s.origin, "", true); w.Code != http.StatusBadGateway {
		t.Fatalf("failed default guest cleanup: %d %s", w.Code, w.Body.String())
	}
	list, err := s.loadClaudeAccounts()
	if err != nil || list.DefaultDesktopRemoved {
		t.Fatalf("registry mutated after failed default guest delete: %+v %v", list, err)
	}
	if w := request(s, "DELETE", "/api/desktop-instances/"+instanceID, s.origin, "", true); w.Code != http.StatusBadGateway {
		t.Fatalf("failed guest cleanup: %d %s", w.Code, w.Body.String())
	}
	list, err = s.loadClaudeAccounts()
	if err != nil || list.Accounts[0].DesktopInstanceID != instanceID || list.DesktopLabels[instanceID] != "自定义桌面" {
		t.Fatalf("registry mutated after failed guest delete: %+v %v", list, err)
	}
	// Successful guest cleanup deletes default and isolated instances until only one remains.
	s.desktopAction = func(_ context.Context, id string, _ claudeAccount, action string) error {
		calls = append(calls, action+":"+id)
		return nil
	}
	beforeRev := s.desktopCatalogRevision.Load()
	if w := request(s, "DELETE", "/api/desktop-instances/default", s.origin, "", true); w.Code != http.StatusOK {
		t.Fatalf("delete default instance: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "DELETE", "/api/desktop-instances/default", s.origin, "", true); w.Code != http.StatusNotFound {
		t.Fatalf("second default delete must return 404: %d", w.Code)
	}
	if _, err := s.defaultDesktopScope(context.Background()); err == nil {
		t.Fatal("deleted default desktop must not be launchable via defaultDesktopScope")
	}
	if w := request(s, "DELETE", "/api/desktop-instances/"+instanceID, s.origin, "", true); w.Code != http.StatusOK {
		t.Fatalf("delete bound instance: %d %s", w.Code, w.Body.String())
	}
	// Pending is now the sole remaining instance and cannot be deleted.
	if w := request(s, "DELETE", "/api/desktop-instances/"+pendingID, s.origin, "", true); w.Code != http.StatusBadRequest {
		t.Fatalf("last remaining desktop delete must return 400: %d %s", w.Code, w.Body.String())
	}
	if w := request(s, "DELETE", "/api/desktop-instances/"+instanceID, s.origin, "", true); w.Code != http.StatusNotFound {
		t.Fatalf("second delete must return 404: %d", w.Code)
	}
	if s.desktopCatalogRevision.Load() <= beforeRev {
		t.Fatal("delete did not advance desktopCatalogRevision")
	}
	list, err = s.loadClaudeAccounts()
	if err != nil || !list.DefaultDesktopRemoved || len(list.Accounts) != 1 || list.Accounts[0].ID != accountID || list.Accounts[0].DesktopInstanceID != "" || list.PendingDesktop != pendingID || list.DesktopLabels[pendingID] != "待删桌面" || list.DesktopLabels[instanceID] != "" {
		t.Fatalf("unexpected state after delete: %+v %v", list, err)
	}
	wantCalls := []string{"delete:default", "delete:" + instanceID, "delete:default", "delete:" + instanceID}
	if strings.Join(calls, ",") != strings.Join(wantCalls, ",") {
		t.Fatalf("unexpected desktopAction calls: %v", calls)
	}
	// When only default remains, default itself cannot be deleted either.
	if err := s.saveClaudeAccounts(claudeAccountList{Accounts: []claudeAccount{{ID: accountID, Label: "工作"}}}); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "DELETE", "/api/desktop-instances/default", s.origin, "", true); w.Code != http.StatusBadRequest {
		t.Fatalf("sole remaining default delete must return 400: %d %s", w.Code, w.Body.String())
	}
}

func TestDesktopInstanceResizeHTTP(t *testing.T) {
	s := testServer(t)
	instanceID := strings.Repeat("b", 24)
	if err := s.saveClaudeAccounts(claudeAccountList{
		Accounts: []claudeAccount{{ID: strings.Repeat("a", 24), Label: "工作", Number: 2, DesktopInstanceID: instanceID}},
	}); err != nil {
		t.Fatal(err)
	}
	path := "/api/desktop-instances/" + instanceID + "/resize"
	if w := request(s, "POST", path, "https://claude.ai", `{"width":1000,"height":800,"scale":2}`, true); w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin resize: %d", w.Code)
	}
	if w := request(s, "GET", path, s.origin, "", true); w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("resize by GET: %d", w.Code)
	}
	for _, body := range []string{
		`{"width":100,"height":800}`,
		`{"width":1000,"height":9000}`,
		`{"width":1000,"height":800,"scale":5}`,
		`{"width":1000,"height":800,"scale":0.5}`,
		`{"width":1000,"height":800,"scale":"2;reboot"}`,
		`{"width":1000,"height":800,"display":":0"}`,
	} {
		if w := request(s, "POST", path, s.origin, body, true); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid resize %s: %d", body, w.Code)
		}
	}
	if w := request(s, "POST", path, s.origin, `{"width":1000,"height":800,"scale":1.5}`, true); w.Code != http.StatusConflict {
		t.Fatalf("resize with a stopped environment: %d", w.Code)
	}
	if err := writePrivate(filepath.Join(s.instanceDir(), "cage-running.json"), map[string]int{"pid": os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	if w := request(s, "POST", "/api/desktop-instances/"+strings.Repeat("f", 24)+"/resize", s.origin, `{"width":1000,"height":800}`, true); w.Code != http.StatusNotFound {
		t.Fatalf("resize of another instance: %d", w.Code)
	}
}

func TestDesktopInstanceCredentialsReportDensity(t *testing.T) {
	for _, tc := range []struct {
		output  string
		ok      bool
		dynamic bool
		density float64
	}{
		// A session started by an earlier version keeps 96 DPI.
		{"abcd12341", true, false, 1},
		{"abcd12340 2\n", true, true, 2},
		{"abcd12341 1.5\n", true, true, 1.5},
		// A missing configured density falls back to the default one.
		{"abcd12340 ", true, true, 2},
		{"abcd1234", false, false, 0},
		{"abcd12342", false, false, 0},
		{"abcd12341x", false, false, 0},
	} {
		reply, ok := accountDesktopCredentials([]byte(tc.output))
		if ok != tc.ok {
			t.Fatalf("%q: ok %v", tc.output, ok)
		}
		if !ok {
			continue
		}
		if reply["password"] != "abcd1234" || reply["native_input"] != (tc.output[8] == '1') || reply["dynamic_scale"] != tc.dynamic || reply["display_scale"] != tc.density {
			t.Fatalf("%q: %v", tc.output, reply)
		}
	}
}

func TestLinuxAccountDesktopDeleteScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash script runs in Linux guest")
	}
	home := t.TempDir()
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "systemctl.log")
	systemctl := filepath.Join(bin, "systemctl")
	sudo := filepath.Join(bin, "sudo")
	flock := filepath.Join(bin, "flock")
	scriptSystemctl := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + logPath + "\ncase \"$1\" in\n  show) echo active ;;\n  is-active) exit 1 ;;\nesac\nexit 0\n"
	scriptSudo := "#!/bin/sh\nexec \"$@\"\n"
	scriptFlock := "#!/bin/sh\nexit 0\n"
	for path, content := range map[string]string{systemctl: scriptSystemctl, sudo: scriptSudo, flock: scriptFlock} {
		if err := os.WriteFile(path, []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
	}
	targetID := strings.Repeat("a", 24)
	otherID := strings.Repeat("b", 24)
	targetDir := filepath.Join(home, ".local/share/desktop-profiles", targetID)
	otherDir := filepath.Join(home, ".local/share/desktop-profiles", otherID)
	accountDir := filepath.Join(home, ".local/share/cage/claude-accounts", targetID, "config")
	for _, dir := range []string{filepath.Join(targetDir, "data"), otherDir, accountDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	readonlyFile := filepath.Join(targetDir, "data", "locked")
	if err := os.WriteFile(readonlyFile, []byte("x"), 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "launch.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	runDelete := func(id string) {
		t.Helper()
		cmd := exec.Command("/bin/bash", "-c", linuxAccountDesktop, "--", id, "default", "delete")
		cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("delete %s failed: %v\n%s", id, err, out)
		}
	}
	runDelete(targetID)
	if _, err := os.Stat(targetDir); !os.IsNotExist(err) {
		t.Fatalf("target profile dir still exists: %v", err)
	}
	if _, err := os.Stat(otherDir); err != nil {
		t.Fatalf("other profile dir was affected: %v", err)
	}
	if _, err := os.Stat(accountDir); err != nil {
		t.Fatalf("claude account CLI config was affected: %v", err)
	}
	// Deleting an instance whose directory was never created succeeds without creating it.
	missingID := strings.Repeat("c", 24)
	runDelete(missingID)
	if _, err := os.Stat(filepath.Join(home, ".local/share/desktop-profiles", missingID)); !os.IsNotExist(err) {
		t.Fatalf("missing profile dir should not be created on delete: %v", err)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), "stop desktop-"+targetID+".service") || !strings.Contains(string(logged), "reset-failed desktop-"+targetID+".service") {
		t.Fatalf("expected systemctl stop and reset-failed, got:\n%s", logged)
	}
	// Deleting the default desktop stops cage-claude-desktop.service and clears default app data and keyring without touching other desktops or Claude Code CLI data.
	defaultDirs := []string{
		filepath.Join(home, ".config/Claude"),
		filepath.Join(home, ".cache/Claude"),
		filepath.Join(home, ".local/share/Claude"),
		filepath.Join(home, ".local/share/keyrings"),
	}
	cliDir := filepath.Join(home, ".claude")
	for _, dir := range append(append([]string{}, defaultDirs...), cliDir) {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".config/Claude/locked"), []byte("x"), 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cliDir, "history.jsonl"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runDelete("default")
	for _, dir := range defaultDirs {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("default desktop dir %s still exists: %v", dir, err)
		}
	}
	if _, err := os.Stat(otherDir); err != nil {
		t.Fatalf("other profile dir was affected by default delete: %v", err)
	}
	if _, err := os.Stat(accountDir); err != nil {
		t.Fatalf("claude account CLI config was affected by default delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cliDir, "history.jsonl")); err != nil {
		t.Fatalf("default Claude Code history was affected by default delete: %v", err)
	}
	// Deleting default again when its directories do not exist succeeds cleanly.
	runDelete("default")
	logged, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), "stop cage-claude-desktop.service") || !strings.Contains(string(logged), "reset-failed cage-claude-desktop.service") {
		t.Fatalf("expected cage-claude-desktop.service stop and reset-failed, got:\n%s", logged)
	}
}

func TestLinuxAccountDesktopScriptWiresPortalAndIsolatedInput(t *testing.T) {
	if bash, err := exec.LookPath("bash"); err == nil {
		cmd := exec.Command(bash, "-n")
		cmd.Stdin = strings.NewReader(linuxAccountDesktop)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("linux-account-desktop.sh syntax error: %v\n%s", err, out)
		}
	}
	for _, want := range []string{
		"/usr/local/bin/cage-desktop-files --open",
		"/usr/local/bin/cage-desktop-files &",
		"export GTK_USE_PORTAL=1",
		"--xdg-portal-required-version=3",
		"/usr/local/bin/cage-input-session",
		"--property=PrivateTmp=yes",
		"--property=EnvironmentFile=-/etc/claude-cage/environment",
		// Not TZ= or CAGE_LANGUAGE= on the unit: the environment file's
		// values would win over them.
		"--property=Environment=account_tz=${CAGE_ACCOUNT_TZ}",
		"--property=Environment=account_language=${CAGE_ACCOUNT_LANGUAGE}",
		"export TZ=$account_tz",
		"client_runtime=(/usr/bin/python3 -I /usr/local/bin/client_runtime.py)",
		`${client_runtime[@]+"${client_runtime[@]}"} claude-desktop`,
		"browser-language",
		"unset CAGE_LANGUAGE",
		"dbus-update-activation-environment DISPLAY XAUTHORITY HOME XDG_RUNTIME_DIR TZ LANG LC_ALL",
		`--lang=${desktop_lang}`,
		// The viewer's density reaches the instance through its settings manager.
		`printf '%s\n' "$!" > "$XDG_RUNTIME_DIR/xsettings.pid"`,
		`"$XDG_RUNTIME_DIR/base-display-scale"`,
		`"$XDG_RUNTIME_DIR/xsettings.fixed"`,
	} {
		if !strings.Contains(linuxAccountDesktop, want) {
			t.Fatalf("linux-account-desktop.sh missing %q", want)
		}
	}
	if strings.Contains(linuxAccountDesktop, "BindPaths=/tmp/.X11-unix") {
		t.Fatal("linux-account-desktop.sh must keep /tmp/.X11-unix isolated under PrivateTmp")
	}
	// /run is mounted noexec in the guest. An opener there is skipped and the
	// system xdg-open keeps the quotes around --user-data-dir, so the login
	// callback starts a Desktop without this instance's data folder.
	if strings.Contains(linuxAccountDesktop, `"$XDG_RUNTIME_DIR/bin`) || !strings.Contains(linuxAccountDesktop, `opener="$base/bin"`) {
		t.Fatal("linux-account-desktop.sh must install its xdg-open in the private profile, not under /run")
	}
}

func TestAccountRegionOverridesFollowNodeLocationAndPreferences(t *testing.T) {
	profile := t.TempDir()
	nodeID := strings.Repeat("d", 24)
	policyDir := filepath.Join(managedproxy.Dir(profile), "account-policy")
	if err := os.MkdirAll(policyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Fallback to ExitPolicies country when node location cache is absent.
	store := managedproxy.Store{
		Version: 1,
		Port:    17896,
		Auth:    managedproxy.Token(),
		ExitPolicies: map[string]exitpolicy.Policy{
			nodeID: {Mode: "dynamic", Scope: "country", CountryCode: "SG"},
		},
	}
	if err := store.Save(profile); err != nil {
		t.Fatal(err)
	}
	tz, lang := accountRegionOverrides(profile, nodeID, Preferences{AutoTimezone: true, AutoLanguage: true})
	if tz != "" || lang != "en-SG" {
		t.Fatalf("fallback overrides = (%q, %q), want (\"\", \"en-SG\")", tz, lang)
	}
	// Verified node location overrides both timezone and language.
	if err := os.WriteFile(filepath.Join(policyDir, nodeID+".location.json"), []byte(`{"country_code":"JP","timezone":"Asia/Tokyo"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tz, lang = accountRegionOverrides(profile, nodeID, Preferences{AutoTimezone: true, AutoLanguage: true})
	if tz != "Asia/Tokyo" || lang != "ja-JP" {
		t.Fatalf("verified overrides = (%q, %q), want (\"Asia/Tokyo\", \"ja-JP\")", tz, lang)
	}
	// Disabled preferences suppress overrides.
	tz, lang = accountRegionOverrides(profile, nodeID, Preferences{AutoTimezone: false, AutoLanguage: true})
	if tz != "" || lang != "ja-JP" {
		t.Fatalf("auto_timezone=false overrides = (%q, %q)", tz, lang)
	}
	tz, lang = accountRegionOverrides(profile, nodeID, Preferences{AutoTimezone: true, AutoLanguage: false})
	if tz != "Asia/Tokyo" || lang != "" {
		t.Fatalf("auto_language=false overrides = (%q, %q)", tz, lang)
	}
	// Invalid or Local timezone is ignored.
	if err := os.WriteFile(filepath.Join(policyDir, nodeID+".location.json"), []byte(`{"country_code":"US","timezone":"Local"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tz, lang = accountRegionOverrides(profile, nodeID, Preferences{AutoTimezone: true, AutoLanguage: true})
	if tz != "" || lang != "en-US" {
		t.Fatalf("Local timezone must be rejected: (%q, %q)", tz, lang)
	}
}
