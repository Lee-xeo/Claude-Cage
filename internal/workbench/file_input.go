package workbench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"al.essio.dev/pkg/shellescape"
	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/transfermeta"
	"github.com/sylvinus/agent-vm/internal/usererror"
)

// The broker belongs to Claude Desktop, not to the VM. An unopened desktop
// is an ordinary empty poll; a failed service must still surface as an error.
const desktopFilesPoll = `state=$(systemctl is-active cage-claude-desktop.service 2>/dev/null) || true
case "$state" in
  inactive|activating|deactivating|unknown)
    printf '%s\n' '{"available":false,"uploads":[],"downloads":[],"opens":[]}'
    exit 0 ;;
  active) ;;
  *) printf 'desktop file service state=%s\n' "$state" >&2; exit 1 ;;
esac
`

// Drain stderr even beyond the retained prefix; a noisy child must not block.
type fileControlStderr struct{ data []byte }

func (b *fileControlStderr) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := 4096 - len(b.data); remaining > 0 {
		b.data = append(b.data, p[:min(len(p), remaining)]...)
	}
	return n, nil
}

var terminalFileID = regexp.MustCompile(`^[a-f0-9]{16}$`)

func validFileTargetID(target, id string) bool {
	if target == "terminal" {
		return terminalFileID.MatchString(id)
	}
	return browserFileID.MatchString(id)
}

func desktopFileSocket(desktopID string) (string, error) {
	switch desktopID {
	case "", "default":
		return "/run/cage-desktop/files.sock", nil
	default:
		if !validClaudeID(desktopID) {
			return "", errors.New("桌面实例标识无效")
		}
		return "/run/desktop-" + desktopID + "/files.sock", nil
	}
}

