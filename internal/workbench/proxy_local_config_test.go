package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/managedproxy"
	"github.com/sylvinus/agent-vm/internal/proxyimport"
)

func configImportRequest(s *Server, method, path, body string, trusted bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, s.origin+path, strings.NewReader(body))
	r.Header.Set("Origin", s.origin)
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(&http.Cookie{Name: s.cookieName(), Value: s.session})
	if trusted {
		r.AddCookie(&http.Cookie{Name: s.cookieName() + "-desktop", Value: trustedViewProof(s.token)})
	}
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	return w
}

func TestImportLocalConfigTrustStalenessValidationAndIdempotency(t *testing.T) {
	s := testServer(t)
	cfg, _ := loadConfig(s.configPath)
	cfg.ProxyRevision = ""
	if err := writePrivate(s.configPath, cfg); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	file := filepath.Join(root, "config.yaml")
	data := []byte("proxies:\n  - {name: Independent, type: ss, server: independent.example, port: 443, cipher: aes-128-gcm, password: private-node-secret}\n")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	oldDiscover, oldValidate := discoverProxyConfigs, validateProxyCore
	t.Cleanup(func() { discoverProxyConfigs, validateProxyCore = oldDiscover, oldValidate })
	discoveryCalls, validationCalls := 0, 0
	discoverProxyConfigs = func(key []byte) ([]proxyimport.Config, error) {
		discoveryCalls++
		return proxyimport.Scan([]proxyimport.Location{{Client: "Clash", Directory: root}}, key), nil
	}
	failValidation := false
	validateProxyCore = func(_ context.Context, profile string, p managedproxy.Store) error {
		validationCalls++
		if failValidation {
			return errors.New("节点校验失败")
		}
		_, err := p.Runtime(profile)
		return err
	}
	for _, endpoint := range []struct{ method, path, body string }{{"GET", "/api/proxy/local-configs", ""}, {"POST", "/api/proxy/import-local", `{"id":"anything"}`}} {
		if w := configImportRequest(s, endpoint.method, endpoint.path, endpoint.body, false); w.Code != 403 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if discoveryCalls != 0 {
		t.Fatal("untrusted request read local configurations")
	}
	w := configImportRequest(s, "GET", "/api/proxy/local-configs", "", true)
	if w.Code != 200 || strings.Contains(w.Body.String(), "private-node-secret") || strings.Contains(w.Body.String(), root) {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		Configs []proxyimport.Config `json:"configs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || len(result.Configs) != 1 {
		t.Fatal(err, w.Body.String())
	}
	body := `{"id":"` + result.Configs[0].ID + `","url":"https://unwanted.example","content":"ignored","name":"ignored"}`
	before, _ := managedproxy.Load(s.profile)
	// The content changed after the preview; never import a different snapshot.
	if err := os.WriteFile(file, append(data, []byte("# comment\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	// Normalization ignores comments; change a node field to invalidate the handle.
	if err := os.WriteFile(file, []byte(strings.ReplaceAll(string(data), "private-node-secret", "changed")), 0600); err != nil {
		t.Fatal(err)
	}
	configImportRequest(s, "POST", "/api/proxy/import-local", body, true)
	if job := awaitJobIdle(t, &s.jobs); !strings.Contains(job.Error, "重新检测") {
		t.Fatal(job)
	}
	after, _ := managedproxy.Load(s.profile)
	if !reflect.DeepEqual(before, after) || validationCalls != 0 {
		t.Fatal("stale import modified store")
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	failValidation = true
	configImportRequest(s, "POST", "/api/proxy/import-local", body, true)
	if job := awaitJobIdle(t, &s.jobs); job.Error == "" {
		t.Fatal("failed core validation accepted")
	}
	after, _ = managedproxy.Load(s.profile)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed import modified store")
	}
	failValidation = false
	for i := 0; i < 2; i++ {
		if w := configImportRequest(s, "POST", "/api/proxy/import-local", body, true); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if job := awaitJobIdle(t, &s.jobs); job.Error != "" {
			t.Fatal(job)
		}
	}
	after, _ = managedproxy.Load(s.profile)
	if len(after.Sources) != len(before.Sources)+1 || after.Selected != before.Selected {
		t.Fatal("duplicate import or unexpected selection")
	}
	source := after.Sources[len(after.Sources)-1]
	if source.URL != "" || source.Name != "Clash · config.yaml" || source.Nodes[0].Config["server"] != "independent.example" || source.Nodes[0].Config["password"] != "private-node-secret" {
		t.Fatal("did not import independent node credentials")
	}
	original, _ := os.ReadFile(file)
	if string(original) != string(data) {
		t.Fatal("modified original client configuration")
	}
}
