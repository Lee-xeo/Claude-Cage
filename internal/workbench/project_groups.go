package workbench

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// Workspace is the stable default directory and preserves transcript identity.
// Paths remain the individual host grants; groups only associate those grants.
type projectGroup struct {
	Workspace string   `json:"workspace"`
	Name      string   `json:"name"`
	Folders   []string `json:"folders"`
}

func (s *Server) readSharedProjects() (sharedProjects, error) {
	var saved sharedProjects
	data, err := os.ReadFile(filepath.Join(s.profile, "projects.json"))
	if os.IsNotExist(err) {
		return saved, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &saved)
	}
	return saved, err
}

func (s *Server) projectGroups() ([]projectGroup, error) {
	saved, err := s.readSharedProjects()
	if err != nil {
		return nil, err
	}
	paths, err := s.projectPaths()
	if err != nil {
		return nil, err
	}
	groups := []projectGroup{}
	covered := map[string]bool{}
	for _, group := range saved.Groups {
		if !slices.Contains(paths, group.Workspace) {
			continue
		}
		folders := []string{group.Workspace}
		for _, folder := range group.Folders {
			if slices.Contains(paths, folder) && !slices.Contains(folders, folder) {
				folders = append(folders, folder)
			}
		}
		group.Folders = folders
		groups = append(groups, group)
		for _, folder := range folders {
			covered[folder] = true
		}
	}
	for _, path := range paths {
		if !covered[path] {
			groups = append(groups, projectGroup{Workspace: path, Name: filepath.Base(path), Folders: []string{path}})
		}
	}
	return groups, nil
}

func (s *Server) projectFolders(workspace string) ([]string, error) {
	groups, err := s.projectGroups()
	if err != nil {
		return nil, err
	}
	for _, group := range groups {
		if slices.Contains(group.Folders, workspace) {
			return group.Folders, nil
		}
	}
	return []string{workspace}, nil
}

func (s *Server) projectAdditionalDirectories(workspace string) ([]string, error) {
	folders, err := s.projectFolders(workspace)
	if err != nil {
		return nil, err
	}
	var directories []string
	for _, folder := range folders {
		if folder == workspace {
			continue
		}
		if _, err := s.projectWorkspace(folder); err != nil {
			return nil, err
		}
		directory := guestPath(folder)
		directories = append(directories, directory)
	}
	return directories, nil
}

func (s *Server) projectConnectionError(w http.ResponseWriter, err error) {
	var failure *projectShareError
	if errors.As(err, &failure) && failure.Recovery != "" {
		w.Header().Set("X-Cage-Recovery", failure.Recovery)
	}
	requestError(w, err)
}

