package workbench

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/klauspost/compress/gzip"
)

//go:embed token_usage.py
var tokenUsageHelper string

type tokenUsageDay struct {
	Date     string `json:"date"`
	Model    string `json:"model"`
	Source   string `json:"source"`
	Client   string `json:"client,omitempty"`
	Input    int64  `json:"input_tokens"`
	Output   int64  `json:"output_tokens"`
	Read     int64  `json:"cache_read_input_tokens"`
	Write    int64  `json:"cache_creation_input_tokens"`
	Requests int64  `json:"requests"`
}

type tokenUsageSnapshot struct {
	Version   int             `json:"version"`
	UpdatedAt int64           `json:"updated_at"`
	Timezone  string          `json:"timezone"`
	Today     string          `json:"today"`
	Cutoff    float64         `json:"cutoff"`
	Pending   bool            `json:"pending"`
	Invalid   int64           `json:"invalid_records"`
	Days      []tokenUsageDay `json:"days"`
}

type tokenUsageState struct {
	Enabled  bool               `json:"enabled"`
	Cutoff   float64            `json:"cutoff"`
	Snapshot tokenUsageSnapshot `json:"snapshot"`
}

type tokenUsageManager struct {
	mu    sync.Mutex
	probe func(context.Context, float64, bool) (tokenUsageSnapshot, error)
}

func (s *Server) loadTokenUsage() (tokenUsageState, error) {
	state := tokenUsageState{Enabled: true}
	data, err := os.ReadFile(filepath.Join(s.profile, "token-usage.json"))
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil {
		return state, errors.New("无法读取本地用量统计")
	}
	if json.Unmarshal(data, &state) != nil {
		return state, errors.New("本地用量统计格式无效，原文件已保留")
	}
	return state, nil
}

func (s *Server) tokenUsageAvailable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready && !s.closing && !s.guestUpdateRequired
}

func (s *Server) tokenUsageView(state tokenUsageState) any {
	sources := map[string]string{"default": "默认配置（归属未核验）", "unknown": "来源不明（重复历史）"}
	s.claudeAccounts.mu.Lock()
	list, _ := s.loadClaudeAccounts()
	s.claudeAccounts.mu.Unlock()
	for _, account := range list.Accounts {
		if account.Source != "default" {
			label := account.Label
			if label == "" {
				label = "独立配置 " + account.ID[:min(6, len(account.ID))]
			}
			sources[account.guestID()] = label
		}
	}
	for _, day := range state.Snapshot.Days {
		if _, ok := sources[day.Source]; !ok {
			sources[day.Source] = "已移除的账号配置"
		}
	}
	return struct {
		tokenUsageState
		Available bool              `json:"available"`
		Sources   map[string]string `json:"sources"`
	}{state, s.tokenUsageAvailable(), sources}
}

