package workbench

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/sylvinus/agent-vm/internal/guestos"
)

//go:embed cache_maintenance.py
var cacheMaintenanceScript string

type cacheItem struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Location    string `json:"location"`
	Bytes       int64  `json:"bytes"`
	Files       int    `json:"files"`
	State       string `json:"state"`
	Reason      string `json:"reason"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

type cacheInventory struct {
	Version   int             `json:"version"`
	Scope     string          `json:"scope"`
	Platform  string          `json:"platform"`
	Items     []cacheItem     `json:"items"`
	Protected json.RawMessage `json:"protected"`
	Batches   []struct {
		ID        string `json:"id"`
		CreatedAt int64  `json:"created_at"`
		Count     int    `json:"count"`
	} `json:"batches"`
	Notice string `json:"notice"`
	PlanID string `json:"plan_id"`
}

// A preview is a short-lived, one-use capability owned by this controller.
// Clients can select IDs from it, but cannot supply paths or file fingerprints.
type cacheMaintenance struct {
	mu      sync.Mutex
	plan    cacheInventory
	created time.Time
	run     string
	execute func(context.Context, map[string]any) ([]byte, error)
}

func (s *Server) registerCacheMaintenance(mux *discoverableMux) {
	care := &cacheMaintenance{execute: s.runCacheMaintenance}
	mux.HandleFunc("POST /api/environment/cache/inventory", func(w http.ResponseWriter, r *http.Request) { s.cacheMaintenanceAction(care, "inspect", w, r) })
	mux.HandleFunc("POST /api/environment/cache/clean", func(w http.ResponseWriter, r *http.Request) { s.cacheMaintenanceAction(care, "clean", w, r) })
	mux.HandleFunc("POST /api/environment/cache/restore", func(w http.ResponseWriter, r *http.Request) { s.cacheMaintenanceAction(care, "restore", w, r) })
}

func (s *Server) cacheMaintenanceRun() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.instanceID
	if s.safety.Gate != nil {
		run += ":" + s.safety.Gate.RunID
	}
	return run, s.ready && !s.closing
}

func (care *cacheMaintenance) selection(id string, ids []string, run string, now time.Time) (map[string]any, error) {
	if id == "" || id != care.plan.PlanID || run != care.run || now.Sub(care.created) > 10*time.Minute || len(ids) == 0 || len(ids) > len(care.plan.Items) {
		return nil, errors.New("清理预览已失效，请重新盘点并选择缓存")
	}
	items := make([]map[string]string, 0, len(ids))
	seen := make(map[string]bool)
	for _, id := range ids {
		var selected *cacheItem
		for i := range care.plan.Items {
			if care.plan.Items[i].ID == id {
				selected = &care.plan.Items[i]
				break
			}
		}
		if selected == nil || selected.State != "ready" || selected.Fingerprint == "" || seen[id] {
			return nil, errors.New("缓存选择不属于当前可清理清单，请重新盘点")
		}
		seen[id] = true
		items = append(items, map[string]string{"id": id, "fingerprint": selected.Fingerprint})
	}
	return map[string]any{"action": "clean", "scope": care.plan.Scope, "items": items}, nil
}

func (s *Server) cacheMaintenanceAction(care *cacheMaintenance, action string, w http.ResponseWriter, r *http.Request) {
	var input struct {
		PlanID string   `json:"plan_id"`
		Items  []string `json:"items"`
		Batch  string   `json:"batch"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		http.Error(w, "缓存操作请求无效", http.StatusBadRequest)
		return
	}
	if !s.op.TryLock() {
		http.Error(w, "环境正在执行其他操作，请稍后重试", http.StatusConflict)
		return
	}
	defer s.op.Unlock()
	if !care.mu.TryLock() {
		http.Error(w, "缓存维护正在进行，请稍后重试", http.StatusConflict)
		return
	}
	defer care.mu.Unlock()
	run, ready := s.cacheMaintenanceRun()
	if !ready {
		http.Error(w, "请先启动当前环境；缓存维护只处理该环境内的数据", http.StatusConflict)
		return
	}
	request := map[string]any{"action": action}
	if action == "inspect" {
		care.plan.PlanID = ""
	}
	if action == "clean" {
		var err error
		request, err = care.selection(input.PlanID, input.Items, run, time.Now())
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		// A cancelled or uncertain mutation must never be replayed from this plan.
		care.plan.PlanID = ""
	} else if action == "restore" {
		if len(input.Batch) != 32 || strings.Trim(input.Batch, "0123456789abcdef") != "" {
			http.Error(w, "恢复批次无效", http.StatusBadRequest)
			return
		}
		request["batch"] = input.Batch
		care.plan.PlanID = ""
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	data, err := care.execute(ctx, request)
	if err != nil {
		requestError(w, errors.New("缓存操作未返回完整结果，请重新盘点；原数据与恢复副本均保留"))
		return
	}
	var status struct {
		Version int    `json:"version"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(data, &status) != nil || status.Version != 1 || status.Error != "" {
		http.Error(w, "缓存状态已变化或无法完整核对，请重新盘点；已有数据与恢复副本均保留", http.StatusConflict)
		return
	}
	if action == "inspect" {
		var inventory cacheInventory
		if json.Unmarshal(data, &inventory) != nil || inventory.Scope == "" || len(inventory.Items) > 110 {
			requestError(w, errors.New("缓存盘点返回无效结果"))
			return
		}
		inventory.PlanID = randomToken()
		care.plan, care.run, care.created = inventory, run, time.Now()
		jsonReply(w, inventory)
		return
	}
	jsonReply(w, json.RawMessage(data))
}

func (s *Server) runCacheMaintenance(ctx context.Context, request map[string]any) ([]byte, error) {
	if s.windowsGuest == nil {
		input, err := json.Marshal(request)
		if err != nil {
			return nil, err
		}
		python := []string{"sudo", "-n", "/usr/bin/python3", "-I", "-c", cacheMaintenanceScript}
		if s.macosGuest != nil {
			python = []string{macCLIInstall + "/bin/python3", "-I", "-c", cacheMaintenanceScript}
		}
		return s.guest(ctx, "exec "+shellescape.QuoteCommand(python)+" <<'MAINTENANCE_INPUT'\n"+string(input)+"\nMAINTENANCE_INPUT\n")
	}
	request["profile"] = windowsCLIProfileID(s.profile)
	data, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	cmd, err := guestos.WindowsRelayCommand(ctx, s.profile, "cache-maintenance")
	if err != nil {
		return nil, err
	}
	cmd.Stdin = bytes.NewReader(append(data, '\n'))
	cmd.Stderr = io.Discard
	cmd.WaitDelay = 2 * time.Second
	var output bytes.Buffer
	cmd.Stdout = &boundedWindowsStatusWriter{buffer: &output, remaining: 64 << 10}
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
