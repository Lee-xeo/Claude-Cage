package workbench

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sylvinus/agent-vm/internal/diagnostics"
	"github.com/sylvinus/agent-vm/internal/hostbridge"
)

// guestSubmissionRoot is the neutral folder inside the workspace environment
// where cage-plugin freezes validated drafts for user review.
const guestSubmissionRoot = ".local/share/plugin-submissions"

const (
	draftListLimit     = 4 << 20
	draftFileViewLimit = 256 << 10
	draftTarLimit      = 128 << 20
)

type pluginDraftFileInfo struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
	Text   bool   `json:"text"`
}

type pluginDraftSubmission struct {
	ID          string                  `json:"id"`
	Name        string                  `json:"name"`
	DisplayName string                  `json:"displayName,omitempty"`
	Version     string                  `json:"version"`
	Description string                  `json:"description"`
	Summary     string                  `json:"summary,omitempty"`
	Target      string                  `json:"target"`
	Platforms   []string                `json:"platforms,omitempty"`
	Permissions []string                `json:"permissions"`
	Categories  []string                `json:"categories,omitempty"`
	Contributes *hostbridge.Contributes `json:"contributes,omitempty"`
	SubmittedAt string                  `json:"submittedAt,omitempty"`
	Tools       []hostbridge.ToolInfo   `json:"tools,omitempty"`
	Warnings    []string                `json:"warnings,omitempty"`
	Files       []pluginDraftFileInfo   `json:"files,omitempty"`
}

type pluginDraftView struct {
	ID              string                `json:"id"`
	Name            string                `json:"name"`
	DisplayName     string                `json:"display_name,omitempty"`
	Version         string                `json:"version"`
	Description     string                `json:"description"`
	Summary         string                `json:"summary,omitempty"`
	Target          string                `json:"target"`
	RecommendedKind string                `json:"recommended_kind"`
	Platforms       []string              `json:"platforms,omitempty"`
	Permissions     []string              `json:"permissions"`
	Categories      []string              `json:"categories,omitempty"`
	Contributes     *hostContributesView  `json:"contributes,omitempty"`
	SubmittedAt     string                `json:"submitted_at,omitempty"`
	Tools           []hostbridge.ToolInfo `json:"tools,omitempty"`
	Warnings        []string              `json:"warnings,omitempty"`
	Files           []pluginDraftFileInfo `json:"files"`
	// Digest names the exact copy shown for review. Installation takes it
	// back and installs only that copy.
	Digest          string `json:"digest,omitempty"`
	Exists          bool   `json:"exists"`
	ExistingVersion string `json:"existing_version,omitempty"`
	ExistingSource  string `json:"existing_source,omitempty"`
	Protected       bool   `json:"protected,omitempty"`
}

type pluginDraftsListView struct {
	Ready  bool              `json:"ready"`
	Drafts []pluginDraftView `json:"drafts"`
}

// guestListSubmissionsScript reads every valid submission.json under
// ~/.local/share/extension-submissions and emits a single JSON array.
const guestListSubmissionsScript = `python3 -c '
import json, os, pathlib, sys
root = pathlib.Path.home() / sys.argv[1]
items = []
if root.is_dir():
    for entry in sorted(root.iterdir(), key=lambda p: p.name):
        if not entry.is_dir() or entry.is_symlink() or entry.name.startswith("."):
            continue
        meta = entry / "submission.json"
        if not meta.is_file() or meta.is_symlink():
            continue
        try:
            items.append(json.loads(meta.read_text(encoding="utf-8")))
        except Exception:
            continue
sys.stdout.write(json.dumps(items))
' "$1"`

const guestTarSubmissionScript = `set -e
dir="$HOME/$1/$2/files"
[ -d "$dir" ] && [ ! -L "$dir" ] || exit 2
tar -cf - -C "$dir" .`

const guestDiscardSubmissionScript = `set -e
dir="$HOME/$1/$2"
[ -d "$dir" ] && [ ! -L "$dir" ] && rm -rf -- "$dir" || true`

// draftSnapshotMu orders the copies kept for review; two windows may list at
// the same time.
var draftSnapshotMu sync.Mutex

