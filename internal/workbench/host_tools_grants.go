package workbench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// hostGrants remembers what the user allowed for good: a plugin's tools, or
// commands in a project. A grant lasts until the user takes it back, the
// plugin is removed, or the plugin comes to declare a permission it did not
// have when the user agreed.
type hostGrants struct {
	path string
	mu   sync.Mutex
	// loaded is false until the file has been read.
	loaded bool
	grants map[string]hostGrant
}

type hostGrant struct {
	// Permissions are those the plugin declared when the user agreed.
	Permissions []string  `json:"permissions,omitempty"`
	Granted     time.Time `json:"granted"`
}

const (
	hostGrantPlugin  = "plugin:"
	hostGrantProject = "project:"
)

func (g *hostGrants) load() {
	if g.loaded {
		return
	}
	g.loaded, g.grants = true, map[string]hostGrant{}
	data, err := os.ReadFile(g.path)
	if err != nil {
		return
	}
	var file struct {
		Grants map[string]hostGrant `json:"grants"`
	}
	// A file that cannot be read grants nothing.
	if json.Unmarshal(data, &file) == nil && file.Grants != nil {
		g.grants = file.Grants
	}
}

func (g *hostGrants) save() error {
	return writePrivate(g.path, struct {
		Version int                  `json:"version"`
		Grants  map[string]hostGrant `json:"grants"`
	}{1, g.grants})
}

// allowed reports whether key was granted and still covers permissions.
func (g *hostGrants) allowed(key string, permissions []string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.load()
	grant, found := g.grants[key]
	if !found {
		return false
	}
	for _, permission := range permissions {
		if !slices.Contains(grant.Permissions, permission) {
			return false
		}
	}
	return true
}

func (g *hostGrants) grant(key string, permissions []string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.load()
	g.grants[key] = hostGrant{Permissions: slices.Clone(permissions), Granted: time.Now().UTC().Truncate(time.Second)}
	return g.save()
}

// revoke takes back the grant for key.
func (g *hostGrants) revoke(key string) error {
	return g.remove(func(name string) bool { return name == key })
}

// revokeProjects takes back every project's grant for commands.
func (g *hostGrants) revokeProjects() error {
	return g.remove(func(name string) bool { return strings.HasPrefix(name, hostGrantProject) })
}

func (g *hostGrants) remove(match func(string) bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.load()
	changed := false
	for name := range g.grants {
		if match(name) {
			delete(g.grants, name)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return g.save()
}

// projects are the folders in which commands are always allowed.
func (g *hostGrants) projects() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.load()
	projects := []string{}
	for name := range g.grants {
		if project, found := strings.CutPrefix(name, hostGrantProject); found {
			projects = append(projects, project)
		}
	}
	sort.Strings(projects)
	return projects
}

func (s *Server) hostGrantStore() *hostGrants {
	s.hostGrantsOnce.Do(func() { s.hostGrants = &hostGrants{path: filepath.Join(s.profile, "host-grants.json")} })
	return s.hostGrants
}
