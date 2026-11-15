package workbench

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func cacheTestInventory() cacheInventory {
	return cacheInventory{Version: 1, Scope: "fixture-scope", Platform: "linux", Items: []cacheItem{
		{ID: "cli-default", Title: "CLI 缓存", State: "ready", Fingerprint: "fixture-metadata", Bytes: 10},
		{ID: "browser-0", Title: "浏览器缓存", State: "blocked"},
	}, Protected: json.RawMessage(`[]`)}
}

func TestCachePlanRejectsInvalidExpiredAndChangedRunSelections(t *testing.T) {
	plan := cacheTestInventory()
	plan.PlanID = "preview"
	now := time.Now()
	care := &cacheMaintenance{plan: plan, created: now, run: "running-instance"}
	for _, test := range []struct {
		name, id, run string
		ids           []string
		at            time.Time
	}{
		{"missing preview", "", "running-instance", []string{"cli-default"}, now},
		{"unknown item", "preview", "running-instance", []string{"../../account"}, now},
		{"blocked", "preview", "running-instance", []string{"browser-0"}, now},
		{"duplicate", "preview", "running-instance", []string{"cli-default", "cli-default"}, now},
		{"empty", "preview", "running-instance", nil, now},
		{"expired", "preview", "running-instance", []string{"cli-default"}, now.Add(11 * time.Minute)},
		{"restarted", "preview", "different-instance", []string{"cli-default"}, now},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := care.selection(test.id, test.ids, test.run, test.at); err == nil {
				t.Fatal("invalid preview authorized a mutation")
			}
		})
	}
	request, err := care.selection("preview", []string{"cli-default"}, "running-instance", now)
	if err != nil || request["scope"] != "fixture-scope" {
		t.Fatalf("valid selection: %v %v", request, err)
	}
	if request["items"].([]map[string]string)[0]["fingerprint"] != "fixture-metadata" {
		t.Fatal("selection did not use controller-owned metadata")
	}
}

func TestCacheHTTPManualPreviewAndOneUseMutation(t *testing.T) {
	s := &Server{ready: true, instanceID: "fixture-run"}
	care := &cacheMaintenance{}
	calls := []string{}
	care.execute = func(_ context.Context, request map[string]any) ([]byte, error) {
		calls = append(calls, request["action"].(string))
		if request["action"] == "inspect" {
			return json.Marshal(cacheTestInventory())
		}
		return []byte(`{"version":1,"status":"complete","outcomes":[]}`), nil
	}
	invoke := func(action, body string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		s.cacheMaintenanceAction(care, action, response, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		return response
	}
	if len(calls) != 0 {
		t.Fatal("maintenance ran without user action")
	}
	for _, body := range []string{`{"path":"/home/user"}`, `{} {}`, `{"items":"all"}`} {
		if code := invoke("inspect", body).Code; code != 400 {
			t.Fatalf("invalid input status %d", code)
		}
	}
	s.ready = false
	if invoke("inspect", `{}`).Code != 409 || len(calls) != 0 {
		t.Fatal("offline inventory executed")
	}
	s.ready = true
	response := invoke("inspect", `{}`)
	if response.Code != 200 || care.plan.PlanID == "" {
		t.Fatalf("preview failed: %s", response.Body)
	}
	body, _ := json.Marshal(map[string]any{"plan_id": care.plan.PlanID, "items": []string{"cli-default"}})
	if invoke("clean", string(body)).Code != 200 {
		t.Fatal("valid clean failed")
	}
	if invoke("clean", string(body)).Code != 409 {
		t.Fatal("consumed preview was reused")
	}
	if invoke("restore", `{"batch":"../../project"}`).Code != 400 {
		t.Fatal("unsafe restore accepted")
	}
	if strings.Join(calls, ",") != "inspect,clean" {
		t.Fatalf("unexpected calls: %v", calls)
	}
}

func TestCacheHTTPConsumesPreviewOnUncertainResultAndSerializesEnvironment(t *testing.T) {
	s := &Server{ready: true, instanceID: "fixture-run"}
	care := &cacheMaintenance{plan: cacheTestInventory(), run: s.instanceID, created: time.Now()}
	care.plan.PlanID = "preview"
	care.execute = func(context.Context, map[string]any) ([]byte, error) { return nil, context.DeadlineExceeded }
	response := httptest.NewRecorder()
	s.op.Lock()
	s.cacheMaintenanceAction(care, "inspect", response, httptest.NewRequest("POST", "/", strings.NewReader(`{}`)))
	s.op.Unlock()
	if response.Code != 409 {
		t.Fatal("inventory overlapped another environment operation")
	}
	response = httptest.NewRecorder()
	s.cacheMaintenanceAction(care, "clean", response, httptest.NewRequest("POST", "/", strings.NewReader(`{"plan_id":"preview","items":["cli-default"]}`)))
	if response.Code == 200 || care.plan.PlanID != "" {
		t.Fatal("uncertain mutation retained an executable preview")
	}
}
