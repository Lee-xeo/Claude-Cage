package workbench

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Set CAGE_TEST_LOCAL_CODEX to exercise the installed CLI and model.
func TestLocalCodexConversation(t *testing.T) {
	if os.Getenv("CAGE_TEST_LOCAL_CODEX") != "1" {
		t.Skip("requires installed Codex and a signed-in account")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	want := hex.EncodeToString(nonce[:])
	if err := os.WriteFile(filepath.Join(root, "marker.txt"), []byte(want), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	session, err := startCodexConversation(ctx, localCodex(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close()
	first, err := session.ask("Read marker.txt in the assigned project. Reply with only its contents.")
	if err != nil || strings.TrimSpace(first) != want {
		t.Fatalf("first answer matched=%t, error=%v", first == want, err)
	}
	second, err := session.ask("Without reading any files again, reply with only the last four characters of your previous answer.")
	if err != nil || strings.TrimSpace(second) != want[len(want)-4:] {
		t.Fatalf("follow-up matched=%t, error=%v", second == want[len(want)-4:], err)
	}
}