// draftReviewDir holds this computer's copy of each draft under review. The
// environment can rewrite its own folder at any time, so what the user reads
// and what is installed both come from here.
func (s *Server) draftReviewDir() string {
	return filepath.Join(s.profile, "plugin-draft-review")
}

// draftTree lists the plain files below dir with a digest over their paths,
// executable bits and contents.
func draftTree(dir string) ([]pluginDraftFileInfo, string, error) {
	files := []pluginDraftFileInfo{}
	sum := sha256.New()
	err := filepath.WalkDir(dir, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == dir {
			return nil
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, current)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s 不是普通文件或文件夹", filepath.ToSlash(rel))
		}
		data, err := os.ReadFile(current)
		if err != nil {
			return err
		}
		content := sha256.Sum256(data)
		item := pluginDraftFileInfo{
			Path:   filepath.ToSlash(rel),
			Size:   int64(len(data)),
			SHA256: hex.EncodeToString(content[:]),
			Text:   !bytes.ContainsRune(data, 0) && utf8.Valid(data),
		}
		files = append(files, item)
		fmt.Fprintf(sum, "%s\x00%t\x00%s\n", item.Path, info.Mode().Perm()&0o111 != 0, item.SHA256)
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	return files, hex.EncodeToString(sum.Sum(nil)), nil
}

// snapshotDraft replaces the review copy of one draft with what the
// environment holds now, and reports what that copy declares and contains.
func (s *Server) snapshotDraft(ctx context.Context, id string) (hostbridge.Manifest, []pluginDraftFileInfo, string, error) {
	if !hostbridge.ValidPluginName(id) {
		return hostbridge.Manifest{}, nil, "", errors.New("插件名称无效")
	}
	draftSnapshotMu.Lock()
	defer draftSnapshotMu.Unlock()
	root := s.draftReviewDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return hostbridge.Manifest{}, nil, "", err
	}
	final := filepath.Join(root, id)
	_ = os.RemoveAll(final)
	staging, err := os.MkdirTemp(root, ".fetching-*")
	if err != nil {
		return hostbridge.Manifest{}, nil, "", err
	}
	defer os.RemoveAll(staging)
	if err := s.fetchSubmittedDraftToDir(ctx, id, staging); err != nil {
		return hostbridge.Manifest{}, nil, "", err
	}
	manifest, _, err := hostbridge.ValidateDraftDir(staging, id, "", "")
	if err != nil {
		return hostbridge.Manifest{}, nil, "", err
	}
	files, digest, err := draftTree(staging)
	if err != nil {
		return hostbridge.Manifest{}, nil, "", err
	}
	if err := os.Rename(staging, final); err != nil {
		return hostbridge.Manifest{}, nil, "", err
	}
	return manifest, files, digest, nil
}

// reviewedDraftDir is the review copy of a draft when it still matches digest.
func (s *Server) reviewedDraftDir(id, digest string) (string, error) {
	if !hostbridge.ValidPluginName(id) {
		return "", errors.New("插件名称无效")
	}
	draftSnapshotMu.Lock()
	defer draftSnapshotMu.Unlock()
	dir := filepath.Join(s.draftReviewDir(), id)
	if digest == "" {
		return "", errors.New("请先在待审核草稿中查看这个插件，再安装")
	}
	_, current, err := draftTree(dir)
	if err != nil || current != digest {
		return "", errors.New("草稿在审核后发生了变化，请刷新待审核草稿并重新查看")
	}
	return dir, nil
}

