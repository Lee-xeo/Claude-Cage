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
	if windowsPathWithin(dir, excluded) {
		return errors.New("inside AppData or Temp")
	}
	return nil
}

// windowsPathWithin reports whether dir is one of roots or below one,
// comparing as Windows does: case-insensitively, ignoring trailing dots and
// spaces of each component.
func windowsPathWithin(dir string, roots []string) bool {
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
	for _, root := range roots {
		if root == "" {
			continue
		}
		r := normalize(root)
		if d == r || strings.HasPrefix(d, r+`\`) {
			return true
		}
	}
	return false
}

// chooseWindowsSocketDir returns the first usable spelling. Each candidate is
// a long path and, when it exists, the ASCII alias Windows gives it; an alias
// must not escape the exclusion its long path is checked against.
func chooseWindowsSocketDir(candidates []string, room int, excluded []string, ensure func(string) error, shortName func(string) (string, error)) (string, error) {
	var reasons []string
	for _, dir := range candidates {
		problem := windowsSocketDirProblem(dir, room, excluded)
		if problem == nil {
			if err := ensure(dir); err != nil {
				reasons = append(reasons, err.Error())
				continue
			}
			return dir, nil
		}
		if strings.HasPrefix(dir, `\\`) || windowsPathWithin(dir, excluded) {
			reasons = append(reasons, problem.Error())
			continue
		}
		if err := ensure(dir); err != nil {
			reasons = append(reasons, err.Error())
			continue
		}
		short, err := shortName(dir)
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
