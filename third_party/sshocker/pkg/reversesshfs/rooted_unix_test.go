//go:build linux || darwin

package reversesshfs

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Opening a FIFO must fail, not block the server: it handles one request at a time.
func TestRootedFifo(t *testing.T) {
	c, root := setupRooted(t, false)
	fifo := filepath.Join(root, "src", "fifo")
	if err := unix.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		if f, err := c.Open(fifo); err == nil {
			f.Close()
			done <- errors.New("Open (read) succeeded")
			return
		}
		if f, err := c.OpenFile(fifo, os.O_WRONLY); err == nil {
			f.Close()
			done <- errors.New("OpenFile (write) succeeded")
			return
		}
		if _, err := c.ReadDir(fifo); err == nil {
			done <- errors.New("ReadDir succeeded")
			return
		}
		_, err := c.Stat(filepath.Join(root, "src", "main.go"))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server blocked on a FIFO")
	}
}

func TestRenameNoReplace(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if err := renameNoReplace(fd, "a", fd, "b"); !errors.Is(err, unix.EEXIST) {
		t.Fatalf("over an existing file: %v", err)
	}
	if err := renameNoReplace(fd, "a", fd, "c"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "b")); err != nil || string(b) != "b" {
		t.Fatalf("b: %q, %v", b, err)
	}
}

// 客机看到的属主与宿主不同：把文件 chown 给呈现属主自身或 -1 时宿主属主不变并成功，其他 ID 仍报错。
func TestRootedChownPresentedOwner(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 可以 chown 给任何 ID，无法验证错误照旧返回")
	}
	c, root, h := setupRootedHandlers(t, false)
	uid, gid := uint32(os.Getuid()), uint32(os.Getgid())
	// 宿主普通用户无权把文件交给这组 ID：不跳过就会失败。
	owner := Owner{UID: uid + 1000, GID: gid + 1000}
	h.presentAs(owner)
	p := filepath.Join(root, "src", "main.go")
	hostOwner := func() (uint32, uint32) {
		t.Helper()
		fi, err := os.Lstat(p)
		if err != nil {
			t.Fatal(err)
		}
		st := fi.Sys().(*syscall.Stat_t)
		return st.Uid, st.Gid
	}
	assertHostOwner := func() {
		t.Helper()
		if u, g := hostOwner(); u != uid || g != gid {
			t.Fatalf("宿主属主变成了 %d:%d", u, g)
		}
	}
	for _, ids := range [][2]int{{int(owner.UID), int(owner.GID)}, {-1, -1}, {int(owner.UID), -1}, {-1, int(owner.GID)}} {
		if err := c.Chown(p, ids[0], ids[1]); err != nil {
			t.Fatalf("SETSTAT chown %v: %v", ids, err)
		}
	}
	assertHostOwner()
	for _, ids := range [][2]int{{0, 0}, {int(owner.UID) + 1, int(owner.GID)}, {int(owner.UID), 0}, {-1, int(owner.GID) + 1}} {
		if err := c.Chown(p, ids[0], ids[1]); err == nil {
			t.Fatalf("SETSTAT chown %v: no error", ids)
		}
	}
	assertHostOwner()

	// FSETSTAT 同样处理。
	f, err := c.OpenFile(p, os.O_WRONLY)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Chown(int(owner.UID), int(owner.GID)); err != nil {
		t.Fatalf("FSETSTAT chown: %v", err)
	}
	if err := f.Chown(0, 0); err == nil {
		t.Fatal("FSETSTAT chown 0:0: no error")
	}
	assertHostOwner()

	// 与权限同时设置时，权限照常生效；改成其他 ID 时仍报错。
	if err := h.Filecmd(ownerSetstat(p, owner.UID, owner.GID, 0o600)); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v, %v", fi, err)
	}
	if err := h.Filecmd(ownerSetstat(p, 0, owner.GID, 0o644)); err == nil {
		t.Fatal("SETSTAT to uid 0 with mode: no error")
	}
	assertHostOwner()

	// 受限根目录的检查不变：只读名下的文件仍被拒绝。
	if err := c.Chown(filepath.Join(root, ".git", "config"), int(owner.UID), int(owner.GID)); err == nil {
		t.Fatal("chown under a read-only name succeeded")
	}
	assertUnchanged(t, root)
}

// 未告知呈现属主的服务器照旧把 chown 交给宿主。
func TestRootedChownWithoutPresentedOwner(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 可以 chown 给任何 ID")
	}
	c, root := setupRooted(t, false)
	if err := c.Chown(filepath.Join(root, "src", "main.go"), os.Getuid()+1000, os.Getgid()+1000); err == nil {
		t.Fatal("chown to another ID succeeded")
	}
}
