package host

import (
	"errors"
	"strings"
)

// Windows' AF_UNIX sun_path holds 108 bytes including the terminating NUL.
const windowsSocketPathLimit = 107

var errSocketDirUnusable = errors.New("socket directory unusable")

// windowsSocketDirProblem reports why sockets with up to room more bytes
// cannot be created under dir on Windows. dir must be a local drive path in
// ASCII, short enough, and outside the excluded trees: AF_UNIX sockets under
// the user's AppData and Temp could not be connected or removed (#484).
func windowsSocketDirProblem(dir string, room int, excluded []string) error {
	if len(dir) < 3 || dir[1] != ':' || dir[2] != '\\' || !(dir[0] >= 'A' && dir[0] <= 'Z' || dir[0] >= 'a' && dir[0] <= 'z') {
		return errors.New("not a local drive path")
	}
	for i := 0; i < len(dir); i++ {
		if dir[i] < 0x20 || dir[i] >= 0x7f {
			return errors.New("not ASCII")
		}
	}
	if len(dir)+room > windowsSocketPathLimit {
		return errors.New("too long")
	}
	if WindowsPathWithin(dir, excluded) {
		return errors.New("inside AppData or Temp")
	}
	return nil
}

// WindowsPathWithin reports whether dir is one of roots or below one,
// comparing as Windows does: case-insensitively, ignoring trailing dots and
// spaces of each component.
func WindowsPathWithin(dir string, roots []string) bool {
	normalize := func(p string) string {
		parts := strings.Split(strings.ReplaceAll(p, "/", `\`), `\`)
		for i, part := range parts {
			if i > 0 {
				parts[i] = strings.TrimRight(part, ". ")
			}
		}
		return strings.ToLower(strings.TrimRight(strings.Join(parts, `\`), `\`))
	}
	d := normalize(dir)
	if d == "" {
		return false
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		r := normalize(root)
		if r == "" {
			continue
		}
		if d == r || strings.HasPrefix(d, r+`\`) {
			return true
		}
	}
	return false
}

// errSocketDirNotCreated is a lookup for a directory that is to be reached
// through its 8.3 alias but does not exist yet: the core has not created it.
var errSocketDirNotCreated = errors.New("socket directory not created yet")

// windowsSocketFS is what chooseWindowsSocketDir needs to know about the
// file systems holding the candidates.
type windowsSocketFS struct {
	// persistentACLs reports whether the volume holding a path keeps access
	// control lists, so a directory there can be made private (exFAT and
	// FAT32 cannot).
	persistentACLs func(string) bool
	exists         func(string) bool
	mkdir          func(string) error
	shortName      func(string) (string, error)
}

// chooseWindowsSocketDir picks the directory for a profile's sockets: the
// first candidate on a volume with persistent ACLs whose spelling, or the
// ASCII 8.3 alias Windows gives it, is usable. An alias must not escape the
// exclusion its long path is checked against.
//
// The choice depends only on the paths and on properties of the volumes
// holding them, never on whether making the directory private succeeded, so
// the process that creates the directory and the processes that only look
// it up always agree. Reading an alias needs the directory to exist: with
// create it is made first, without it a lookup reports errSocketDirNotCreated
// instead of moving on to a candidate the creating process would not use.
func chooseWindowsSocketDir(candidates []string, room int, excluded []string, fs windowsSocketFS, create bool) (string, error) {
	var reasons []string
	for _, dir := range candidates {
		problem := windowsSocketDirProblem(dir, room, excluded)
		if strings.HasPrefix(dir, `\\`) || WindowsPathWithin(dir, excluded) {
			reasons = append(reasons, problem.Error())
			continue
		}
		if !fs.persistentACLs(dir) {
			reasons = append(reasons, "volume keeps no access control lists")
			continue
		}
		if problem == nil {
			return dir, nil
		}
		if !fs.exists(dir) {
			if !create {
				return "", errSocketDirNotCreated
			}
			if err := fs.mkdir(dir); err != nil {
				return "", err
			}
		}
		short, err := fs.shortName(dir)
		if err == nil && windowsSocketDirProblem(short, room, excluded) == nil {
			return short, nil
		}
		reasons = append(reasons, problem.Error())
	}
	return "", errors.Join(append([]error{errSocketDirUnusable}, errors.New(strings.Join(reasons, "; ")))...)
}

// WindowsSocketDirCandidates lists, in order, the private directories that
// may hold a profile's Windows control sockets: one in the profile, then one
// per profile in the user's home for profiles in AppData, on a network share
// or on a long path.
func WindowsSocketDirCandidates(profile, home, id string) []string {
	join := func(parts ...string) string {
		for i := range parts {
			parts[i] = strings.TrimRight(parts[i], `\/`)
		}
		return strings.Join(parts, `\`)
	}
	candidates := []string{join(profile, "run")}
	if home != "" {
		candidates = append(candidates, join(home, ".cage-run", id))
	}
	return candidates
}