func validDraftRelPath(rel string) bool {
	if rel == "" || len(rel) > 200 || strings.Contains(rel, "\\") || strings.HasPrefix(rel, "/") || strings.HasSuffix(rel, "/") {
		return false
	}
	parts := strings.Split(rel, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func (s *Server) listSubmittedDrafts(ctx context.Context) ([]pluginDraftSubmission, bool, error) {
	if s.draftStoreOverride != "" {
		return listLocalSubmittedDrafts(s.draftStoreOverride)
	}
	s.mu.Lock()
	ready := s.ready
	s.mu.Unlock()
	if !ready {
		return nil, false, nil
	}
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := s.sshCommand(runCtx, "-T", s.sshTarget(), "--", "sh -c "+shellQuote(guestListSubmissionsScript)+" sh "+shellQuote(guestSubmissionRoot))
	out, err := cmd.Output()
	if err != nil {
		return nil, true, err
	}
	if len(out) > draftListLimit {
		return nil, true, errors.New("待审核草稿元数据过大")
	}
	var subs []pluginDraftSubmission
	if err := json.Unmarshal(out, &subs); err != nil {
		return nil, true, err
	}
	return subs, true, nil
}

func listLocalSubmittedDrafts(root string) ([]pluginDraftSubmission, bool, error) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, true, nil
	}
	if err != nil {
		return nil, true, err
	}
	var subs []pluginDraftSubmission
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		metaPath := filepath.Join(root, entry.Name(), "submission.json")
		raw, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}
		var sub pluginDraftSubmission
		if json.Unmarshal(raw, &sub) == nil {
			subs = append(subs, sub)
		}
	}
	sort.Slice(subs, func(i, j int) bool { return subs[i].Name < subs[j].Name })
	return subs, true, nil
}

// readReviewedDraftFile reads one file of the copy kept for review.
func (s *Server) readReviewedDraftFile(id, rel string) ([]byte, error) {
	if !hostbridge.ValidPluginName(id) || !validDraftRelPath(rel) {
		return nil, errors.New("无效的草稿或文件路径")
	}
	draftSnapshotMu.Lock()
	defer draftSnapshotMu.Unlock()
	full := filepath.Join(s.draftReviewDir(), id, filepath.FromSlash(rel))
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() {
		return nil, os.ErrNotExist
	}
	if info.Size() > draftFileViewLimit {
		return nil, errors.New("文件超过 256 KB 预览限制")
	}
	return os.ReadFile(full)
}

func (s *Server) fetchSubmittedDraftToDir(ctx context.Context, id, dstDir string) error {
	if !hostbridge.ValidPluginName(id) {
		return errors.New("插件名称无效")
	}
	if s.draftStoreOverride != "" {
		src := filepath.Join(s.draftStoreOverride, id, "files")
		return copyPlainTree(src, dstDir)
	}
	runCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := s.sshCommand(runCtx, "-T", s.sshTarget(), "--",
		"sh -c "+shellQuote(guestTarSubmissionScript)+" sh "+shellQuote(guestSubmissionRoot)+" "+shellQuote(id))
	out, err := cmd.Output()
	if err != nil {
		return errors.New("没有找到该待审核草稿")
	}
	if int64(len(out)) > draftTarLimit {
		return errors.New("草稿体积超过上限")
	}
	return extractPlainTar(bytes.NewReader(out), dstDir)
}

func (s *Server) discardSubmittedDraft(ctx context.Context, id string) error {
	if !hostbridge.ValidPluginName(id) {
		return errors.New("插件名称无效")
	}
	_ = os.RemoveAll(filepath.Join(s.draftReviewDir(), id))
	if s.draftStoreOverride != "" {
		return os.RemoveAll(filepath.Join(s.draftStoreOverride, id))
	}
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := s.sshCommand(runCtx, "-T", s.sshTarget(), "--",
		"sh -c "+shellQuote(guestDiscardSubmissionScript)+" sh "+shellQuote(guestSubmissionRoot)+" "+shellQuote(id))
	return cmd.Run()
}