func (s *Server) rememberDesktopFileOwner(id, desktopID string) {
	if !browserFileID.MatchString(id) {
		return
	}
	if desktopID == "default" {
		desktopID = ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.desktopFileOwners == nil {
		s.desktopFileOwners = map[string]string{}
	} else if len(s.desktopFileOwners) >= 1024 {
		if _, exists := s.desktopFileOwners[id]; !exists {
			clear(s.desktopFileOwners)
		}
	}
	s.desktopFileOwners[id] = desktopID
}

func (s *Server) desktopFileOwner(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.desktopFileOwners[id]
}

// desktopFilesPollAllScript polls the default Desktop broker (when active) and
// any active independent account desktop brokers in a single guest command.
func desktopFilesPollAllScript(asWorkUser string) string {
	return `state=$(systemctl is-active cage-claude-desktop.service 2>/dev/null) || true
default_active=0
default_failed=0
case "$state" in
  inactive|activating|deactivating|unknown) ;;
  active) default_active=1 ;;
  # A failed default Desktop does not stop the account Desktops' transfers.
  *) default_failed=1 ;;
esac
sources=()
if [ "$default_active" -eq 1 ]; then
  sources+=("default:/run/cage-desktop/files.sock")
fi
for sock in /run/desktop-*/files.sock; do
  [ -S "$sock" ] || continue
  dir=${sock%/files.sock}
  id=${dir#/run/desktop-}
  case "$id" in
    *[!a-f0-9]*) continue ;;
  esac
  [ "${#id}" -eq 24 ] || continue
  astate=$(systemctl is-active "desktop-${id}.service" 2>/dev/null) || true
  if [ "$astate" = active ]; then
    sources+=("${id}:${sock}")
  fi
done
if [ "${#sources[@]}" -eq 0 ]; then
  if [ "$default_failed" -eq 1 ]; then
    printf 'desktop file service state=%s\n' "$state" >&2
    exit 1
  fi
  printf '%s\n' '{"available":false,"sources":{}}'
  exit 0
fi
out=""
for entry in "${sources[@]}"; do
  key=${entry%%:*}
  sock=${entry#*:}
  if ! body=$(` + asWorkUser + `curl --silent --show-error --max-time 5 --write-out '\nCAGE_FILE_HTTP:%{http_code}' --unix-socket "$sock" -X GET http://localhost/requests); then
    if [ "$key" = default ]; then
      exit 1
    fi
    continue
  fi
  case "$body" in
    *$'\n'CAGE_FILE_HTTP:*)
      code=${body##*$'\n'CAGE_FILE_HTTP:}
      json=${body%$'\n'CAGE_FILE_HTTP:*}
      ;;
    *)
      if [ "$key" = default ]; then
        printf 'desktop file broker missing status\n' >&2
        exit 1
      fi
      continue
      ;;
  esac
  if [ "$code" != 200 ] || [ -z "$json" ]; then
    if [ "$key" = default ]; then
      printf 'desktop file broker http=%s\n' "$code" >&2
      exit 1
    fi
    continue
  fi
  if [ -n "$out" ]; then out+=","; fi
  out+="\"${key}\":${json}"
done
printf '{"available":true,"sources":{%s}}\n' "$out"
`
}

func (s *Server) mergeDesktopFilePoll(raw map[string]any) map[string]any {
	merged := map[string]any{
		"available": false,
		"uploads":   []any{},
		"downloads": []any{},
		"opens":     []any{},
	}
	sources, ok := raw["sources"].(map[string]any)
	if !ok || len(sources) == 0 {
		return merged
	}
	keys := make([]string, 0, len(sources))
	for key := range sources {
		if key != "default" && !validClaudeID(key) {
			continue
		}
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i] == "default" {
			return true
		}
		if keys[j] == "default" {
			return false
		}
		return keys[i] < keys[j]
	})
	uploads := []any{}
	downloads := []any{}
	opens := []any{}
	seenUploads := map[string]bool{}
	seenDownloads := map[string]bool{}
	seenOpens := map[string]bool{}
	available := false
	for _, key := range keys {
		src, ok := sources[key].(map[string]any)
		if !ok {
			continue
		}
		if avail, _ := src["available"].(bool); avail {
			available = true
		}
		if list, ok := src["uploads"].([]any); ok {
			for _, value := range list {
				item, ok := value.(map[string]any)
				if !ok {
					continue
				}
				id, _ := item["id"].(string)
				if id != "" {
					if seenUploads[id] {
						continue
					}
					seenUploads[id] = true
					s.rememberDesktopFileOwner(id, key)
				}
				item["desktop"] = key
				uploads = append(uploads, item)
			}
		}
		if list, ok := src["downloads"].([]any); ok {
			for _, value := range list {
				item, ok := value.(map[string]any)
				if !ok {
					continue
				}
				id, _ := item["id"].(string)
				if id != "" {
					if seenDownloads[id] {
						continue
					}
					seenDownloads[id] = true
					s.rememberDesktopFileOwner(id, key)
				}
				item["desktop"] = key
				downloads = append(downloads, item)
			}
		}
		if list, ok := src["opens"].([]any); ok {
			for _, value := range list {
				item, ok := value.(map[string]any)
				if !ok {
					continue
				}
				id, _ := item["id"].(string)
				if id != "" {
					if seenOpens[id] {
						continue
					}
					seenOpens[id] = true
					s.rememberDesktopFileOwner(id, key)
				}
				item["desktop"] = key
				opens = append(opens, item)
			}
		}
	}
	merged["available"] = available
	merged["uploads"] = uploads
	merged["downloads"] = downloads
	merged["opens"] = opens
	return merged
}

