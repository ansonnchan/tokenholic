package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ansonnchan/tokenholic/internal/privatefs"
	"github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

const (
	applicationDirectory = "tokenholic"
	databaseFileName     = "tokenholic.db"
	busyTimeoutMS        = 5000
)

// DB is tokenholic's local SQLite database.
type DB struct {
	sql       *sql.DB
	closeOnce sync.Once
	closeErr  error
	closed    chan struct{}
}

// DefaultPath returns the platform-appropriate application database path:
// under the user configuration directory on macOS and Linux, and under the
// local, non-roaming application data folder on Windows.
func DefaultPath() (string, error) {
	root, err := privatefs.ApplicationRoot()
	if err != nil {
		return "", fmt.Errorf("resolve local database directory: %w", err)
	}
	return defaultPath(root)
}

func defaultPath(configDir string) (string, error) {
	preferred := filepath.Join(configDir, applicationDirectory, databaseFileName)
	if _, err := os.Lstat(preferred); err == nil {
		return preferred, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect local database path: %w", err)
	}
	// Previous releases capitalized this directory; keep existing databases in place.
	legacy := filepath.Join(configDir, legacyApplicationDirectory(), databaseFileName)
	if _, err := os.Lstat(legacy); err == nil {
		return legacy, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect legacy local database path: %w", err)
	}
	return preferred, nil
}

func legacyApplicationDirectory() string {
	return strings.ToUpper(applicationDirectory[:1]) + applicationDirectory[1:]
}

// Open prepares a private database file, configures SQLite, and applies all
// supported migrations before returning.
func Open(ctx context.Context, path string) (*DB, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("open local database: %w", err)
	}
	if !filepath.IsAbs(path) {
		return nil, errors.New("open local database: path must be absolute")
	}
	if err := prepareDatabaseFile(path); err != nil {
		return nil, err
	}

	sqlDB, err := driver.Open(dataSourceName(path))
	if err != nil {
		return nil, fmt.Errorf("configure local database: %w", err)
	}
	db := &DB{sql: sqlDB, closed: make(chan struct{})}
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("open local database: %w", err)
	}
	if err := migrate(ctx, sqlDB, applicationMigrations); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// Close stops accepting work and waits for active database operations. If the
// context expires, closure continues in the background so handles are not leaked.
func (db *DB) Close(ctx context.Context) error {
	db.closeOnce.Do(func() {
		go func() {
			db.closeErr = db.sql.Close()
			close(db.closed)
		}()
	})
	select {
	case <-db.closed:
		if db.closeErr != nil {
			return fmt.Errorf("close local database: %w", db.closeErr)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("close local database: %w", ctx.Err())
	}
}

// SchemaVersion returns the latest applied local schema version.
func (db *DB) SchemaVersion(ctx context.Context) (int, error) {
	version, err := currentVersion(ctx, db.sql)
	if err != nil {
		return 0, fmt.Errorf("read local schema version: %w", err)
	}
	return version, nil
}

func prepareDatabaseFile(path string) error {
	// The directory is private before the database exists, so SQLite's
	// journal files beside it are never created readable by anyone else.
	if err := privatefs.MkdirAll(filepath.Dir(path)); err != nil {
		return fmt.Errorf("prepare local database directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("prepare local database: symbolic links are not allowed")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect local database: %w", err)
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("create local database: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close prepared local database: %w", err)
	}
	if err := privatefs.RestrictFile(path); err != nil {
		return fmt.Errorf("secure local database: %w", err)
	}
	return nil
}

func dataSourceName(path string) string {
	query := url.Values{}
	// These connection-local PRAGMAs apply to every pooled connection. WAL is
	// persistent, while foreign keys and busy handling are not.
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMS))
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "synchronous(NORMAL)")
	query.Set("_txlock", "immediate")
	return fileURI(filepath.ToSlash(path), query)
}

// fileURI writes a SQLite file: URI for a slash-separated absolute path. The
// path follows the scheme directly, with no authority: SQLite would read a
// Windows drive letter after "file://" as a host name and refuse it.
func fileURI(slashed string, query url.Values) string {
	return "file:" + (&url.URL{Path: slashed}).EscapedPath() + "?" + query.Encode()
}
