package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sylvinus/agent-vm/internal/managedproxy"
	"go.yaml.in/yaml/v3"
)

func TestProxyRouteAPIIsAtomicAndSurvivesRefresh(t *testing.T) {
	s := testServer(t)
	p, _ := managedproxy.Load(s.profile)
	entry := p.Selected
	if err := p.Import("home", "https://example.com/home?token=private-token", []byte(`proxies: [{name: home, type: http, server: 127.0.0.1, port: 1080, password: private-password}]`), "home", 0, ""); err != nil {
		t.Fatal(err)
	}
	exit := p.Sources[1].Nodes[0].ID
	if err := p.Save(s.profile); err != nil {
		t.Fatal(err)
	}
	oldValidate, oldStart, oldFetch := validateProxyCore, startProxyCore, fetchProxySource
	t.Cleanup(func() { validateProxyCore, startProxyCore, fetchProxySource = oldValidate, oldStart, oldFetch })
	validateProxyCore = func(_ context.Context, profile string, p managedproxy.Store) error {
		_, err := p.Runtime(profile)
		return err
	}
	failStart := false
	startProxyCore = func(context.Context, string, managedproxy.Store) error {
		if failStart {
			return errors.New("test core startup failed")
		}
		return nil
	}
	body, _ := json.Marshal(proxyInput{NodeIDs: []string{entry, exit}})
	if w := request(s, "POST", "/api/proxy/route", s.origin, string(body), false); w.Code != http.StatusUnauthorized {
		t.Fatal("unauthenticated chain accepted", w.Code)
	}
	if w := request(s, "POST", "/api/proxy/route", s.origin, string(body), true); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		job := s.jobs.snapshot()
		s.mu.Unlock()
		if !job.Running {
			if job.Error != "" {
				t.Fatal(job.Error)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("chain apply did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	applied, _ := managedproxy.Load(s.profile)
	if !reflect.DeepEqual(applied.Chain, []string{entry, exit}) || applied.Selected != exit || applied.Revision == p.Revision {
		t.Fatal("chain not applied atomically", applied.Chain)
	}
	w := request(s, "GET", "/api/proxy", s.origin, "", true)
	var view struct {
		Route []string `json:"route"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || !reflect.DeepEqual(view.Route, applied.Chain) {
		t.Fatal("chain status missing", err, w.Body.String())
	}
	for _, secret := range []string{"private-token", "private-password", p.Auth} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("chain status leaked credentials")
		}
	}
	cfgBefore, _ := loadConfig(s.configPath)
	failStart = true
	if err := s.changeProxy("route", proxyInput{NodeIDs: []string{exit}}, nil); err == nil {
		t.Fatal("startup failure accepted")
	}
	after, _ := managedproxy.Load(s.profile)
	cfgAfter, _ := loadConfig(s.configPath)
	if !reflect.DeepEqual(after, applied) || !reflect.DeepEqual(cfgAfter, cfgBefore) {
		t.Fatal("failed chain application replaced last saved route")
	}
	failStart = false
	if err := s.changeProxy("route", proxyInput{NodeIDs: []string{entry, entry}}, nil); err == nil {
		t.Fatal("repeated node accepted")
	}
	if err := s.changeProxy("delete", proxyInput{ID: "source"}, nil); err == nil {
		t.Fatal("active transit source removed")
	}
	fetchProxySource = func(context.Context, *http.Client, string) ([]byte, string, error) {
		return []byte(`proxies: [{name: home, type: http, server: 127.0.0.1, port: 2080, password: updated}]`), "", nil
	}
	if err := s.changeProxy("refresh", proxyInput{ID: "home", HostDownload: true}, nil); err != nil {
		t.Fatal(err)
	}
	refreshed, _ := managedproxy.Load(s.profile)
	if !reflect.DeepEqual(refreshed.Chain, applied.Chain) || refreshed.Sources[1].Nodes[0].Config["password"] != "updated" {
		t.Fatal("subscription refresh lost the chain")
	}
	fetchProxySource = func(context.Context, *http.Client, string) ([]byte, string, error) {
		return []byte(`proxies: [{name: replacement, type: http, server: 127.0.0.1, port: 3080}]`), "", nil
	}
	if err := s.changeProxy("refresh", proxyInput{ID: "home", HostDownload: true}, nil); err == nil {
		t.Fatal("missing exit silently replaced")
	}
	retained, _ := managedproxy.Load(s.profile)
	if !reflect.DeepEqual(retained.Chain, applied.Chain) || retained.Sources[1].Nodes[0].ID != exit {
		t.Fatal("missing node refresh discarded the working chain")
	}
	if err := s.changeProxy("route", proxyInput{NodeIDs: []string{exit}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.changeProxy("delete", proxyInput{ID: "home"}, nil); err != nil {
		t.Fatal("single-node source could not be deleted", err)
	}
	deleted, _ := managedproxy.Load(s.profile)
	if deleted.Selected != "" || len(deleted.Chain) != 0 {
		t.Fatal("deleted single-node source retained an active route")
	}

}

// 回归 #275：链 [A,B] 后 select A 曾把链改写成 [A,A] 而报“代理链不能重复使用同一个节点”。
func TestProxySelectReplacesExistingChain(t *testing.T) {
	s := testServer(t)
	p, _ := managedproxy.Load(s.profile)
	a := p.Selected
	if err := p.Import("home", "", []byte(`proxies: [{name: home, type: http, server: 127.0.0.1, port: 1080}]`), "home", 0, ""); err != nil {
		t.Fatal(err)
	}
	b := p.Sources[1].Nodes[0].ID
	if err := p.Save(s.profile); err != nil {
		t.Fatal(err)
	}
	oldValidate, oldStart := validateProxyCore, startProxyCore
	t.Cleanup(func() { validateProxyCore, startProxyCore = oldValidate, oldStart })
	validateProxyCore = func(_ context.Context, profile string, p managedproxy.Store) error {
		_, err := p.Runtime(profile)
		return err
	}
	failStart := false
	startProxyCore = func(context.Context, string, managedproxy.Store) error {
		if failStart {
			return errors.New("test core startup failed")
		}
		return nil
	}
	if err := s.changeProxy("route", proxyInput{NodeIDs: []string{a, b}}, nil); err != nil {
		t.Fatal(err)
	}
	chained, _ := managedproxy.Load(s.profile)
	if !reflect.DeepEqual(chained.Chain, []string{a, b}) {
		t.Fatal("chain not configured", chained.Chain)
	}

	// 失败时状态保持原链。
	failStart = true
	if err := s.changeProxy("select", proxyInput{NodeID: a}, nil); err == nil {
		t.Fatal("startup failure accepted")
	}
	if after, _ := managedproxy.Load(s.profile); !reflect.DeepEqual(after, chained) {
		t.Fatal("failed select changed the saved route")
	}
	if err := s.changeProxy("select", proxyInput{NodeID: "missing"}, nil); err == nil {
		t.Fatal("unknown node accepted")
	}
	if after, _ := managedproxy.Load(s.profile); !reflect.DeepEqual(after, chained) {
		t.Fatal("rejected select changed the saved route")
	}
	failStart = false

	// 链成员（入口 A 与出口 B）都能被选为单节点出口。
	for _, id := range []string{a, b} {
		if err := s.changeProxy("route", proxyInput{NodeIDs: []string{a, b}}, nil); err != nil {
			t.Fatal(err)
		}
		if err := s.changeProxy("select", proxyInput{NodeID: id}, nil); err != nil {
			t.Fatalf("select %s: %v", id, err)
		}
		got, _ := managedproxy.Load(s.profile)
		route, err := got.Route()
		if err != nil || got.Selected != id || len(got.Chain) != 0 || !reflect.DeepEqual(route, []string{id}) {
			t.Fatal("select did not leave a single-node route", got.Chain, route, err)
		}
	}
	// 无链时的普通 select 与随后的 route 仍然可用。
	if err := s.changeProxy("select", proxyInput{NodeID: b}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.changeProxy("route", proxyInput{NodeIDs: []string{a}}, nil); err != nil {
		t.Fatal(err)
	}
}

// 回归 #345：单节点应用曾以单元素显式链覆盖导入的 dialer-proxy，所选出口失去中转，
// 切走后旧出口又恢复中转。
func TestSingleNodeRouteKeepsImportedDialer(t *testing.T) {
	s := testServer(t)
	p, _ := managedproxy.Load(s.profile)
	if err := p.Import("chain", "", []byte(`proxies:
  - {name: T, type: http, server: 127.0.0.1, port: 1080}
  - {name: A, type: socks5, server: 127.0.0.1, port: 1081, dialer-proxy: T}
  - {name: B, type: socks5, server: 127.0.0.1, port: 1082, dialer-proxy: T}
`), "chain", 0, ""); err != nil {
		t.Fatal(err)
	}
	nodes := p.Sources[1].Nodes
	transit, a, b := nodes[0].ID, nodes[1].ID, nodes[2].ID
	if err := p.Save(s.profile); err != nil {
		t.Fatal(err)
	}
	oldValidate, oldStart := validateProxyCore, startProxyCore
	t.Cleanup(func() { validateProxyCore, startProxyCore = oldValidate, oldStart })
	validateProxyCore = func(_ context.Context, profile string, p managedproxy.Store) error {
		_, err := p.Runtime(profile)
		return err
	}
	startProxyCore = func(context.Context, string, managedproxy.Store) error { return nil }
	for _, exit := range []string{a, b, a} {
		if err := s.changeProxy("route", proxyInput{NodeIDs: []string{exit}}, nil); err != nil {
			t.Fatal(err)
		}
		got, _ := managedproxy.Load(s.profile)
		route, err := got.Route()
		if err != nil || got.Selected != exit || len(got.Chain) != 0 || !reflect.DeepEqual(route, []string{transit, exit}) {
			t.Fatalf("single exit lost its imported hop: chain=%v route=%v err=%v", got.Chain, route, err)
		}
		runtime, err := got.Runtime(s.profile)
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			Proxies []map[string]any `yaml:"proxies"`
		}
		if err := yaml.Unmarshal(runtime, &config); err != nil {
			t.Fatal(err)
		}
		for _, proxy := range config.Proxies {
			if (proxy["name"] == a || proxy["name"] == b) && proxy["dialer-proxy"] != transit {
				t.Fatalf("node %v lost its imported dialer after selecting %s: %v", proxy["name"], exit, proxy)
			}
		}
	}
}