func (s *Server) pollAllDesktopFiles(ctx context.Context) (map[string]any, error) {
	asWorkUser, err := s.linuxTemplate("sudo -H -u cage ")
	if err != nil {
		return nil, err
	}
	script := desktopFilesPollAllScript(asWorkUser)
	var stderr fileControlStderr
	var data []byte
	var readErr error
	const limit = 262144
	shared := false
	var reply guestSessionReply
	if reply, shared, err = s.guestShared(ctx, script); shared {
		data, stderr.data = reply.stdout, reply.stderr
		if err == nil && reply.code != 0 {
			err = fmt.Errorf("exit status %d", reply.code)
		}
	}
	if !shared {
		command := "exec /bin/bash --noprofile --norc -c " + shellescape.Quote(script)
		cmd := s.sshCommand(ctx, "-T", s.sshTarget(), "--", command)
		cmd.Stderr = &stderr
		pipe, pipeErr := cmd.StdoutPipe()
		if pipeErr != nil {
			return nil, pipeErr
		}
		if err = cmd.Start(); err != nil {
			return nil, usererror.Wrap("连接文件传递服务", err, "请确认 Cage 安装完整，环境已经启动；持续失败时在设置中导出诊断包。")
		}
		data, readErr = io.ReadAll(io.LimitReader(pipe, limit+64))
		if readErr != nil || len(data) > limit+64 {
			_ = cmd.Process.Kill()
		}
		err = cmd.Wait()
	}
	var raw map[string]any
	decodeErr := json.Unmarshal(data, &raw)
	if err != nil || readErr != nil || len(data) > limit || decodeErr != nil || raw == nil {
		_ = diagnostics.New(s.profile).Record("error", "desktop-files", fmt.Sprintf("target=desktop method=GET route=/requests exit=%v read=%v invalid_json=%t oversized=%t context=%v stderr=%s", err, readErr, decodeErr != nil || raw == nil, len(data) > limit, ctx.Err(), stderr.data))
		switch {
		case ctx.Err() != nil:
			return nil, usererror.Wrap("传递文件", ctx.Err(), "请确认环境仍在运行；恢复连接后重新选择文件，并核对目标是否已收到文件。")
		case err != nil:
			return nil, executionFailure("连接文件传递服务", err, string(stderr.data))
		case readErr != nil:
			return nil, usererror.Wrap("读取文件传递结果", readErr, "结果没有完整返回，请恢复连接后核对文件是否已收到。")
		case len(data) > limit:
			return nil, errors.New("读取文件传递结果失败：服务返回的数据超过限制。请分批处理文件；持续出现时导出诊断包。")
		default:
			return nil, errors.New("读取文件传递结果失败：服务没有返回有效的文件记录。请重新打开接收文件的应用；旧环境请先更新环境组件。")
		}
	}
	return s.mergeDesktopFilePoll(raw), nil
}

// Desktop requests use the private session socket, carried over the existing
// SSH connection. Directory selections use an authorized project's guest path.
func (s *Server) guestFiles(ctx context.Context, target, method, path string, body io.Reader, name string) (map[string]any, error) {
	return s.guestFilesForDesktop(ctx, "", target, method, path, body, name)
}

