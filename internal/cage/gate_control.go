package cage

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/host"
)

const gateRecordName = "cage-gate.json"

type gateRecord struct {
	Socket string `json:"socket"`
	Token  string `json:"token"`
	RunID  string `json:"run_id"`
}

type gateCommand struct {
	Node     string        `json:"node,omitempty"`
	Action   string        `json:"action"`
	Fault    bool          `json:"fault,omitempty"`
	Reason   string        `json:"reason,omitempty"`
	Report   *SafetyReport `json:"report,omitempty"`
	ReportID string        `json:"report_id,omitempty"`
}

// StartGateControl publishes an authenticated Unix endpoint in host-private
// storage. It creates no TCP listener or guest-visible control credential.
func StartGateControl(configPath, instanceDir string, gate *EgressGate) (func(), error) {
	dir, err := os.MkdirTemp(gateTempDir(), "cage-gate-")
	if err != nil {
		return nil, err
	}
	if err = host.EnsurePrivateDir(dir); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	socket := filepath.Join(dir, "control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	if err = host.ProtectFile(socket); err != nil {
		listener.Close()
		os.RemoveAll(dir)
		return nil, err
	}
	var token [32]byte
	if _, err = rand.Read(token[:]); err != nil {
		listener.Close()
		os.RemoveAll(dir)
		return nil, err
	}
	record := gateRecord{Socket: socket, Token: hex.EncodeToString(token[:]), RunID: gate.Status().RunID}
	data, _ := json.Marshal(record)
	temporary, err := os.CreateTemp(instanceDir, ".gate-*")
	if err != nil {
		listener.Close()
		os.RemoveAll(dir)
		return nil, err
	}
	defer os.Remove(temporary.Name())
	if err = host.ProtectFile(temporary.Name()); err != nil {
		temporary.Close()
		listener.Close()
		os.RemoveAll(dir)
		return nil, err
	}
	_, err = temporary.Write(data)
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temporary.Name(), filepath.Join(instanceDir, gateRecordName))
	}
	if err != nil {
		listener.Close()
		os.RemoveAll(dir)
		return nil, err
	}
	nodes := nodeListeners{dir: dir, configPath: configPath, gate: gate, verify: verifyNodeExit}
	server := &http.Server{ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 8192}
	// Keep watchdog reads and control transitions in one order, so a stale
	// watchdog snapshot cannot close a newly synchronized checking session.
	var controlMu sync.Mutex
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		if r.Host != "cage" || r.Header.Get("Origin") != "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+record.Token)) != 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		var operationError error
		if r.Method == "POST" && r.URL.Path == "/control" {
			var command gateCommand
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&command) != nil {
				w.WriteHeader(400)
				return
			}
			if len(command.Reason) > 200 {
				if command.Action != "close" {
					w.WriteHeader(400)
					return
				}
				// A detailed failure message must not prevent fail-closed cleanup.
				// Keep the wire/storage limit without splitting a UTF-8 character.
				command.Reason = gateCloseReason(command.Reason)
			}
			controlMu.Lock()
			defer controlMu.Unlock()
			var cfg Config
			if command.Action == "check" || command.Action == "grant" || command.Action == "renew" || command.Action == "proxy-revision" {
				var e error
				cfg, e = Load(configPath)
				if e == nil {
					e = cfg.RequireManagedProxy(filepath.Dir(configPath))
				}
				if e != nil {
					gate.Close("代理节点未应用，外网保持关闭")
					w.WriteHeader(409)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": e.Error()})
					return
				}
			}
			switch command.Action {
			case "account-node":
				operationError = nodes.prepare(command.Node)
			case "proxy-revision":
				operationError = gate.SyncProxyRevision(cfg)
				if operationError == nil {
					operationError = cfg.RecordRunningAt(instanceDir)
				} else {
					gate.Close("配置无法同步，请停止并重新启动环境后检测")
				}
			case "check":
				// A controller may have saved and started a node before its gate
				// synchronization failed. Adopt only changes supported by the same
				// hot-switch contract before collecting a new report.
				gate.mu.Lock()
				configurationChanged := !reflect.DeepEqual(gate.config, cfg)
				gate.mu.Unlock()
				if configurationChanged {
					operationError = gate.SyncProxyRevision(cfg)
					if operationError == nil {
						operationError = cfg.RecordRunningAt(instanceDir)
						nodes.refresh()
					}
				}
				if operationError == nil {
					operationError = gate.BeginCheck()
				} else {
					gate.Close("配置无法同步，请停止并重新启动环境后检测")
				}
			case "fault":
				operationError = gate.SetFault(command.Fault)
			case "grant":
				if command.Report == nil {
					operationError = errors.New("检测报告缺失")
				} else {
					operationError = gate.Grant(*command.Report)
				}
			case "renew":
				operationError = gate.Renew(command.ReportID)
			case "close":
				gate.Close(command.Reason)
			default:
				w.WriteHeader(400)
				return
			}
		} else if r.Method != "GET" || r.URL.Path != "/status" {
			w.WriteHeader(404)
			return
		}
		if operationError != nil {
			w.WriteHeader(409)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": operationError.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(gate.Status())
	})
	ctx, cancel := context.WithCancel(context.Background())
	watched := make(chan struct{})
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			nodes.close()
			gate.Close("网络控制服务已停止")
			_ = server.Close()
			// The watcher may be writing a diagnostic into the instance folder;
			// the caller can remove that folder once stop returns.
			<-watched
			_ = os.RemoveAll(dir)
		})
	}
	go func() { _ = server.Serve(listener); stop() }()
	go func() {
		defer close(watched)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				controlMu.Lock()
				cfg, err := Load(configPath)
				if err == nil {
					err = cfg.RequireManagedProxy(filepath.Dir(configPath))
				}
				if err != nil || cfg.Fingerprint() != gate.Status().Configuration {
					const reason = "配置已变更或无法读取，请重新检测；环境设置变化时需停止并重新启动"
					if state := gate.Status(); state.Mode != GateClosed || state.Reason != reason {
						gate.Close(reason)
						cause := "configuration_changed"
						if err != nil {
							cause = "configuration_unavailable"
						}
						_ = diagnostics.New(filepath.Dir(configPath)).Record("warn", "network-gate", "action=close cause="+cause+" recovery=network.check")
					}
				}
				controlMu.Unlock()
			}
		}
	}()
	return stop, nil
}