func (s *Server) getTokenUsage(w http.ResponseWriter, r *http.Request) {
	s.tokenUsage.mu.Lock()
	state, err := s.loadTokenUsage()
	s.tokenUsage.mu.Unlock()
	if err != nil {
		requestError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonReply(w, s.tokenUsageView(state))
}

func (s *Server) mutateTokenUsage(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Action  string `json:"action"`
		Enabled *bool  `json:"enabled"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	s.tokenUsage.mu.Lock()
	defer s.tokenUsage.mu.Unlock()
	state, err := s.loadTokenUsage()
	if err != nil {
		requestError(w, err)
		return
	}
	switch input.Action {
	case "refresh":
		if !state.Enabled {
			requestError(w, errors.New("本地用量统计已暂停"))
			return
		}
		if err = s.collectTokenUsage(r.Context(), &state, true); err != nil {
			requestError(w, err)
			return
		}
	case "configure":
		if input.Enabled == nil {
			requestError(w, errors.New("请选择是否启用本地统计"))
			return
		}
		state.Enabled = *input.Enabled
	case "clear":
		// Apply to both stores before acknowledging. New requests alone can be
		// imported afterwards; retained transcripts are never removed or edited.
		state.Cutoff = float64(time.Now().UnixMilli()) / 1000
		state.Snapshot = tokenUsageSnapshot{Version: 1, Days: []tokenUsageDay{}}
		if err = writePrivate(filepath.Join(s.profile, "token-usage.json"), state); err != nil {
			requestError(w, errors.New("清除起点未保存，请重试"))
			return
		}
		if err = s.collectTokenUsage(r.Context(), &state, false); err != nil {
			requestError(w, err)
			return
		}
	default:
		http.Error(w, "未知用量统计操作", http.StatusBadRequest)
		return
	}
	if err = writePrivate(filepath.Join(s.profile, "token-usage.json"), state); err != nil {
		requestError(w, errors.New("用量统计尚未保存，请重试"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonReply(w, s.tokenUsageView(state))
}

func (s *Server) collectTokenUsage(ctx context.Context, state *tokenUsageState, scan bool) error {
	if !s.tokenUsageAvailable() {
		return errors.New("请先启动环境并完成组件更新，再刷新统计")
	}
	probe := s.tokenUsage.probe
	if probe == nil {
		probe = s.readTokenUsage
	}
	snapshot, err := probe(ctx, state.Cutoff, scan)
	if err != nil {
		return errors.New("暂时无法读取 Claude Code 用量，已保存的统计仍然保留")
	}
	if snapshot.Version != 1 || snapshot.Days == nil {
		return errors.New("用量记录格式暂不支持，已保存的统计仍然保留")
	}
	for _, d := range snapshot.Days {
		switch d.Client {
		case "", "unknown", "terminal", "desktop_code", "cowork", "ide", "automation", "remote", "other":
		default:
			return errors.New("运行入口记录校验失败，已保存的统计仍然保留")
		}
		if _, e := time.Parse("2006-01-02", d.Date); e != nil || d.Input < 0 || d.Output < 0 || d.Read < 0 || d.Write < 0 || d.Requests < 0 || len(d.Model) > 128 || !(d.Source == "unknown" || validClaudeGuestID(d.Source)) {
			return errors.New("用量记录校验失败，已保存的统计仍然保留")
		}
	}
	state.Snapshot = snapshot
	state.Cutoff = max(state.Cutoff, snapshot.Cutoff)
	return nil
}

var tokenUsageEncoded = func() string {
	var data bytes.Buffer
	w, _ := gzip.NewWriterLevel(&data, gzip.BestCompression)
	_, _ = w.Write([]byte(tokenUsageHelper))
	_ = w.Close()
	return cliTransport85(data.Bytes())
}()

func tokenUsageCommand() string {
	code := fmt.Sprintf("import base64,gzip;exec(compile(gzip.decompress(base64.b85decode('%s')),'<statistics>','exec'))", tokenUsageEncoded)
	return shellescape.QuoteCommand([]string{"python3", "-I", "-c", code})
}

func (s *Server) readTokenUsage(ctx context.Context, cutoff float64, scan bool) (tokenUsageSnapshot, error) {
	var result tokenUsageSnapshot
	input := map[string]any{"cutoff": cutoff, "scan": scan}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	data, _ := json.Marshal(input)
	cmd := s.sshCommand(ctx, "-T", s.sshTarget(), "--", "exec "+tokenUsageCommand())
	cmd.Stdin = strings.NewReader(string(data))
	var output bytes.Buffer
	cmd.Stdout = &boundedStatusWriter{buffer: &output, remaining: 4 << 20}
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		return result, err
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Server) monitorTokenUsage(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if s.tokenUsageAvailable() && !s.busy() {
			if epoch, ok := s.account.access(); ok && s.tokenUsage.mu.TryLock() {
				state, err := s.loadTokenUsage()
				if err == nil && state.Enabled {
					call, cancel := context.WithCancel(ctx)
					stop := context.AfterFunc(epoch, cancel)
					if s.collectTokenUsage(call, &state, true) == nil && call.Err() == nil {
						_ = writePrivate(filepath.Join(s.profile, "token-usage.json"), state)
					}
					stop()
					cancel()
				}
				s.tokenUsage.mu.Unlock()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