func (s *Server) guestFilesForDesktop(ctx context.Context, desktopID, target, method, path string, body io.Reader, name string) (map[string]any, error) {
	asWorkUser, err := s.linuxTemplate("sudo -H -u cage ")
	if err != nil {
		return nil, err
	}
	var script string
	if target == "terminal" {
		script = asWorkUser + "/usr/local/bin/cage-desktop-files --save-file " + shellescape.Quote(name)
	} else {
		sock, sockErr := desktopFileSocket(desktopID)
		if sockErr != nil {
			return nil, sockErr
		}
		script = asWorkUser + "curl --silent --show-error --max-time 1800 --write-out '\\nCAGE_FILE_HTTP:%{http_code}' --unix-socket " + shellescape.Quote(sock) + " -X " + method
		if body != nil {
			script += " --upload-file -"
		}
		script += " " + shellescape.Quote("http://localhost"+path)
	}
	var stderr fileControlStderr
	var data []byte
	var readErr error
	shared := false
	if target != "terminal" && method == "GET" && path == "/requests" && (desktopID == "" || desktopID == "default") {
		script = desktopFilesPoll + script
		// The workbench asks for pending requests every second or two.
		var reply guestSessionReply
		if reply, shared, err = s.guestShared(ctx, script); shared {
			data, stderr.data = reply.stdout, reply.stderr
			if err == nil && reply.code != 0 {
				err = fmt.Errorf("exit status %d", reply.code)
			}
		}
	}
	if !shared {
		command := "exec /bin/bash --noprofile --norc -c " + shellescape.Quote(script)
		cmd := s.sshCommand(ctx, "-T", s.sshTarget(), "--", command)
		cmd.Stderr = &stderr
		cmd.Stdin = body
		pipe, pipeErr := cmd.StdoutPipe()
		if pipeErr != nil {
			return nil, pipeErr
		}
		if err = cmd.Start(); err != nil {
			return nil, usererror.Wrap("连接文件传递服务", err, "请确认 Cage 安装完整，环境已经启动；持续失败时在设置中导出诊断包。")
		}
		data, readErr = io.ReadAll(io.LimitReader(pipe, 65537+64))
		if readErr != nil || len(data) > 65536+64 {
			_ = cmd.Process.Kill()
		}
		err = cmd.Wait()
	}
	httpStatus := 0
	if at := bytes.LastIndex(data, []byte("\nCAGE_FILE_HTTP:")); at >= 0 && target != "terminal" {
		httpStatus, _ = strconv.Atoi(strings.TrimSpace(string(data[at+len("\nCAGE_FILE_HTTP:"):])))
		data = data[:at]
	}
	var result map[string]any
	decodeErr := json.Unmarshal(data, &result)
	if err != nil || readErr != nil || len(data) > 65536 || decodeErr != nil || result == nil || httpStatus >= 400 {
		// No response body, filename, request ID or query string enters the log.
		route := strings.SplitN(path, "?", 2)[0]
		_ = diagnostics.New(s.profile).Record("error", "desktop-files", fmt.Sprintf("target=%s method=%s route=%s exit=%v read=%v invalid_json=%t oversized=%t context=%v stderr=%s", target, method, route, err, readErr, decodeErr != nil || result == nil, len(data) > 65536, ctx.Err(), stderr.data))
		switch {
		case ctx.Err() != nil:
			return nil, usererror.Wrap("传递文件", ctx.Err(), "请确认环境仍在运行；恢复连接后重新选择文件，并核对目标是否已收到文件。")
		case httpStatus >= 400:
			return nil, fileServiceFailure(httpStatus, data)
		case err != nil:
			return nil, executionFailure("连接文件传递服务", err, string(stderr.data))
		case readErr != nil:
			return nil, usererror.Wrap("读取文件传递结果", readErr, "结果没有完整返回，请恢复连接后核对文件是否已收到。")
		case len(data) > 65536:
			return nil, errors.New("读取文件传递结果失败：服务返回的数据超过 64 KiB。请分批处理文件；持续出现时导出诊断包。")
		default:
			return nil, errors.New("读取文件传递结果失败：服务没有返回有效的文件记录。请重新打开接收文件的应用；旧环境请先更新环境组件。")
		}
	}
	return result, nil
}

func (s *Server) fileTargetReady(target, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ready {
		return false
	}
	if target == "terminal" {
		terminal := s.terminals.get(id)
		if terminal == nil {
			return false
		}
		terminal.mu.Lock()
		defer terminal.mu.Unlock()
		return !terminal.closed
	}
	return target == "browser" || target == "desktop"
}

// projectReadCheck makes the shared folder answer one request that no cache
// can serve: the lookup of a name that does not exist. A stalled SSHFS
// transport can still return cached attributes and directory listings.
const projectReadCheck = `import os, sys
try:
    os.lstat(os.path.join(sys.argv[1], "." + os.urandom(8).hex()))
except FileNotFoundError:
    pass
`

var errProjectUnreadable = errors.New("项目文件夹暂时无法读取，请检查共享连接后重试。持续失败时，请保存工作后重启环境")

// checkProjectReadable bounds the check, so a stalled share fails this
// handoff within seconds instead of blocking the Claude Desktop file service.
// It leaves the transport and mount in place for writes still in progress.
func (s *Server) checkProjectReadable(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, projectReadTimeout+2*time.Second)
	defer cancel()
	if _, err := s.guest(ctx, projectReadCommand(path, projectReadTimeout)); err != nil {
		return errProjectUnreadable
	}
	return nil
}

