//go:build !windows

package agentcli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPendingInputNeverBlocks(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if pendingInput(r) {
		t.Fatal("an open, empty pipe has no input")
	}
	if _, err := w.Write([]byte(`{"username":"x"}`)); err != nil {
		t.Fatal(err)
	}
	if !pendingInput(r) {
		t.Fatal("written pipe data was not noticed")
	}
	empty, closed, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	closed.Close()
	w.Close()
	if pendingInput(empty) {
		t.Fatal("a closed, empty pipe has no input")
	}
	dir := t.TempDir()
	for name, content := range map[string]string{"empty": "", "full": "{}"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := pendingInput(f); got != (content != "") {
			t.Errorf("%s file: %v", name, got)
		}
		f.Close()
	}
}