func copyPlainTree(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil || info.Mode().Type() != os.ModeDir {
		return errors.New("没有找到该待审核草稿")
	}
	return filepath.WalkDir(src, func(current string, _ os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == src {
			return nil
		}
		rel, err := filepath.Rel(src, current)
		if err != nil {
			return err
		}
		linfo, err := os.Lstat(current)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch linfo.Mode().Type() {
		case os.ModeDir:
			return os.MkdirAll(target, 0o755)
		case 0:
			raw, err := os.ReadFile(current)
			if err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if linfo.Mode().Perm()&0o111 != 0 {
				mode = 0o755
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.WriteFile(target, raw, mode)
		default:
			return fmt.Errorf("%s 不是普通文件或文件夹", filepath.ToSlash(rel))
		}
	})
}

func extractPlainTar(r io.Reader, dst string) error {
	tr := tar.NewReader(r)
	var total int64
	files := 0
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("解包草稿失败：%w", err)
		}
		cleaned := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
		if cleaned == "." || cleaned == "" {
			continue
		}
		if !validDraftRelPath(cleaned) {
			return fmt.Errorf("草稿包含非法路径 %q", hdr.Name)
		}
		target := filepath.Join(dst, filepath.FromSlash(cleaned))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			files++
			if files > 2000 || hdr.Size < 0 || total+hdr.Size > 100<<20 {
				return errors.New("草稿文件数量或体积超过限制")
			}
			total += hdr.Size
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			data, err := io.ReadAll(io.LimitReader(tr, hdr.Size+1))
			if err != nil {
				return err
			}
			if int64(len(data)) != hdr.Size {
				return errors.New("草稿文件大小不一致")
			}
			mode := os.FileMode(0o644)
			if hdr.Mode&0o111 != 0 {
				mode = 0o755
			}
			if err := os.WriteFile(target, data, mode); err != nil {
				return err
			}
		default:
			return fmt.Errorf("草稿包含不允许的特殊文件 %q", cleaned)
		}
	}
}

func (s *Server) existingPluginSource(name string) (exists bool, ver string, source string, protected bool) {
	if name == hostCommandsPlugin {
		return true, "", "builtin", true
	}
	target := filepath.Join(s.hostPluginDir(), name)
	if _, err := os.Lstat(target); err != nil {
		return false, "", "", false
	}
	if manifest, err := hostbridge.LoadManifest(target); err == nil {
		ver = manifest.Version
	}
	if _, isDefault := hostbridge.DefaultPluginByName(name); isDefault {
		return true, ver, "default", false
	}
	return true, ver, "installed", false
}

func (s *Server) getHostPluginDrafts(w http.ResponseWriter, r *http.Request) {
	if s.clientOnly {
		jsonReply(w, pluginDraftsListView{Ready: false, Drafts: []pluginDraftView{}})
		return
	}
	subs, ready, err := s.listSubmittedDrafts(r.Context())
	if err != nil {
		diagnostics.Log("warn", "host-tools", fmt.Sprintf("list drafts error=%v", err))
		jsonReply(w, pluginDraftsListView{Ready: ready, Drafts: []pluginDraftView{}})
		return
	}
	views := make([]pluginDraftView, 0, len(subs))
	for _, sub := range subs {
		if !hostbridge.ValidPluginName(sub.Name) {
			continue
		}
		exists, existingVer, existingSource, protected := s.existingPluginSource(sub.Name)
		// Everything a decision rests on comes from this computer's copy of
		// the files. The submission record is written inside the environment
		// and only supplies the notes shown beside them.
		manifest, files, digest, err := s.snapshotDraft(r.Context(), sub.Name)
		if err != nil {
			diagnostics.Log("warn", "host-tools", fmt.Sprintf("snapshot draft %s error=%v", sub.Name, err))
			views = append(views, pluginDraftView{
				ID: sub.Name, Name: sub.Name, Target: hostbridge.DraftTargetWorkspace, RecommendedKind: hostbridge.KindGuest,
				Permissions: []string{}, Files: []pluginDraftFileInfo{},
				Warnings: []string{"无法读取或校验这个草稿，暂时不能安装：" + err.Error()},
				Exists:   exists, ExistingVersion: existingVer, ExistingSource: existingSource, Protected: protected,
			})
			continue
		}
		target, recommended := hostbridge.DraftTargetWorkspace, hostbridge.KindGuest
		if manifest.Kind == hostbridge.KindHost {
			target, recommended = hostbridge.DraftTargetLocal, hostbridge.KindHost
		}
		perms := manifest.Permissions
		if perms == nil {
			perms = []string{}
		}
		views = append(views, pluginDraftView{
			ID:              sub.Name,
			Name:            sub.Name,
			DisplayName:     manifest.DisplayName,
			Version:         manifest.Version,
			Description:     manifest.Description,
			Summary:         sub.Summary,
			Target:          target,
			RecommendedKind: recommended,
			Platforms:       manifest.Platforms,
			Permissions:     perms,
			Categories:      hostbridge.ShownCategories(manifest.Categories),
			Contributes:     hostContributes(manifest.Contributes),
			SubmittedAt:     sub.SubmittedAt,
			Tools:           sub.Tools,
			Warnings:        sub.Warnings,
			Files:           files,
			Digest:          digest,
			Exists:          exists,
			ExistingVersion: existingVer,
			ExistingSource:  existingSource,
			Protected:       protected,
		})
	}
	jsonReply(w, pluginDraftsListView{Ready: ready, Drafts: views})
}

