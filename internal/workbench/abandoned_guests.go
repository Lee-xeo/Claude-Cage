package workbench

import (
	"path/filepath"
	"slices"

	"github.com/sylvinus/agent-vm/internal/guestos"
	"github.com/sylvinus/agent-vm/internal/hostimage"
)

// abandonedGuestData lists, per guest system Cage no longer runs, the profile
// entries such a guest left behind. Entries are glob patterns relative to the
// profile directory.
var abandonedGuestData = []struct {
	system   string
	patterns []string
}{
	{"macos", []string{"macos-guest.json", filepath.Join("state", "lima", "mac"), filepath.Join("state", "guest-media", "macos")}},
	{"windows", []string{"windows-guest.json", filepath.Join("state", "windows-*")}},
	{"ubuntu", []string{filepath.Join("state", "guest-media", "ubuntu-*")}},
}

// abandonedGuestName maps a guest system ID to its name in abandoned_guests.
func abandonedGuestName(system string) string {
	switch {
	case system == guestos.MacOS:
		return "macos"
	case system == guestos.Windows11:
		return "windows"
	case hostimage.AbandonedLinux(system):
		return "ubuntu"
	}
	return ""
}

// abandonedGuestSystem reports whether a requested guest system is one Cage
// no longer installs or starts.
func abandonedGuestSystem(system string) bool {
	return abandonedGuestName(system) != ""
}

// abandonedGuests reports which no longer supported guest systems still have
// data in the profile. It only checks existence: the files stay where they
// are so the user can export or remove them deliberately.
func abandonedGuests(profile string) []string {
	found := []string{}
	for _, guest := range abandonedGuestData {
		for _, pattern := range guest.patterns {
			if matches, _ := filepath.Glob(filepath.Join(profile, pattern)); len(matches) != 0 {
				found = append(found, guest.system)
				break
			}
		}
	}
	return found
}

// abandonedGuestList also names the system of an installation that an earlier
// version recorded but can no longer continue, and of an existing environment
// that was created from a system Cage no longer runs.
func (s *Server) abandonedGuestList() []string {
	found := abandonedGuests(s.profile)
	systems := []string{hostimage.AbandonedEnvironment(filepath.Join(s.profile, "state"))}
	if pending, err := guestos.LoadInstallation(s.profile); err == nil && pending != nil {
		systems = append(systems, pending.System)
	}
	for _, system := range systems {
		if name := abandonedGuestName(system); name != "" && !slices.Contains(found, name) {
			found = append(found, name)
		}
	}
	slices.Sort(found)
	return found
}

func abandonedGuestMessage(system string) string {
	name := "所选"
	switch system {
	case "macos":
		name = "macOS"
	case "windows", "windows-11":
		name = "Windows"
	case "ubuntu", "ubuntu-22.04", "ubuntu-24.04":
		name = "Ubuntu"
	}
	return "Cage 已不再支持 " + name + " 客机，现在只提供 Debian 环境；原有客机数据仍保留在数据目录中，未被改动。"
}
