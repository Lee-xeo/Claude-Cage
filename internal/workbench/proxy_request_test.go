package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sylvinus/agent-vm/internal/managedproxy"
)

func queuedJob(s *Server) Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jobs.snapshot()
}

func TestProxyMutationRejectsInvalidInputBeforeQueueingJob(t *testing.T) {
	s := testServer(t)
	for _, c := range []struct{ path, body, want string }{
		{"/api/proxy/import", `{"id":"local-config-id","name":"FlClash"}`, "local-configs"},
		{"/api/proxy/import", `{"name":"FlClash"}`, "缺少 content 或 url"},
		{"/api/proxy/import", `{"content":"proxies: []"}`, "请填写 1–100 字节的来源名称"},
		{"/api/proxy/import", `{"name":"sub","url":"http://example.com/sub"}`, "HTTPS"},
		{"/api/proxy/import", `{"name":"sub","content":"x","interval_hours":200}`, "更新间隔"},
		{"/api/proxy/refresh", `{}`, "缺少 id"},
		{"/api/proxy/edit", `{"id":"s","name":" ","interval_hours":0}`, "名称或更新间隔无效"},
		{"/api/proxy/edit-node", `{"id":"s","server":"192.0.2.1","server_port":1080}`, "缺少 node_id"},
		{"/api/proxy/edit-node", `{"id":"s","node_id":"n","server":"bad host!","server_port":1080}`, "服务器地址无效"},
		{"/api/proxy/edit-node", `{"id":"s","node_id":"n","server":"192.0.2.1","server_port":0}`, "节点端口"},
		{"/api/proxy/select", `{"action":"select"}`, "缺少 node_id"},
		{"/api/proxy/select", `{"action":"route","node_id":"n"}`, "字段 action 与请求路径不一致"},
		{"/api/proxy/select", `{"node_ids":["a","b"]}`, "proxy.route"},
		{"/api/proxy/select", `{"node_id":"n","extra":1}`, `不支持字段 "extra"`},
		{"/api/proxy/select", `{"node_id":1}`, `字段 "node_id" 应为字符串`},
		{"/api/proxy/select", `{"node_id":"n"} {}`, "只能包含一个 JSON 值"},
		{"/api/proxy/select", `{"node_id":`, "不是完整的 JSON"},
		{"/api/proxy/route", `{"node_id":"n"}`, "node_ids"},
		{"/api/proxy/route", `{"node_ids":[]}`, "请至少选择一个最终出口"},
		{"/api/proxy/restart", `{"port":80}`, "代理端口应为 1024–65535"},
	} {
		w := request(s, "POST", c.path, s.origin, c.body, true)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%s %s: %d %q, want 400 containing %q", c.path, c.body, w.Code, w.Body.String(), c.want)
		}
	}
	if job := queuedJob(s); job.ID != "" {
		t.Fatalf("invalid input queued job %q", job.Kind)
	}
}

func TestProxyMutationFailsFastWithoutCore(t *testing.T) {
	s := testServer(t)
	old := proxyCorePath
	t.Cleanup(func() { proxyCorePath = old })
	proxyCorePath = managedproxy.CorePath
	if _, err := proxyCorePath(); err == nil {
		t.Skip("a proxy core is installed beside the test binary")
	}
	p, _ := managedproxy.Load(s.profile)
	for _, c := range []struct{ path, body string }{
		{"/api/proxy/import", `{"name":"FlClash","content":"proxies: [{name: a, type: http, server: 192.0.2.1, port: 1080}]"}`},
		{"/api/proxy/select", `{"action":"select","node_id":"` + p.Selected + `"}`},
		{"/api/proxy/restart", `{}`},
	} {
		w := request(s, "POST", c.path, s.origin, c.body, true)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "随附代理核心缺失") || !strings.Contains(w.Body.String(), "proxy-core") {
			t.Errorf("%s: %d %q", c.path, w.Code, w.Body.String())
		}
	}
	if job := queuedJob(s); job.ID != "" {
		t.Fatalf("missing core queued job %q", job.Kind)
	}
	var view struct {
		CoreAvailable bool   `json:"core_available"`
		CoreError     string `json:"core_error"`
	}
	w := request(s, "GET", "/api/proxy", s.origin, "", true)
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || view.CoreAvailable || !strings.Contains(view.CoreError, "proxy-core") {
		t.Fatalf("status does not explain the missing core: %v %s", err, w.Body.String())
	}
}

func TestProxySelectAcceptsActionMatchingPath(t *testing.T) {
	s := testServer(t)
	old := validateProxyCore
	t.Cleanup(func() { validateProxyCore = old })
	var validated atomic.Bool
	validateProxyCore = func(context.Context, string, managedproxy.Store) error {
		validated.Store(true)
		return errors.New("test validation stopped")
	}
	p, _ := managedproxy.Load(s.profile)
	if w := request(s, "POST", "/api/proxy/select", s.origin, `{"action":"select","node_id":"`+p.Selected+`"}`, true); w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		job := queuedJob(s)
		if !job.Running {
			if job.Kind != "proxy" || job.Error == "" || !validated.Load() {
				t.Fatalf("selection job did not run: %+v", job)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("selection job did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
