package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"sort"
)

// ErrIncompatibleSchema marks a database created by an unsupported schema.
var ErrIncompatibleSchema = errors.New("incompatible local database schema")

type migration struct {
	version int
	name    string
	sql     string
}

//go:embed migrations/*.sql
var migrationFiles embed.FS

var applicationMigrations = mustLoadMigrations([]struct {
	version int
	name    string
	file    string
}{
	{version: 1, name: "initialize_store", file: "migrations/0001_initialize_store.sql"},
	{version: 2, name: "store_observations", file: "migrations/0002_store_observations.sql"},
	{version: 3, name: "store_usage_buckets", file: "migrations/0003_store_usage_buckets.sql"},
	{version: 4, name: "cache_write_lifetimes", file: "migrations/0004_cache_write_lifetimes.sql"},
	{version: 5, name: "sync_v2_buckets", file: "migrations/0005_sync_v2_buckets.sql"},
	{version: 6, name: "recollect_codex_usage", file: "migrations/0006_recollect_codex_usage.sql"},
	{version: 7, name: "pricing_evidence", file: "migrations/0007_pricing_evidence.sql"},
})

func mustLoadMigrations(definitions []struct {
	version int
	name    string
	file    string
}) []migration {
	migrations := make([]migration, 0, len(definitions))
	for _, definition := range definitions {
		contents, err := migrationFiles.ReadFile(definition.file)
		if err != nil {
			panic(fmt.Sprintf("load migration %s: %v", definition.file, err))
		}
		migrations = append(migrations, migration{
			version: definition.version,
			name:    definition.name,
			sql:     string(contents),
		})
	}
	return migrations
}

func migrate(ctx context.Context, db *sql.DB, migrations []migration) (err error) {
	if err := validateMigrationSet(migrations); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin local schema migration: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		) STRICT
	`); err != nil {
		return fmt.Errorf("initialize local schema history: %w", err)
	}

	applied, err := readAppliedMigrations(ctx, tx)
	if err != nil {
		return err
	}
	if err := validateAppliedMigrations(applied, migrations); err != nil {
		return err
	}
	for _, migration := range migrations[len(applied):] {
		if _, err = tx.ExecContext(ctx, migration.sql); err != nil {
			return fmt.Errorf("apply local schema migration %d (%s): %w", migration.version, migration.name, err)
		}
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, name, checksum) VALUES (?, ?, ?)`,
			migration.version, migration.name, checksum(migration.sql)); err != nil {
			return fmt.Errorf("record local schema migration %d (%s): %w", migration.version, migration.name, err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit local schema migration: %w", err)
	}
	return nil
}

type appliedMigration struct {
	version  int
	name     string
	checksum string
}

func readAppliedMigrations(ctx context.Context, tx *sql.Tx) ([]appliedMigration, error) {
	rows, err := tx.QueryContext(ctx, `SELECT version, name, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("read local schema history: %w", err)
	}
	defer rows.Close()
	var applied []appliedMigration
	for rows.Next() {
		var migration appliedMigration
		if err := rows.Scan(&migration.version, &migration.name, &migration.checksum); err != nil {
			return nil, fmt.Errorf("decode local schema history: %w", err)
		}
		applied = append(applied, migration)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate local schema history: %w", err)
	}
	return applied, nil
}

func validateMigrationSet(migrations []migration) error {
	if !sort.SliceIsSorted(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version }) {
		return errors.New("local schema migrations are not ordered")
	}
	for index, migration := range migrations {
		expected := index + 1
		if migration.version != expected || migration.name == "" || migration.sql == "" {
			return fmt.Errorf("local schema migration sequence is invalid at version %d", expected)
		}
	}
	return nil
}

func validateAppliedMigrations(applied []appliedMigration, supported []migration) error {
	if len(applied) > len(supported) {
		return fmt.Errorf("%w: database version %d is newer than supported version %d", ErrIncompatibleSchema, applied[len(applied)-1].version, len(supported))
	}
	for index, actual := range applied {
		expected := supported[index]
		if actual.version != expected.version || actual.name != expected.name || actual.checksum != checksum(expected.sql) {
			return fmt.Errorf("%w: migration history differs at version %d", ErrIncompatibleSchema, expected.version)
		}
	}
	return nil
}

func currentVersion(ctx context.Context, db *sql.DB) (int, error) {
	var version int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

func checksum(contents string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(contents)))
}
