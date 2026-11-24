package host

import (
	"errors"
	"strings"
	"testing"
)

func TestWindowsSocketDirProblem(t *testing.T) {
	excluded := []string{`C:\Users\dev\AppData\Local`, `C:\Users\dev\AppData\Roaming`, `C:\Users\dev\AppData\Local\Temp`}
	for dir, ok := range map[string]bool{
		`C:\Users\dev\.claude-cage\run`:         true,
		`d:\cage\run`:                           true,
		`C:\Users\鲸落\.claude-cage\run`:          false,
		`\\server\share\run`:                    false,
		`run`:                                   false,
		`C:\Users\dev\AppData\Local\Temp\x`:     false,
		`c:\users\DEV\appdata\local. \cage`:     false,
		`C:\Users\dev\AppDataX\run`:             true,
		`C:\` + strings.Repeat("a", 100):        false,
		`C:\` + strings.Repeat("a", 107-3-48):   true,
		`C:\` + strings.Repeat("a", 107-3-48+1): false,
	} {
		if got := windowsSocketDirProblem(dir, 48, excluded) == nil; got != ok {
			t.Errorf("%q: usable %v, want %v", dir, got, ok)
		}
	}
}

// fakeSocketFS is a set of existing directories, volumes without ACLs and
// 8.3 aliases.
type fakeSocketFS struct {
	existing map[string]bool
	noACLs   []string
	short    map[string]string
	made     []string
}

func (f *fakeSocketFS) fs() windowsSocketFS {
	return windowsSocketFS{
		persistentACLs: func(dir string) bool { return !WindowsPathWithin(dir, f.noACLs) },
		exists:         func(dir string) bool { return f.existing[dir] },
		mkdir: func(dir string) error {
			f.made = append(f.made, dir)
			f.existing[dir] = true
			return nil
		},
		shortName: func(dir string) (string, error) {
			if s, ok := f.short[dir]; ok && f.existing[dir] {
				return s, nil
			}
			return "", errors.New("8.3 names disabled")
		},
	}
}

func TestChooseWindowsSocketDir(t *testing.T) {
	excluded := []string{`C:\Users\鲸落\AppData\Local`}
	f := &fakeSocketFS{existing: map[string]bool{}, short: map[string]string{`C:\Users\鲸落\.claude-cage\run`: `C:\Users\9B9E~1\.CLAUD~1\run`}}
	candidates := WindowsSocketDirCandidates(`C:\Users\鲸落\.claude-cage`, `C:\Users\鲸落`, "abc")
	if candidates[0] != `C:\Users\鲸落\.claude-cage\run` || candidates[1] != `C:\Users\鲸落\.cage-run\abc` {
		t.Fatalf("candidates: %q", candidates)
	}
	// A lookup before the core made the directory does not settle on
	// another candidate.
	if _, err := chooseWindowsSocketDir(candidates, 48, excluded, f.fs(), false); !errors.Is(err, errSocketDirNotCreated) {
		t.Fatalf("lookup before creation: %v", err)
	}
	if got, err := chooseWindowsSocketDir(candidates, 48, excluded, f.fs(), true); err != nil || got != `C:\Users\9B9E~1\.CLAUD~1\run` {
		t.Fatalf("non-ASCII profile: %q, %v", got, err)
	}
	if got, err := chooseWindowsSocketDir(candidates, 48, excluded, f.fs(), false); err != nil || got != `C:\Users\9B9E~1\.CLAUD~1\run` {
		t.Fatalf("lookup after creation: %q, %v", got, err)
	}
	// A profile in AppData is never used, not even through an alias.
	appData := WindowsSocketDirCandidates(`C:\Users\鲸落\AppData\Local\cage`, `C:\Users\鲸落`, "abc")
	f.short[`C:\Users\鲸落\AppData\Local\cage\run`] = `C:\Users\9B9E~1\AppData\Local\cage\run`
	f.short[`C:\Users\鲸落\.cage-run\abc`] = `C:\Users\9B9E~1\.cage-run\abc`
	if got, err := chooseWindowsSocketDir(appData, 48, excluded, f.fs(), true); err != nil || got != `C:\Users\9B9E~1\.cage-run\abc` {
		t.Fatalf("AppData profile: %q, %v", got, err)
	}
	delete(f.short, `C:\Users\鲸落\.cage-run\abc`)
	delete(f.short, `C:\Users\鲸落\.claude-cage\run`)
	if _, err := chooseWindowsSocketDir(candidates, 48, excluded, f.fs(), true); !errors.Is(err, errSocketDirUnusable) {
		t.Fatalf("no ASCII spelling must fail clearly: %v", err)
	}
	f.made = nil
	if got, err := chooseWindowsSocketDir([]string{`C:\Users\dev\.claude-cage\run`}, 48, excluded, f.fs(), true); err != nil || got != `C:\Users\dev\.claude-cage\run` || len(f.made) != 0 {
		t.Fatalf("ASCII profile: %q, %v, made %q", got, err, f.made)
	}
}

// A profile on exFAT: the run directory there can be
// created but not made private. Whether the directory already exists, and
// whether the process creates or only looks up, every process must pick the
// same directory, or the window shows the core as stopped and restarts it.
func TestWindowsSocketDirIsTheSameForEveryProcess(t *testing.T) {
	excluded := []string{`C:\Users\dev\AppData\Local`}
	candidates := WindowsSocketDirCandidates(`E:\cage-profile`, `C:\Users\dev`, "abc")
	for _, existing := range []map[string]bool{
		{},
		{`E:\cage-profile\run`: true},
		{`E:\cage-profile\run`: true, `C:\Users\dev\.cage-run\abc`: true},
		{`C:\Users\dev\.cage-run\abc`: true},
	} {
		f := &fakeSocketFS{existing: existing, noACLs: []string{`E:\`}, short: map[string]string{}}
		created, err := chooseWindowsSocketDir(candidates, 48, excluded, f.fs(), true)
		if err != nil || created != `C:\Users\dev\.cage-run\abc` {
			t.Fatalf("existing %v: core chose %q, %v", existing, created, err)
		}
		looked, err := chooseWindowsSocketDir(candidates, 48, excluded, f.fs(), false)
		if err != nil || looked != created {
			t.Fatalf("existing %v: lookup chose %q (%v), core %q", existing, looked, err, created)
		}
	}
	// On NTFS the profile's own directory is used by all, existing or not.
	f := &fakeSocketFS{existing: map[string]bool{`C:\Users\dev\.cage-run\abc`: true}, short: map[string]string{}}
	ntfs := WindowsSocketDirCandidates(`D:\cage-profile`, `C:\Users\dev`, "abc")
	for _, create := range []bool{true, false} {
		if got, err := chooseWindowsSocketDir(ntfs, 48, excluded, f.fs(), create); err != nil || got != `D:\cage-profile\run` {
			t.Fatalf("create=%v: %q, %v", create, got, err)
		}
	}
}
