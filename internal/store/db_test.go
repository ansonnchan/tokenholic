package store

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ansonnchan/tokenholic/internal/privatefs"
)

func TestOpenCreatesConfiguredPrivateDatabase(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state", databaseFileName)
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(context.Background()) })

	if version, err := db.SchemaVersion(t.Context()); err != nil || version != 7 {
		t.Fatalf("SchemaVersion() = %d, %v; want 7, nil", version, err)
	}
	assertPragma(t, db, "journal_mode", "wal")
	assertPragma(t, db, "foreign_keys", "1")
	assertPragma(t, db, "busy_timeout", "5000")
	// Owner-only on every system: permission bits on Unix, a protected
	// single-user access-control list on Windows.
	for _, private := range []string{path, filepath.Dir(path)} {
		if ok, err := privatefs.IsPrivate(private); err != nil || !ok {
			t.Fatalf("IsPrivate(%s) = %t, %v", filepath.Base(private), ok, err)
		}
	}
}

func TestFileURIsSurviveSQLiteParsing(t *testing.T) {
	t.Parallel()
	query := url.Values{"_txlock": {"immediate"}}
	for _, test := range []struct{ path, want string }{
		{"/home/synthetic/.config/tokenholic/tokenholic.db", "file:/home/synthetic/.config/tokenholic/tokenholic.db?_txlock=immediate"},
		{"/Users/synthetic user/Library/tokenholic.db", "file:/Users/synthetic%20user/Library/tokenholic.db?_txlock=immediate"},
		// A drive letter must not land where a host name would be read.
		{"C:/Users/synthetic/AppData/Local/tokenholic/tokenholic.db", "file:C:/Users/synthetic/AppData/Local/tokenholic/tokenholic.db?_txlock=immediate"},
		{"C:/Users/a b/100%#?/tokenholic.db", "file:C:/Users/a%20b/100%25%23%3F/tokenholic.db?_txlock=immediate"},
	} {
		if got := fileURI(test.path, query); got != test.want {
			t.Errorf("fileURI(%q) = %q, want %q", test.path, got, test.want)
		}
	}
}

func TestOpenAcceptsPathsThatNeedEscaping(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "two words 100%", databaseFileName)
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		t.Fatalf("database was not written at the requested path: %v", err)
	}
}

func TestDefaultPathUsesLowercaseAndPreservesExistingDatabase(t *testing.T) {
	root := t.TempDir()
	preferred := filepath.Join(root, applicationDirectory, databaseFileName)
	path, err := defaultPath(root)
	if err != nil || path != preferred {
		t.Fatalf("defaultPath() = %q, %v; want %q", path, err, preferred)
	}
	legacy := filepath.Join(root, legacyApplicationDirectory(), databaseFileName)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	wantLegacy := legacy
	if _, err := os.Lstat(preferred); err == nil {
		wantLegacy = preferred
	}
	path, err = defaultPath(root)
	if err != nil || path != wantLegacy {
		t.Fatalf("legacy defaultPath() = %q, %v; want %q", path, err, wantLegacy)
	}
	if err := os.MkdirAll(filepath.Dir(preferred), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preferred, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	path, err = defaultPath(root)
	if err != nil || path != preferred {
		t.Fatalf("preferred defaultPath() = %q, %v; want %q", path, err, preferred)
	}
}

func TestOpenRejectsCanceledContextAndRelativePath(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(canceled, filepath.Join(t.TempDir(), databaseFileName)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Open(canceled) error = %v, want context cancellation", err)
	}
	if _, err := Open(t.Context(), databaseFileName); err == nil {
		t.Fatal("Open(relative path) succeeded")
	}
}

func TestOpenRejectsSymbolicLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges vary on Windows")
	}
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "target.db")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, databaseFileName)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.Context(), link); err == nil {
		t.Fatal("Open(symlink) succeeded")
	}
}

func TestCloseIsRepeatableAndDatabaseReopens(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), databaseFileName)
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func assertPragma(t *testing.T, db *DB, pragma, want string) {
	t.Helper()
	var got string
	if err := db.sql.QueryRowContext(t.Context(), "PRAGMA "+pragma).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("PRAGMA %s = %q, want %q", pragma, got, want)
	}
}
