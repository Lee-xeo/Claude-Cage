package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
)

func (s *Server) removeProject(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Workspace string `json:"workspace"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	if !s.op.TryLock() {
		http.Error(w, "另一个操作仍在进行，请稍候", http.StatusConflict)
		return
	}
	defer s.op.Unlock()
	s.mu.Lock()
	closing, ready, preferences := s.closing, s.ready, s.preferences
	s.mu.Unlock()
	if closing || s.busy() {
		http.Error(w, "环境正在处理其他操作，请稍后重试", http.StatusConflict)
		return
	}
	path := input.Workspace
	if path == "" || isClientMountPath(path) {
		http.Error(w, "请选择已添加的项目；客户端共享请使用停止共享。", http.StatusBadRequest)
		return
	}
	projects, err := s.projectPaths()
	if err != nil {
		requestError(w, err)
		return
	}
	if !slices.Contains(projects, path) {
		jsonReply(w, map[string]string{"workspace": s.workspace()})
		return
	}
	if !ready {
		running, err := controllerVMState(s.profile)
		if err != nil || running {
			http.Error(w, "环境仍在运行或运行状态无法确认。请恢复连接，或保存工作并停止环境后再移除项目。", http.StatusConflict)
			return
		}
	}
	folders, err := s.projectFolders(path)
	if err != nil {
		requestError(w, err)
		return
	}
	if err := s.checkProjectFoldersIdle(r.Context(), folders, ready); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	var saved sharedProjects
	filename := filepath.Join(s.profile, "projects.json")
	data, err := os.ReadFile(filename)
	if err == nil {
		err = json.Unmarshal(data, &saved)
	}
	if err != nil && !os.IsNotExist(err) {
		requestError(w, err)
		return
	}
	if slices.Contains(folders, preferences.Workspace) {
		// Preserve a legacy current grant before changing the selection. If a
		// later write fails, a retry can still find and remove this project.
		if !slices.Contains(saved.Paths, preferences.Workspace) {
			saved.Paths = append(saved.Paths, preferences.Workspace)
			if err = writePrivate(filename, saved); err != nil {
				requestError(w, err)
				return
			}
		}
		preferences.Workspace = ""
		for _, project := range projects {
			if !slices.Contains(folders, project) && !isClientMountPath(project) {
				preferences.Workspace = project
				break
			}
		}
		if err = writePrivate(filepath.Join(s.profile, "workbench.json"), preferences); err != nil {
			requestError(w, err)
			return
		}
		s.mu.Lock()
		s.preferences.Workspace = preferences.Workspace
		s.mu.Unlock()
	}
	deleteProject := func(value string) bool { return slices.Contains(folders, value) }
	saved.Paths = slices.DeleteFunc(saved.Paths, deleteProject)
	saved.Groups = slices.DeleteFunc(saved.Groups, func(g projectGroup) bool { return slices.Contains(folders, g.Workspace) })
	if err = writePrivate(filename, saved); err != nil {
		requestError(w, err)
		return
	}
	s.detachProjectFolders(folders)
	jsonReply(w, map[string]string{"workspace": s.workspace()})
}

func (s *Server) checkProjectFoldersIdle(ctx context.Context, folders []string, ready bool) error {
	if len(folders) == 0 {
		return nil
	}
	s.mu.Lock()
	preferences := s.preferences
	s.mu.Unlock()
	projects, err := s.projectPaths()
	if err != nil {
		return err
	}
	for _, terminal := range s.terminals.snapshot() {
		if slices.Contains(folders, terminal["workspace"].(string)) && terminal["closed"] != true {
			return errors.New("请先关闭此项目的终端，再移除项目。会话历史会保留。")
		}
	}
	if ready && sharedCLILauncher(claudeTerminalCommand(preferences)) {
		granted := make(map[string]bool, len(projects))
		for _, project := range projects {
			granted[project] = true
		}
		_, err = s.mobileCLI.summaries(s.profile, granted, false)
		if err == nil {
			err = s.discoverTerminalConversations(ctx, projects)
		}
		if err != nil {
			return errors.New("暂时无法核对运行中的会话。请恢复会话连接，或保存工作并停止环境后再移除项目。")
		}
	}
	s.mobileCLI.mu.Lock()
	active := false
	for _, run := range s.mobileCLI.active {
		active = active || slices.Contains(folders, run.record.Workspace)
	}
	for id, running := range s.mobileCLI.running {
		active = active || ready && running && slices.Contains(folders, s.mobileCLI.catalog[id].Workspace)
	}
	s.mobileCLI.mu.Unlock()
	if active {
		return errors.New("请先结束此项目正在运行的会话，再移除项目。会话历史会保留。")
	}
	if ready {
		body, err := s.browserAction(ctx, "tabs", nil)
		var browser struct {
			Tabs []struct {
				Kind      string `json:"kind"`
				Workspace string `json:"workspace"`
			} `json:"tabs"`
		}
		if err != nil || json.Unmarshal(body, &browser) != nil || browser.Tabs == nil {
			return errors.New("暂时无法核对文件编辑器。请恢复浏览器连接，或保存工作并停止环境后再移除项目。")
		}
		for _, tab := range browser.Tabs {
			if tab.Kind == "editor" && slices.Contains(folders, tab.Workspace) {
				return errors.New("请先保存文件并关闭此项目的编辑器，再移除项目。")
			}
		}
	}
	return nil
}

func (s *Server) detachProjectFolders(folders []string) {
	for _, folder := range folders {
		s.mu.Lock()
		share := s.projectShares[folder]
		delete(s.projectShares, folder)
		s.mu.Unlock()
		if share != nil {
			share.close()
		}
		s.setProjectError(folder, nil)
	}
}

func (s *Server) replaceRemovedWorkspace(removed []string, replacement string) error {
	s.mu.Lock()
	preferences := s.preferences
	s.mu.Unlock()
	if !slices.Contains(removed, preferences.Workspace) {
		return nil
	}
	preferences.Workspace = replacement
	if err := writePrivate(filepath.Join(s.profile, "workbench.json"), preferences); err != nil {
		return err
	}
	s.mu.Lock()
	s.preferences.Workspace = replacement
	s.mu.Unlock()
	return nil
}
