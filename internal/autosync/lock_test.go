package autosync

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLockPreventsOverlapAndCanBeReacquired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "sync.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode = %v, %v", info, err)
	}
	if _, err := Acquire(path); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Acquire() = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultLockHoldsEveryApplicationDirectory(t *testing.T) {
	root := t.TempDir()
	lock, err := acquireDefault(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireDefault(root); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second acquireDefault() = %v, want overlap", err)
	}
	for _, directory := range lockDirectories() {
		if _, err := Acquire(filepath.Join(root, directory, "sync.lock")); !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("Acquire(%s) = %v, want overlap", directory, err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	// No directory appears that this platform never shipped.
	if len(entries) > len(lockDirectories()) {
		t.Fatalf("lock created %d directories for %d spellings", len(entries), len(lockDirectories()))
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := acquireDefault(root)
	if err != nil {
		t.Fatalf("acquireDefault() after release = %v", err)
	}
	if err := again.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLockRejectsSymbolicLink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "sync.lock")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(link); err == nil {
		t.Fatal("Acquire() accepted symbolic link")
	}
}
