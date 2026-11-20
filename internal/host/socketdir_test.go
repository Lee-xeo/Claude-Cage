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

func TestChooseWindowsSocketDir(t *testing.T) {
	excluded := []string{`C:\Users\鲸落\AppData\Local`}
	ensured := []string{}
	ensure := func(dir string) error { ensured = append(ensured, dir); return nil }
	short := map[string]string{`C:\Users\鲸落\.claude-cage\run`: `C:\Users\9B9E~1\.CLAUD~1\run`}
	shortName := func(dir string) (string, error) {
		if s, ok := short[dir]; ok {
			return s, nil
		}
		return "", errors.New("8.3 names disabled")
	}
	candidates := WindowsSocketDirCandidates(`C:\Users\鲸落\.claude-cage`, `C:\Users\鲸落`, "abc")
	if candidates[0] != `C:\Users\鲸落\.claude-cage\run` || candidates[1] != `C:\Users\鲸落\.cage-run\abc` {
		t.Fatalf("candidates: %q", candidates)
	}
	if got, err := chooseWindowsSocketDir(candidates, 48, excluded, ensure, shortName); err != nil || got != `C:\Users\9B9E~1\.CLAUD~1\run` {
		t.Fatalf("non-ASCII profile: %q, %v", got, err)
	}
	// A profile in AppData is never used, not even through an alias.
	appData := WindowsSocketDirCandidates(`C:\Users\鲸落\AppData\Local\cage`, `C:\Users\鲸落`, "abc")
	short[`C:\Users\鲸落\AppData\Local\cage\run`] = `C:\Users\9B9E~1\AppData\Local\cage\run`
	short[`C:\Users\鲸落\.cage-run\abc`] = `C:\Users\9B9E~1\.cage-run\abc`
	if got, err := chooseWindowsSocketDir(appData, 48, excluded, ensure, shortName); err != nil || got != `C:\Users\9B9E~1\.cage-run\abc` {
		t.Fatalf("AppData profile: %q, %v", got, err)
	}
	delete(short, `C:\Users\鲸落\.cage-run\abc`)
	delete(short, `C:\Users\鲸落\.claude-cage\run`)
	if _, err := chooseWindowsSocketDir(candidates, 48, excluded, ensure, shortName); !errors.Is(err, errSocketDirUnusable) {
		t.Fatalf("no ASCII spelling must fail clearly: %v", err)
	}
	ensured = nil
	if got, err := chooseWindowsSocketDir([]string{`C:\Users\dev\.claude-cage\run`}, 48, excluded, ensure, shortName); err != nil || got != `C:\Users\dev\.claude-cage\run` || len(ensured) != 1 {
		t.Fatalf("ASCII profile: %q, %v, %q", got, err, ensured)
	}
}