func (s *Server) saveProject(w http.ResponseWriter, r *http.Request) {
	var input projectGroup
	if !readJSON(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || utf8.RuneCountInString(input.Name) > 100 || strings.ContainsAny(input.Name, "\x00\r\n") || len(input.Folders) == 0 || len(input.Folders) > 32 {
		http.Error(w, "请填写项目名称，并添加 1–32 个文件夹", http.StatusBadRequest)
		return
	}
	if !s.lockProjectOperation(r.Context()) {
		http.Error(w, "另一个操作仍在进行，请稍候", http.StatusConflict)
		return
	}
	defer s.op.Unlock()
	s.mu.Lock()
	ready, closing := s.ready, s.closing
	s.mu.Unlock()
	if closing || !ready || s.busy() {
		http.Error(w, "环境尚未就绪或正在处理其他操作，请稍后重试", http.StatusConflict)
		return
	}
	saved, err := s.readSharedProjects()
	if err != nil {
		requestError(w, err)
		return
	}
	groups, err := s.projectGroups()
	if err != nil {
		requestError(w, err)
		return
	}
	// Validate every directory before opening any transport, including aliases.
	folders := []string{}
	for _, folder := range input.Folders {
		if isClientMountPath(folder) {
			http.Error(w, "临时客户端共享请单独管理", http.StatusBadRequest)
			return
		}
		path, err := validateWorkspace(folder, s.profile)
		if err != nil {
			requestError(w, err)
			return
		}
		if !slices.Contains(folders, path) {
			folders = append(folders, path)
		}
	}
	var previous *projectGroup
	if input.Workspace != "" {
		for _, group := range groups {
			if group.Workspace == input.Workspace {
				value := group
				previous = &value
				break
			}
		}
		if previous == nil || !slices.Contains(folders, input.Workspace) {
			http.Error(w, "请保留项目的默认文件夹，以继续使用已有会话历史", http.StatusBadRequest)
			return
		}
	} else {
		input.Workspace = folders[0]
		if slices.ContainsFunc(saved.Groups, func(g projectGroup) bool { return g.Workspace == input.Workspace }) {
			http.Error(w, "默认文件夹已属于现有项目，请选择编辑该项目", http.StatusBadRequest)
			return
		}
	}
	for _, group := range groups {
		if group.Workspace == input.Workspace {
			continue
		}
		for _, folder := range folders {
			// An existing single-folder project can join this group. Named or
			// multi-folder groups must be edited explicitly to avoid stealing them.
			if slices.Contains(group.Folders, folder) && slices.ContainsFunc(saved.Groups, func(g projectGroup) bool { return g.Workspace == group.Workspace }) {
				http.Error(w, "文件夹已绑定到项目“"+group.Name+"”，请先从该项目解绑", http.StatusBadRequest)
				return
			}
		}
	}
	removed := []string{}
	if previous != nil {
		for _, folder := range previous.Folders {
			if !slices.Contains(folders, folder) {
				removed = append(removed, folder)
			}
		}
	}
	var affected []string
	if len(removed) > 0 {
		affected = previous.Folders
	}
	if err := s.checkProjectFoldersIdle(r.Context(), affected, true); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	// Failed creation must not leave new, invisible directory transports alive.
	s.mu.Lock()
	initialShares := make(map[string]*projectShare, len(folders))
	for _, folder := range folders {
		initialShares[folder] = s.projectShares[folder]
	}
	s.mu.Unlock()
	committed := false
	defer func() {
		if committed {
			return
		}
		var created []string
		s.mu.Lock()
		for _, folder := range folders {
			if initialShares[folder] == nil && s.projectShares[folder] != nil {
				created = append(created, folder)
			}
		}
		s.mu.Unlock()
		s.detachProjectFolders(created)
	}()
	for _, folder := range folders {
		if err := s.ensureProjectShare(folder); err != nil {
			s.projectConnectionError(w, err)
			return
		}
	}
	input.Folders = append([]string{input.Workspace}, slices.DeleteFunc(slices.Clone(folders), func(p string) bool { return p == input.Workspace })...)
	// Preserve the legacy current grant even when it was never in paths.
	paths, err := s.projectPaths()
	if err != nil {
		requestError(w, err)
		return
	}
	saved.Paths = slices.DeleteFunc(paths, func(p string) bool { return isClientMountPath(p) || slices.Contains(removed, p) })
	for _, folder := range folders {
		if !slices.Contains(saved.Paths, folder) {
			saved.Paths = append(saved.Paths, folder)
		}
	}
	index := slices.IndexFunc(saved.Groups, func(g projectGroup) bool { return g.Workspace == input.Workspace })
	if index >= 0 {
		saved.Groups[index] = input
	} else {
		saved.Groups = append(saved.Groups, input)
	}
	// A removed member may be the legacy current selection. Change it before
	// dropping its grant so projectPaths cannot resurrect it.
	if err := s.replaceRemovedWorkspace(removed, input.Workspace); err != nil {
		requestError(w, err)
		return
	}
	if err := writePrivate(filepath.Join(s.profile, "projects.json"), saved); err != nil {
		requestError(w, err)
		return
	}
	committed = true
	s.detachProjectFolders(removed)
	jsonReply(w, input)
}
