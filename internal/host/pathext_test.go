package host

import "testing"

func TestNormalizePathExtKeepsExecutablesResolvable(t *testing.T) {
	for raw, want := range map[string]string{
		"":                                  ".COM;.EXE;.BAT;.CMD",
		".CPL":                              ".COM;.EXE;.BAT;.CMD;.CPL",
		".COM;.EXE;.BAT;.CMD;.VBS;.JS;.MSC": ".COM;.EXE;.BAT;.CMD;.VBS;.JS;.MSC",
		" .exe ; py ;;.Cpl":                 ".COM;.EXE;.BAT;.CMD;.py;.Cpl",
	} {
		if got := normalizePathExt(raw); got != want {
			t.Errorf("%q -> %q, want %q", raw, got, want)
		}
	}
}