const projectReadTimeout = 3 * time.Second

func projectReadCommand(path string, limit time.Duration) string {
	check := "mountpoint -q -- " + shellescape.Quote(path) + " && exec /usr/bin/python3 -I -c " + shellescape.Quote(projectReadCheck) + " " + shellescape.Quote(path)
	seconds := strconv.FormatFloat(limit.Seconds(), 'f', -1, 64)
	return "exec timeout --kill-after=1s " + seconds + "s /bin/sh -c " + shellescape.Quote(check)
}

func (s *Server) fileInputAction(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Target    string  `json:"target"`
		ID        string  `json:"id"`
		Desktop   string  `json:"desktop,omitempty"`
		Kind      string  `json:"kind"`
		X         float64 `json:"x"`
		Y         float64 `json:"y"`
		Surface   string  `json:"surface,omitempty"`
		Workspace string  `json:"workspace"`
		Window    uint32  `json:"window,omitempty"`
		PID       int32   `json:"pid,omitempty"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	action := r.PathValue("action")
	if action != "start" && action != "finish" && action != "cancel" && action != "directory" && action != "download-retry" {
		http.NotFound(w, r)
		return
	}
	if input.Desktop != "" && input.Desktop != "default" && !validClaudeID(input.Desktop) {
		http.Error(w, "桌面实例标识无效", 400)
		return
	}
	if !(action == "start" && (input.Target == "browser" && input.ID != "" && len(input.ID) <= 256 || input.Target == "desktop")) && !validFileTargetID(input.Target, input.ID) {
		http.Error(w, "文件目标无效", 400)
		return
	}
	if !s.fileTargetReady(input.Target, input.ID) {
		http.Error(w, "文件目标已关闭或环境尚未就绪", 409)
		return
	}
	if action == "start" && input.Kind != "drop" && input.Kind != "paste" {
		http.Error(w, "文件传递方式无效", 400)
		return
	}
	if (action == "directory" || action == "download-retry") && input.Target != "desktop" {
		http.Error(w, "文件夹目标无效", 400)
		return
	}
	if action == "directory" {
		if !s.lockProjectOperation(r.Context()) {
			http.Error(w, "另一个操作仍在进行，请稍候", 409)
			return
		}
		defer s.op.Unlock()
		if !s.fileTargetReady("desktop", input.ID) || s.busy() {
			http.Error(w, "环境尚未就绪或正在处理其他操作，请稍后重试", 409)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	body, _ := json.Marshal(input)
	if action == "directory" {
		if input.Workspace == "" {
			http.Error(w, "请选择已打开的项目", 400)
			return
		}
		workspace, err := s.connectProject(input.Workspace, false)
		if err != nil {
			requestError(w, err)
			return
		}
		path := guestPath(workspace)
		// The environment folder is a local guest directory; client mounts
		// keep their own liveness checks.
		if workspace != s.environmentDir() && !isClientMountPath(workspace) {
			if err := s.checkProjectReadable(ctx, path); err != nil {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
				return
			}
		}
		body, _ = json.Marshal(map[string]string{"id": input.ID, "path": path})
	}
	if input.Target == "terminal" {
		jsonReply(w, map[string]any{"ok": true, "id": input.ID})
		return
	}
	if input.Target == "browser" {
		s.mu.Lock()
		token := s.credentials.Token
		s.mu.Unlock()
		data, err := s.guestAPI(ctx, token, "POST", "/upload-"+action, body)
		if err != nil {
			requestError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
		return
	}
	desktopID := input.Desktop
	if action != "start" && desktopID == "" {
		desktopID = s.desktopFileOwner(input.ID)
	}
	result, err := s.guestFilesForDesktop(ctx, desktopID, "desktop", "POST", "/"+action, bytes.NewReader(body), "")
	if err != nil {
		requestError(w, err)
		return
	}
	if action == "start" {
		if id, _ := result["id"].(string); id != "" {
			s.rememberDesktopFileOwner(id, desktopID)
		}
	}
	jsonReply(w, result)
}

func (s *Server) desktopFileRequests(w http.ResponseWriter, r *http.Request) {
	if !s.fileTargetReady("desktop", "") {
		http.Error(w, "环境尚未就绪", 409)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	result, err := s.pollAllDesktopFiles(ctx)
	if err != nil {
		// Service/transport failures are not invalid user input. pollAllDesktopFiles has
		// already recorded the bounded diagnostic, including the real exit code.
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	s.rememberDesktopDownloads(result)
	jsonReply(w, result)
}

// Register completed exports before the UI can ask a native bridge to save them.
// Guest paths are never accepted as arbitrary host paths.
func (s *Server) rememberDesktopDownloads(result map[string]any) {
	items, _ := result["downloads"].([]any)
	downloads := make([]any, 0, len(items))
	for _, value := range items {
		item, _ := value.(map[string]any)
		id, _ := item["id"].(string)
		name, _ := item["name"].(string)
		if !browserFileID.MatchString(id) || !browserFileName(name) {
			continue
		}
		item["target"] = "desktop"
		s.resolveSharedDesktopDownload(item, id, name)
		downloads = append(downloads, item)
	}
	result["downloads"] = downloads
}

func (s *Server) resolveSharedDesktopDownload(item map[string]any, id, name string) {
	path, _ := item["path"].(string)
	delete(item, "path")
	if item["state"] != "saved" {
		return
	}
	// A host receipt survives a change of download directory or a Save As.
	if saved, err := readSavedDownload(s.profile, id); err == nil {
		item["name"], item["path"] = saved.Name, saved.Path
		return
	}
	root := s.environmentDir()
	if path != strings.TrimRight(guestPath(root), "/")+"/Downloads/"+name {
		item["state"], item["retryable"], item["error"] = "failed", false, "无法确认这条下载记录的本机保存位置。"
		return
	}
	hostPath := filepath.Join(root, "Downloads", name)
	if err := rememberDownload(s.profile, SavedDownload{ID: id, Name: name, Path: hostPath}, true); err != nil {
		item["state"], item["retryable"], item["error"] = "failed", false, "无法记录这次下载的本机保存位置。"
		return
	}
	item["path"] = hostPath
}

func (s *Server) fileInputUpload(w http.ResponseWriter, r *http.Request) {
	target, id, name := r.PathValue("target"), r.PathValue("id"), r.URL.Query().Get("name")
	if !validFileTargetID(target, id) || !browserFileName(name) {
		http.Error(w, "文件名或上传目标无效", 400)
		return
	}
	if !s.fileTargetReady(target, id) {
		http.Error(w, "文件目标已关闭或环境尚未就绪", 409)
		return
	}
	if target == "browser" {
		s.browserUpload(w, r)
		return
	}
	if r.ContentLength > browserFileLimit {
		http.Error(w, "每个文件不能超过 2 GB", 413)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	r.Body = http.MaxBytesReader(w, r.Body, browserFileLimit)
	prepared, err := transfermeta.Prepare(name, r.Body, r.ContentLength, s.transferOptions())
	if err != nil {
		transferMetadataError(w, err)
		return
	}
	defer prepared.Close()
	if prepared.Unclean {
		w.Header().Set("X-Cage-Metadata-Warning", "metadata-may-remain")
	}
	desktopID := ""
	if target == "desktop" {
		desktopID = s.desktopFileOwner(id)
	}
	result, err := s.guestFilesForDesktop(ctx, desktopID, target, "POST", "/upload?"+url.Values{"id": {id}, "name": {name}}.Encode(), prepared.Body, name)
	if err != nil {
		requestError(w, err)
		return
	}
	if target == "terminal" {
		path, _ := result["path"].(string)
		valid := s.linuxPathHasPrefix(path, "/home/cage/.local/share/cage/attachments/files/") && !strings.ContainsAny(path, "\x00\r\n")
		if !valid {
			http.Error(w, "文件保存路径无效", 502)
			return
		}
	}
	jsonReply(w, result)
}