type GateClient struct{ record gateRecord }

func OpenGateClient(instanceDir string) (*GateClient, error) {
	path := filepath.Join(instanceDir, gateRecordName)
	if err := host.CheckPrivateFile(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("网络门槛控制记录不存在：%w", os.ErrNotExist)
		}
		return nil, errors.New("网络门槛控制记录不可用，请停止并重启环境")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 4096 {
		return nil, errors.New("网络门槛控制记录不可读")
	}
	var record gateRecord
	if json.Unmarshal(data, &record) != nil || len(record.Token) != 64 || record.RunID == "" || !filepath.IsAbs(record.Socket) {
		return nil, errors.New("网络门槛控制记录无效")
	}
	return &GateClient{record: record}, nil
}

func (c *GateClient) request(ctx context.Context, command *gateCommand) (GateStatus, error) {
	var state GateStatus
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", c.record.Socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("gate redirect refused") }}
	method, path := "GET", "/status"
	var body io.Reader
	if command != nil {
		method, path = "POST", "/control"
		data, err := json.Marshal(command)
		if err != nil {
			return state, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://cage"+path, body)
	if err != nil {
		return state, err
	}
	req.Header.Set("Authorization", "Bearer "+c.record.Token)
	resp, err := client.Do(req)
	if err != nil {
		return state, errors.New("无法连接 VM 外网络门槛控制器")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		var message struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&message)
		if message.Error == "" {
			message.Error = "网络门槛操作被拒绝"
		}
		return state, errors.New(message.Error)
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(&state) != nil || state.RunID != c.record.RunID || state.Policy != SafetyPolicyVersion {
		return GateStatus{}, errors.New("网络门槛状态不属于当前运行实例")
	}
	return state, nil
}

func (c *GateClient) Status(ctx context.Context) (GateStatus, error) { return c.request(ctx, nil) }
func (c *GateClient) BeginCheck(ctx context.Context) (GateStatus, error) {
	return c.request(ctx, &gateCommand{Action: "check"})
}
func (c *GateClient) SetFault(ctx context.Context, fault bool) (GateStatus, error) {
	return c.request(ctx, &gateCommand{Action: "fault", Fault: fault})
}
func (c *GateClient) Grant(ctx context.Context, report SafetyReport) (GateStatus, error) {
	return c.request(ctx, &gateCommand{Action: "grant", Report: &report})
}
func (c *GateClient) Renew(ctx context.Context, reportID string) (GateStatus, error) {
	return c.request(ctx, &gateCommand{Action: "renew", ReportID: reportID})
}
func gateCloseReason(reason string) string {
	if len(reason) > 200 {
		reason = reason[:200]
		for len(reason) > 0 && !utf8.ValidString(reason) {
			reason = reason[:len(reason)-1]
		}
	}
	return reason
}

func (c *GateClient) Close(ctx context.Context, reason string) (GateStatus, error) {
	return c.request(ctx, &gateCommand{Action: "close", Reason: gateCloseReason(reason)})
}
func (c *GateClient) SyncProxyRevision(ctx context.Context) (GateStatus, error) {
	return c.request(ctx, &gateCommand{Action: "proxy-revision"})
}
