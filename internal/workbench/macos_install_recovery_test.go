package workbench

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/sylvinus/agent-vm/internal/guestos"
)

func trustedRecoveryRequest(s *Server, method, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, s.origin+"/api/macos/install-recovery", strings.NewReader(body))
	req.Header.Set("Origin", s.origin)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: s.cookieName(), Value: s.session})
	req.AddCookie(&http.Cookie{Name: s.cookieName() + "-desktop", Value: trustedViewProof(s.token)})
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	return w
}

func diagnosticEvents(t *testing.T, profile string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(profile, "logs", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Recovery refusals must tell the client apart why the entry is unusable while
// the precise, sanitized cause goes to the diagnostics log. Neither channel
// may reveal the pending guest identity.
func TestMacOSInstallRecoveryIdentityFailuresAreDistinctAndRecorded(t *testing.T) {
	s := testServer(t)
	id := uuid.New()
	if err := guestos.SaveInstallation(s.profile, guestos.Installation{Version: 1, System: guestos.MacOS, ID: id}); err != nil {
		t.Fatal(err)
	}
	// Shared credentials are absent: the refusal names the next step.
	credentials := trustedRecoveryRequest(s, http.MethodPost, `{"action":"prepare"}`)
	if credentials.Code != http.StatusConflict || !strings.Contains(credentials.Body.String(), "暂时无法读取原客机凭据") {
		t.Fatalf("missing credentials refusal: %d %s", credentials.Code, credentials.Body.String())
	}
	s.account = newSharedMacAccount(t)
	// Without a guest disk the entry is not preparable yet.
	undisked := trustedRecoveryRequest(s, http.MethodPost, `{"action":"prepare"}`)
	if undisked.Code != http.StatusConflict || !strings.Contains(undisked.Body.String(), "磁盘或机器标识尚未就绪") {
		t.Fatalf("missing disk refusal: %d %s", undisked.Code, undisked.Body.String())
	}
	machine := filepath.Join(s.profile, "state", "lima", "mac")
	if err := os.MkdirAll(machine, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(machine, "disk"), []byte("published"), 0600); err != nil {
		t.Fatal(err)
	}
	// A disk without its machine identifier fails the identity check itself.
	unidentified := trustedRecoveryRequest(s, http.MethodPost, `{"action":"prepare"}`)
	if unidentified.Code != http.StatusConflict || !strings.Contains(unidentified.Body.String(), "暂时无法核对原客机磁盘身份") {
		t.Fatalf("missing machine identity refusal: %d %s", unidentified.Code, unidentified.Body.String())
	}
	if err := os.WriteFile(filepath.Join(machine, "vz-identifier"), []byte("failed-install-machine"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.profile, "macos-install-recovery.json"), []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	corrupt := trustedRecoveryRequest(s, http.MethodGet, "")
	if corrupt.Code != http.StatusConflict || !strings.Contains(corrupt.Body.String(), "身份校验未通过") {
		t.Fatalf("corrupt recovery record refusal: %d %s", corrupt.Code, corrupt.Body.String())
	}
	events := diagnosticEvents(t, s.profile)
	for _, want := range []string{
		"读取原客机凭据失败",
		"准备恢复入口被拒绝：原客机磁盘或机器标识尚未就绪",
		"核对原客机磁盘身份失败",
		"恢复入口身份校验未通过",
	} {
		if !strings.Contains(events, want) {
			t.Fatalf("diagnostics missing %q in %s", want, events)
		}
	}
	for _, refusal := range []*httptest.ResponseRecorder{credentials, undisked, unidentified, corrupt} {
		if strings.Contains(refusal.Body.String(), "shihao") || strings.Contains(refusal.Body.String(), "failed-install-machine") {
			t.Fatalf("refusal leaked guest identity: %s", refusal.Body.String())
		}
	}
	if strings.Contains(events, "shihao") || strings.Contains(events, "failed-install-machine") {
		t.Fatalf("diagnostics leaked guest identity: %s", events)
	}
}

func TestResumePendingMacOSInstallDistinguishesRecoveryRecordFailures(t *testing.T) {
	s := testServer(t)
	id := uuid.New()
	if err := guestos.SaveInstallation(s.profile, guestos.Installation{Version: 1, System: guestos.MacOS, ID: id}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, s.origin+"/api/start", nil)
	unprepared := httptest.NewRecorder()
	s.resumePendingMacOSInstall(unprepared, req, id)
	if unprepared.Code != http.StatusConflict || !strings.Contains(unprepared.Body.String(), "请先准备与原客机身份匹配的恢复入口") {
		t.Fatalf("missing recovery entry refusal: %d %s", unprepared.Code, unprepared.Body.String())
	}
	if err := os.WriteFile(filepath.Join(s.profile, "macos-install-recovery.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	corrupt := httptest.NewRecorder()
	s.resumePendingMacOSInstall(corrupt, req, id)
	if corrupt.Code != http.StatusConflict || !strings.Contains(corrupt.Body.String(), "暂时无法确认安装恢复入口") {
		t.Fatalf("corrupt recovery record refusal: %d %s", corrupt.Code, corrupt.Body.String())
	}
	events := diagnosticEvents(t, s.profile)
	if !strings.Contains(events, "继续原安装被拒绝，恢复入口身份校验未通过") {
		t.Fatalf("diagnostics missing resume refusal cause: %s", events)
	}
}

func TestPendingMacOSWithoutDiskReturnsToInstallerAdmission(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(fmt.Sprint(started), func(t *testing.T) {
			s := testServer(t)
			install := guestos.Installation{Version: 1, System: guestos.MacOS, ID: uuid.New()}
			if err := guestos.SaveInstallation(s.profile, install); err != nil {
				t.Fatal(err)
			}
			if started {
				if err := os.MkdirAll(filepath.Join(s.profile, "state", "lima", "mac"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			r := httptest.NewRequest(http.MethodPost, s.origin+"/api/start", strings.NewReader(`{}`))
			r.Header.Set("Content-Type", "application/json")
			r.AddCookie(&http.Cookie{Name: s.cookieName() + "-desktop", Value: trustedViewProof(s.token)})
			w := httptest.NewRecorder()
			s.start(w, r)
			want := "请先注册或确认 Cage 账密"
			if started {
				want = "请先准备与原客机身份匹配的恢复入口"
			}
			if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), want) || s.jobs.snapshot().Running {
				t.Fatalf("wrong pending installation admission: %d %s", w.Code, w.Body.String())
			}
			pending, err := guestos.LoadInstallation(s.profile)
			if err != nil || pending == nil || *pending != install {
				t.Fatal("original installation identity changed", err)
			}
		})
	}
}

// Install failures must send users to the initialization page's operation
// details and the diagnostics log. Only the macOS component recovery prompt
// involves GUI authorization; the generic message must not claim it.
func TestGuestInstallFailureMessagePointsToDetailsAndDiagnostics(t *testing.T) {
	cause := errors.New("installer exited 3")
	mac := guestInstallFailureMessage(guestos.MacOS, cause)
	if !errors.Is(mac, cause) {
		t.Fatal("install failure lost its cause")
	}
	for _, want := range []string{"操作详情", "日志与诊断", "继续安装"} {
		if !strings.Contains(mac.Error(), want) {
			t.Fatalf("macOS install failure missing %q: %s", want, mac.Error())
		}
	}
	if !strings.Contains(mac.Error(), "才需要打开恢复画面完成授权") {
		t.Fatalf("macOS install failure must scope authorization to the recovery prompt: %s", mac.Error())
	}
	other := guestInstallFailureMessage(guestos.Windows11, cause).Error()
	for _, want := range []string{"操作详情", "日志与诊断"} {
		if !strings.Contains(other, want) {
			t.Fatalf("install failure missing %q: %s", want, other)
		}
	}
	if strings.Contains(other, "授权") || strings.Contains(other, "恢复画面") {
		t.Fatalf("non-macOS install failure claimed GUI authorization: %s", other)
	}
	if strings.Contains(other, "请在恢复界面继续安装") {
		t.Fatalf("install failure told every case to use the recovery screen: %s", other)
	}
}

func trustedRecoveryAPIRequest(s *Server, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, s.origin+path, strings.NewReader(body))
	req.Header.Set("Origin", s.origin)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: s.cookieName(), Value: s.session})
	req.AddCookie(&http.Cookie{Name: s.cookieName() + "-desktop", Value: trustedViewProof(s.token)})
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	return w
}

// The recovery mode must let an unstarted installation be re-selected from the
// recovery page, while every other destructive guest-system call stays gated.
func TestMacOSInstallRecoveryRouteAllowsOnlyUnstartedReselect(t *testing.T) {
	if !macOSInstallRecoveryRoute(http.MethodDelete, "/api/environment/guest-systems") {
		t.Fatal("recovery mode must allow re-selecting an unstarted guest system")
	}
	for _, give := range []struct{ method, path string }{
		{http.MethodDelete, "/api/environment/guest-systems/activate"},
		{http.MethodDelete, "/api/account"},
		{http.MethodPut, "/api/environment/guest-systems"},
		{http.MethodPost, "/api/environment"},
	} {
		if macOSInstallRecoveryRoute(give.method, give.path) {
			t.Fatalf("recovery mode must refuse %s %s", give.method, give.path)
		}
	}
}

// Re-selection from the recovery page only works while no partial VM exists:
// once the installation has started, the same call must be refused and keep
// the pending record and disk for the recovery flow.
func TestRecoveryReselectRefusesStartedInstallations(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("the pending macOS reselect view requires its darwin/arm64 host")
	}
	s := testServer(t)
	started := uuid.New()
	if err := guestos.SaveInstallation(s.profile, guestos.Installation{Version: 1, System: guestos.MacOS, ID: started}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s.profile, "state", "lima", "mac"), 0700); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"` + started.String() + `"}`
	refused := trustedRecoveryAPIRequest(s, http.MethodDelete, "/api/environment/guest-systems", body)
	if refused.Code != http.StatusConflict || !strings.Contains(refused.Body.String(), "请继续原安装") {
		t.Fatalf("started installation reselect refusal: %d %s", refused.Code, refused.Body.String())
	}
	pending, err := guestos.LoadInstallation(s.profile)
	if err != nil || pending == nil || pending.ID != started {
		t.Fatalf("started installation must keep its pending record: %v %v", pending, err)
	}
}

// An installation that never started has no disk to protect: the recovery page
// can clear the choice, and the pending record disappears with it.
func TestRecoveryReselectClearsUnstartedInstallations(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("the pending macOS reselect view requires its darwin/arm64 host")
	}
	s := testServer(t)
	unstarted := uuid.New()
	if err := guestos.SaveInstallation(s.profile, guestos.Installation{Version: 1, System: guestos.MacOS, ID: unstarted}); err != nil {
		t.Fatal(err)
	}
	body := `{"id":"` + unstarted.String() + `"}`
	cleared := trustedRecoveryAPIRequest(s, http.MethodDelete, "/api/environment/guest-systems", body)
	if cleared.Code != http.StatusOK || !strings.Contains(cleared.Body.String(), `"cleared":true`) {
		t.Fatalf("unstarted installation reselect: %d %s", cleared.Code, cleared.Body.String())
	}
	pending, err := guestos.LoadInstallation(s.profile)
	if err != nil || pending != nil {
		t.Fatalf("unstarted installation must be cleared: %v %v", pending, err)
	}
}