func (s *Server) getHostPluginDraftFile(w http.ResponseWriter, r *http.Request) {
	if !s.hasPluginControlView(r) {
		http.Error(w, "请在受信任的 Cage 窗口查看插件草稿", http.StatusForbidden)
		return
	}
	if s.clientOnly {
		http.Error(w, "此环境不支持插件草稿", http.StatusConflict)
		return
	}
	id := r.URL.Query().Get("id")
	rel := r.URL.Query().Get("path")
	data, err := s.readReviewedDraftFile(id, rel)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		requestError(w, err)
		return
	}
	isText := !bytes.ContainsRune(data, 0) && utf8.Valid(data)
	reply := struct {
		ID               string `json:"id"`
		Path             string `json:"path"`
		Text             bool   `json:"text"`
		Content          string `json:"content,omitempty"`
		InstalledExists  bool   `json:"installed_exists,omitempty"`
		InstalledContent string `json:"installed_content,omitempty"`
		Changed          bool   `json:"changed"`
	}{
		ID:      id,
		Path:    rel,
		Text:    isText,
		Changed: true,
	}
	if isText {
		reply.Content = string(data)
	}
	installedRel := rel
	if rel == hostbridge.DraftManifestFile {
		installedRel = hostbridge.ManifestFile
	}
	installedPath := filepath.Join(s.hostPluginDir(), id, filepath.FromSlash(installedRel))
	if info, err := os.Lstat(installedPath); err == nil && info.Mode().IsRegular() && info.Size() <= draftFileViewLimit {
		if oldData, err := os.ReadFile(installedPath); err == nil {
			reply.InstalledExists = true
			if !bytes.ContainsRune(oldData, 0) && utf8.Valid(oldData) {
				reply.InstalledContent = string(oldData)
				reply.Changed = reply.InstalledContent != reply.Content
			}
		}
	}
	jsonReply(w, reply)
}

