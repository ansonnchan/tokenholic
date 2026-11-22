package privatefs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateDirectoriesAndFiles(t *testing.T) {
	root := t.TempDir()
	// A fresh temporary directory is not private on every system; the test
	// only relies on what this package creates or restricts.
	directory := filepath.Join(root, "outer", "inner")
	if err := MkdirAll(directory); err != nil {
		t.Fatal(err)
	}
	if private, err := IsPrivate(directory); err != nil || !private {
		t.Fatalf("IsPrivate(directory) = %t, %v", private, err)
	}

	for name, prepare := range map[string]func(string) error{
		"a new file": func(string) error { return nil },
		"an existing open file": func(path string) error {
			return os.WriteFile(path, []byte("synthetic"), 0o644)
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(directory, filepath.Base(t.Name()))
			if err := prepare(path); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			if err := Touch(path); err != nil {
				t.Fatal(err)
			}
			if private, err := IsPrivate(path); err != nil || !private {
				t.Fatalf("IsPrivate(file) = %t, %v", private, err)
			}
			if after, err := os.ReadFile(path); err != nil || string(after) != string(before) {
				t.Fatalf("Touch changed the contents: %q, %v", after, err)
			}
			// Still usable by its owner.
			if err := os.WriteFile(path, []byte("rewritten"), 0o600); err != nil {
				t.Fatalf("owner cannot write a private file: %v", err)
			}
		})
	}
}

func TestMkdirAllIsRepeatable(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private")
	for range 2 {
		if err := MkdirAll(directory); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "kept"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MkdirAll(directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "kept")); err != nil {
		t.Fatalf("MkdirAll disturbed existing contents: %v", err)
	}
}

func TestRestrictFileRequiresAFile(t *testing.T) {
	if err := RestrictFile(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("RestrictFile() accepted a missing path")
	}
}

func TestApplicationRootIsAbsolute(t *testing.T) {
	root, err := ApplicationRoot()
	if err != nil {
		t.Skipf("no per-user directory in this environment: %v", err)
	}
	if !filepath.IsAbs(root) {
		t.Fatalf("ApplicationRoot() = %q, want an absolute path", root)
	}
}
