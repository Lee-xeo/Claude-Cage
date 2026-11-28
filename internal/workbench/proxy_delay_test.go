package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sylvinus/agent-vm/internal/managedproxy"
)

func TestProxyDelayBatchBoundsConcurrencyAndKeepsSelection(t *testing.T) {
	s := testServer(t)
	p, _ := managedproxy.Load(s.profile)
	if err := p.Import("batch", "", []byte(`proxies:
  - {name: slow, type: http, server: 203.0.113.10, port: 1081}
  - {name: fast, type: http, server: 203.0.113.10, port: 1082}
  - {name: broken, type: http, server: 203.0.113.10, port: 1083}
  - {name: mid, type: http, server: 203.0.113.10, port: 1084}
  - {name: other, type: http, server: 203.0.113.10, port: 1085}
`), "batch", 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.Save(s.profile); err != nil {
		t.Fatal(err)
	}
	selected := p.Selected
	ids := map[string]string{}
	var order []string
	for _, n := range p.Sources[1].Nodes {
		ids[n.ID] = n.Name
		order = append(order, n.ID)
	}
	var active, peak atomic.Int32
	var mu sync.Mutex
	measured := map[string]int{}
	old := measureProxyDelay
	t.Cleanup(func() { measureProxyDelay = old })
	measureProxyDelay = func(_ context.Context, _ string, id string) (int, string, error) {
		now := active.Add(1)
		defer active.Add(-1)
		for {
			if was := peak.Load(); now <= was || peak.CompareAndSwap(was, now) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		measured[id]++
		mu.Unlock()
		switch ids[id] {
		case "broken":
			return 0, "", errors.New("测速超时：节点已载入核心")
		case "fast":
			return 40, proxyDelayPrimaryURL, nil
		case "mid":
			return 90, proxyDelayFallbackURL, nil
		default:
			return 300, proxyDelayPrimaryURL, nil
		}
	}
	body, _ := json.Marshal(proxyInput{NodeIDs: order})
	w := request(s, "POST", "/api/proxy/delay", s.origin, string(body), true)
	if w.Code != http.StatusOK {
		t.Fatal(w.Code, w.Body.String())
	}
	var reply struct {
		Results []proxyDelayResult
		Best    string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Results) != len(order) || ids[reply.Best] != "fast" {
		t.Fatalf("unexpected batch reply: %s", w.Body.String())
	}
	for i, result := range reply.Results {
		if result.NodeID != order[i] {
			t.Fatal("results lost request order")
		}
		if ids[result.NodeID] == "broken" && (result.Error == "" || result.Delay != 0) {
			t.Fatalf("failed node lost its reason: %+v", result)
		}
		if want := map[bool]string{true: proxyDelayFallbackURL}[ids[result.NodeID] == "mid"]; result.Target != want {
			t.Fatalf("result target %q, want %q: %+v", result.Target, want, result)
		}
	}
	if peak.Load() > proxyDelayWorkers || peak.Load() < 2 {
		t.Fatalf("peak concurrency %d, want 2..%d", peak.Load(), proxyDelayWorkers)
	}
	for _, id := range order {
		if measured[id] != 1 {
			t.Fatalf("node measured %d times", measured[id])
		}
	}
	after, _ := managedproxy.Load(s.profile)
	if after.Selected != selected || len(after.Chain) != 0 {
		t.Fatal("batch measurement changed the selected node")
	}
	for _, bad := range []proxyInput{
		{NodeIDs: []string{order[0], order[0]}},
		{NodeIDs: []string{"missing"}},
		{NodeIDs: order, NodeID: order[0]},
		{NodeIDs: make([]string, proxyDelayMaxIDs+1)},
	} {
		body, _ := json.Marshal(bad)
		if w := request(s, "POST", "/api/proxy/delay", s.origin, string(body), true); w.Code == http.StatusOK {
			t.Fatalf("invalid batch accepted: %+v", bad)
		}
	}
	single, _ := json.Marshal(proxyInput{NodeID: order[1]})
	if w := request(s, "POST", "/api/proxy/delay", s.origin, string(single), true); w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"delay":40}` {
		t.Fatalf("single-node reply changed: %d %s", w.Code, w.Body.String())
	}
}

// fakeDelayCore serves the core's delay API on the profile's control socket.
// reply maps each requested target to an HTTP status (200 replies delay 171);
// the targets each request asked for are recorded in order.
func fakeDelayCore(t *testing.T, profile string, reply map[string]int) func() []string {
	t.Helper()
	socket := managedproxy.Socket(profile)
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(filepath.Dir(socket)) })
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal("listen on test control socket:", err)
	}
	var mu sync.Mutex
	var asked []string
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/proxies/") || !strings.HasSuffix(r.URL.Path, "/delay") {
			// Readiness checks find no usable core; only delay calls are faked.
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("timeout") != "5000" {
			t.Errorf("delay request lost its timeout: %s", r.URL)
		}
		target := r.URL.Query().Get("url")
		mu.Lock()
		asked = append(asked, target)
		mu.Unlock()
		status := reply[target]
		if status == http.StatusOK {
			_, _ = io.WriteString(w, `{"delay":171}`)
			return
		}
		w.WriteHeader(status)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), asked...)
	}
}

func TestProxyDelayFallsBackOnlyWhenTargetTimesOut(t *testing.T) {
	primary, fallback := proxyDelayPrimaryURL, proxyDelayFallbackURL
	for _, tc := range []struct {
		name       string
		reply      map[string]int
		wantAsked  []string
		wantDelay  int
		wantTarget string
		wantErr    string
	}{
		{"primary answers", map[string]int{primary: 200, fallback: 200}, []string{primary}, 171, primary, ""},
		{"primary timeout, fallback answers", map[string]int{primary: 504, fallback: 200}, []string{primary, fallback}, 171, fallback, ""},
		{"both time out", map[string]int{primary: 504, fallback: 504}, []string{primary, fallback}, 0, "", "测速超时"},
		{"fallback upstream error", map[string]int{primary: 504, fallback: 503}, []string{primary, fallback}, 0, "", "连接节点或上游代理时出错"},
		{"upstream error is not retried", map[string]int{primary: 503, fallback: 200}, []string{primary}, 0, "", "连接节点或上游代理时出错"},
		{"unloaded node is not retried", map[string]int{primary: 404, fallback: 200}, []string{primary}, 0, "", "节点未载入核心"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Keep the socket path within the Windows AF_UNIX length limit.
			profile, err := os.MkdirTemp("", "cage-delay-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Remove(profile) })
			requested := fakeDelayCore(t, profile, tc.reply)
			delay, target, err := measureProxyDelay(context.Background(), profile, "node-id")
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			asked := requested()
			if delay != tc.wantDelay || target != tc.wantTarget || strings.Join(asked, " ") != strings.Join(tc.wantAsked, " ") {
				t.Fatalf("delay=%d target=%q asked=%v; want %d %q %v", delay, target, asked, tc.wantDelay, tc.wantTarget, tc.wantAsked)
			}
		})
	}
}

func TestProxyDelayReplyNamesFallbackTarget(t *testing.T) {
	s := testServer(t)
	p, _ := managedproxy.Load(s.profile)
	fakeDelayCore(t, s.profile, map[string]int{proxyDelayPrimaryURL: 504, proxyDelayFallbackURL: 200})
	single, _ := json.Marshal(proxyInput{NodeID: p.Selected})
	w := request(s, "POST", "/api/proxy/delay", s.origin, string(single), true)
	if want := `{"delay":171,"target":"` + proxyDelayFallbackURL + `"}`; w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != want {
		t.Fatalf("single reply %d %s, want %s", w.Code, w.Body.String(), want)
	}
}