func (s *Server) installHostPluginDraft(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID               string `json:"id"`
		Kind             string `json:"kind"`
		Replace          bool   `json:"replace"`
		ConfirmOverwrite bool   `json:"confirm_overwrite"`
		Digest           string `json:"digest"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !s.hasPluginControlView(r) {
		http.Error(w, "请在受信任的 Cage 窗口安装插件草稿", http.StatusForbidden)
		return
	}
	if s.clientOnly {
		http.Error(w, "此环境不支持插件安装", http.StatusConflict)
		return
	}
	if !s.op.TryLock() {
		http.Error(w, "环境正在执行其他操作，请稍后再试", http.StatusConflict)
		return
	}
	defer s.op.Unlock()

	if !hostbridge.ValidPluginName(body.ID) {
		requestError(w, errors.New("插件名称无效"))
		return
	}
	if body.Kind != "" && body.Kind != hostbridge.KindGuest && body.Kind != hostbridge.KindHost {
		requestError(w, errors.New("运行位置必须为 guest 或 host"))
		return
	}
	exists, _, existingSource, protected := s.existingPluginSource(body.ID)
	if protected {
		requestError(w, errors.New("内置核心插件不允许被草稿覆盖"))
		return
	}
	if exists && !body.Replace {
		requestError(w, errors.New("同名插件已安装，请确认覆盖后再安装"))
		return
	}
	if exists && existingSource == "default" && !body.ConfirmOverwrite {
		requestError(w, errors.New("覆盖默认预装插件需要二次确认"))
		return
	}
	// Check if a market plugin shares this name and requires explicit overwrite confirmation.
	if exists && !body.ConfirmOverwrite {
		entries, _, _ := s.hostMarketEntries(r.Context(), false)
		if slices.ContainsFunc(entries, func(e hostbridge.MarketEntry) bool { return e.Name == body.ID }) {
			requestError(w, errors.New("覆盖来自插件市场的同名插件需要二次确认"))
			return
		}
	}

	pluginDir := s.hostPluginDir()
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		requestError(w, err)
		return
	}
	staging, err := os.MkdirTemp(pluginDir, ".installing-draft-*")
	if err != nil {
		requestError(w, err)
		return
	}
	defer os.RemoveAll(staging)

	// Install the copy the user was shown, never a fresh read: the
	// environment may have rewritten its folder since.
	reviewed, err := s.reviewedDraftDir(body.ID, body.Digest)
	if err != nil {
		requestError(w, err)
		return
	}
	if err := copyPlainTree(reviewed, staging); err != nil {
		requestError(w, errors.New("读取草稿失败："+err.Error()))
		return
	}
	if _, copied, err := draftTree(staging); err != nil || copied != body.Digest {
		requestError(w, errors.New("草稿在审核后发生了变化，请刷新待审核草稿并重新查看"))
		return
	}
	_, manifestBytes, err := hostbridge.ValidateDraftDir(staging, body.ID, body.Kind, "")
	if err != nil {
		requestError(w, errors.New("草稿校验失败："+err.Error()))
		return
	}
	if err := os.WriteFile(filepath.Join(staging, hostbridge.ManifestFile), manifestBytes, 0o644); err != nil {
		requestError(w, err)
		return
	}
	// Remove the neutral draft manifest so the installed folder matches the
	// standard plugin layout cleanly.
	_ = os.Remove(filepath.Join(staging, hostbridge.DraftManifestFile))

	// Run a final integrity pass with ArchivePlugin (re-checks manifest, size,
	// file count, and absence of symlinks).
	if _, _, err := hostbridge.ArchivePlugin(staging, body.ID); err != nil {
		requestError(w, errors.New("草稿打包校验失败："+err.Error()))
		return
	}

	target := filepath.Join(pluginDir, body.ID)
	aside := ""
	if exists {
		s.closeHostTools()
		if aside, err = s.setAsideHostPlugin(body.ID); err != nil {
			requestError(w, err)
			return
		}
	}
	if err := os.Rename(staging, target); err != nil {
		if aside != "" {
			_ = os.Rename(aside, target)
		}
		requestError(w, err)
		return
	}

	// Security invariant: installing or replacing a plugin from a draft ALWAYS
	// revokes any previous "always allow" grant for that plugin name, even when
	// declared permissions did not change.
	_ = s.hostGrantStore().revokePlugin(body.ID)
	// What the user entered for the plugin it replaces, secrets included, was
	// entered for that program and is not handed to this one.
	if exists {
		s.forgetPluginSettings(body.ID)
	}
	_ = s.recordHostPluginOn(body.ID, true)
	_ = os.RemoveAll(filepath.Join(s.draftReviewDir(), body.ID))
	if err := s.discardSubmittedDraft(r.Context(), body.ID); err != nil {
		diagnostics.Log("warn", "host-tools", fmt.Sprintf("discard installed draft %s error=%v", body.ID, err))
	}

	s.closeHostTools()
	s.mu.Lock()
	ready := s.ready
	s.mu.Unlock()
	if ready || !s.hostToolsView().Enabled {
		s.ensureHostTools()
	}
	jsonReply(w, s.hostToolsView())
}

func (s *Server) discardHostPluginDraft(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !s.hasPluginControlView(r) {
		http.Error(w, "请在受信任的 Cage 窗口管理插件草稿", http.StatusForbidden)
		return
	}
	if s.clientOnly {
		http.Error(w, "此环境不支持插件草稿", http.StatusConflict)
		return
	}
	if err := s.discardSubmittedDraft(r.Context(), body.ID); err != nil {
		requestError(w, err)
		return
	}
	jsonReply(w, map[string]any{"discarded": body.ID})
}
