package autosync

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ansonnchan/tokenholic/internal/privatefs"
)

func TestLockPreventsOverlapAndCanBeReacquired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "sync.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{path, filepath.Dir(path)} {
		if ok, err := privatefs.IsPrivate(private); err != nil || !ok {
			t.Fatalf("IsPrivate(%s) = %t, %v", filepath.Base(private), ok, err)
		}
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
		// Windows lets only privileged accounts create symbolic links.
		t.Skipf("cannot create a symbolic link here: %v", err)
	}
	if _, err := Acquire(link); err == nil {
		t.Fatal("Acquire() accepted symbolic link")
	}
}

// A lock held by one process must stop another process, not only another
// handle in the same one: that is what keeps a manual sync and the scheduled
// job apart. The test re-runs itself as the second process.
func TestLockIsHeldAcrossProcesses(t *testing.T) {
	const pathVariable = "TOKENHOLIC_TEST_LOCK_PATH"
	if path := os.Getenv(pathVariable); path != "" {
		lock, err := Acquire(path)
		switch {
		case errors.Is(err, ErrAlreadyRunning):
			os.Exit(3)
		case err != nil:
			os.Exit(4)
		}
		_ = lock.Close()
		os.Exit(0)
	}
	path := filepath.Join(t.TempDir(), "sync.lock")
	attempt := func() int {
		command := exec.Command(os.Args[0], "-test.run=^TestLockIsHeldAcrossProcesses$")
		command.Env = append(os.Environ(), pathVariable+"="+path)
		err := command.Run()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		if err != nil {
			t.Fatalf("run the second process: %v", err)
		}
		return 0
	}
	held, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if code := attempt(); code != 3 {
		t.Fatalf("second process exited %d while the lock was held, want 3 (already running)", code)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if code := attempt(); code != 0 {
		t.Fatalf("second process exited %d after release, want 0", code)
	}
}
