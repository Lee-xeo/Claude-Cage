package workbench

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sylvinus/agent-vm/internal/guestos"
)

func groupFixture(t *testing.T) (*Server, string, string) {
	t.Helper()
	s := testServer(t)
	s.ready = true
	a, _ := filepath.EvalSymlinks(t.TempDir())
	b, _ := filepath.EvalSymlinks(t.TempDir())
	s.projectShares = map[string]*projectShare{a: {done: make(chan struct{})}, b: {done: make(chan struct{})}}
	return s, a, b
}

func saveGroupRequest(t *testing.T, s *Server, group projectGroup, code int) {
	t.Helper()
	data, _ := json.Marshal(group)
	w := request(s, "POST", "/api/projects/save", s.origin, string(data), true)
	if w.Code != code {
		t.Fatalf("save status=%d want=%d body=%s", w.Code, code, w.Body.String())
	}
}

func TestProjectGroupSaveReloadLegacyAndLaunchDirectories(t *testing.T) {
	s, a, b := groupFixture(t)
	s.preferences.Workspace = a // legacy selection absent from projects.json
	other := t.TempDir()
	if err := s.rememberSharedProject(other); err != nil {
		t.Fatal(err)
	}
	saveGroupRequest(t, s, projectGroup{Name: "课程系统", Folders: []string{a, b, b}}, 200)
	reloaded := testServer(t)
	reloaded.profile, reloaded.preferences = s.profile, s.preferences
	groups, err := reloaded.projectGroups()
	if err != nil || len(groups) != 2 {
		t.Fatalf("groups=%+v error=%v", groups, err)
	}
	group := groups[0]
	if group.Workspace != a || group.Name != "课程系统" || !slices.Equal(group.Folders, []string{a, b}) {
		t.Fatalf("group=%+v", group)
	}
	paths, err := reloaded.projectPaths()
	if err != nil || len(paths) != 3 || !slices.Contains(paths, other) {
		t.Fatal(paths, err)
	}
	for _, platform := range []string{"linux", "macos", "windows"} {
		t.Run(platform, func(t *testing.T) {
			s.macosGuest, s.windowsGuest = nil, nil
			expected := guestPath(b)
			if platform == "macos" {
				s.macosGuest = &guestos.MacOSProfile{User: "developer"}
				expected = macOSProjectPath("developer", b)
			}
			if platform == "windows" {
				s.windowsGuest = &guestos.WindowsProfile{}
				expected = `C:\Users\dev\Projects\backend`
				s.projectShares[b].windowsProject = &windowsProjectBinding{ID: "binding", Grant: "grant", Path: expected}
			}
			actual, err := s.projectAdditionalDirectories(a)
			if err != nil || !slices.Equal(actual, []string{expected}) {
				t.Fatalf("directories=%q err=%v", actual, err)
			}
		})
	}
}

func TestProjectGroupRejectsProtectedMemberBeforeOpeningAnyShare(t *testing.T) {
	s, a, _ := groupFixture(t)
	s.projectShares = nil
	saveGroupRequest(t, s, projectGroup{Name: "rejected", Folders: []string{a, s.profile}}, 400)
	if len(s.projectShares) != 0 {
		t.Fatal("opened a share before validating every directory")
	}
	if paths, _ := s.projectPaths(); len(paths) != 0 {
		t.Fatal("partial grant persisted", paths)
	}
}

func TestProjectGroupUnbindChecksDefaultSessionAndPreservesHistory(t *testing.T) {
	s, a, b := groupFixture(t)
	s.preferences.ClaudeTerminalCommand = "claude --continue" // no shared-conversation discovery needed for this fixture
	saveGroupRequest(t, s, projectGroup{Name: "app", Folders: []string{a, b}}, 200)
	s.terminals.sessions = map[string]*terminalSession{"live": {id: "live", workspace: a}}
	saveGroupRequest(t, s, projectGroup{Workspace: a, Name: "app", Folders: []string{a}}, 409)
	if paths, _ := s.projectPaths(); !slices.Contains(paths, b) {
		t.Fatal("revoked a directory used by a live default-folder session")
	}
	s.terminals.sessions = nil
	s.browserControl = browserClientForTest(t, s, browserTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"tabs":[]}`)), Header: http.Header{}}, nil
	}))
	done := make(chan struct{})
	close(done)
	detached := false
	s.projectShares[b] = &projectShare{done: done, detach: func() { detached = true }}
	file := filepath.Join(b, "keep.txt")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	s.preferences.Workspace = b
	saveGroupRequest(t, s, projectGroup{Workspace: a, Name: "renamed", Folders: []string{a}}, 200)
	if paths, _ := s.projectPaths(); slices.Contains(paths, b) || !slices.Contains(paths, a) {
		t.Fatal("unbound grant survived", paths)
	}
	if s.workspace() != a || !detached {
		t.Fatal("selection or transport not updated")
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "keep" {
		t.Fatal("host files changed")
	}
	saveGroupRequest(t, s, projectGroup{Workspace: a, Name: "bad", Folders: []string{b}}, 400)
}

func TestProjectGroupRemovalRevokesAllMembers(t *testing.T) {
	s, a, b := groupFixture(t)
	saveGroupRequest(t, s, projectGroup{Name: "app", Folders: []string{a, b}}, 200)
	s.ready = false
	s.preferences.Workspace = b
	for _, folder := range []string{a, b} {
		done := make(chan struct{})
		close(done)
		s.projectShares[folder] = &projectShare{done: done}
	}
	removeProjectRequest(t, s, a, 200)
	if paths, _ := s.projectPaths(); len(paths) != 0 {
		t.Fatal("member grant retained", paths)
	}
	if groups, _ := s.projectGroups(); len(groups) != 0 || len(s.projectShares) != 0 {
		t.Fatal("group or transport retained", groups)
	}
}

func TestProjectGroupsCannotTakeAnotherNamedProjectsFolders(t *testing.T) {
	s, a, b := groupFixture(t)
	saveGroupRequest(t, s, projectGroup{Name: "first", Folders: []string{a}}, 200)
	saveGroupRequest(t, s, projectGroup{Name: "second", Folders: []string{b, a}}, 400)
	groups, _ := s.projectGroups()
	if len(groups) != 1 || groups[0].Name != "first" {
		t.Fatal("failed save modified groups", groups)
	}
}
