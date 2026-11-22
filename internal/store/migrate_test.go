package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

func TestMigrateCreatesUpgradesAndRepeats(t *testing.T) {
	t.Parallel()
	db := openTestSQLDB(t)
	migrations := []migration{
		{version: 1, name: "one", sql: `CREATE TABLE one (id INTEGER PRIMARY KEY) STRICT;`},
		{version: 2, name: "two", sql: `CREATE TABLE two (id INTEGER PRIMARY KEY) STRICT;`},
	}
	if err := migrate(t.Context(), db, migrations[:1]); err != nil {
		t.Fatal(err)
	}
	if err := migrate(t.Context(), db, migrations); err != nil {
		t.Fatal(err)
	}
	if err := migrate(t.Context(), db, migrations); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("migration count = %d, want 2", count)
	}
}

func TestMigrateRejectsIncompatibleVersionsAndHistory(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		applied appliedMigration
	}{
		{name: "newer version", applied: appliedMigration{version: 2, name: "future", checksum: "future"}},
		{name: "changed migration", applied: appliedMigration{version: 1, name: "changed", checksum: "changed"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openTestSQLDB(t)
			createMigrationTable(t, db)
			if _, err := db.ExecContext(t.Context(),
				`INSERT INTO schema_migrations(version, name, checksum) VALUES (?, ?, ?)`,
				test.applied.version, test.applied.name, test.applied.checksum); err != nil {
				t.Fatal(err)
			}
			err := migrate(t.Context(), db, []migration{{version: 1, name: "one", sql: `SELECT 1;`}})
			if !errors.Is(err, ErrIncompatibleSchema) {
				t.Fatalf("migrate() error = %v, want ErrIncompatibleSchema", err)
			}
		})
	}
}

func TestMigrateRollsBackPendingBatch(t *testing.T) {
	t.Parallel()
	db := openTestSQLDB(t)
	err := migrate(t.Context(), db, []migration{
		{version: 1, name: "one", sql: `CREATE TABLE should_rollback (id INTEGER PRIMARY KEY) STRICT;`},
		{version: 2, name: "broken", sql: `CREATE TABLE`},
	})
	if err == nil {
		t.Fatal("migrate() succeeded")
	}
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('schema_migrations', 'should_rollback')`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("tables after rollback = %d, want 0", count)
	}
}

func TestMigrateHonorsCancellation(t *testing.T) {
	t.Parallel()
	db := openTestSQLDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := migrate(ctx, db, []migration{{version: 1, name: "one", sql: `SELECT 1;`}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("migrate(canceled) error = %v, want context cancellation", err)
	}
}

func TestCacheWriteLifetimeMigrationRemovesLocalNoticeUsage(t *testing.T) {
	t.Parallel()
	db := openTestSQLDB(t)
	if err := migrate(t.Context(), db, applicationMigrations[:3]); err != nil {
		t.Fatal(err)
	}
	const insertObservation = `
		INSERT INTO observations(
			identity, source, surface, source_version, occurred_at, observed_model_id,
			model_resolution, input_cache_write_tokens, count_semantics, measurement,
			adapter_version, normalization_version, warnings_json
		) VALUES (?, 'claude', 'cli', 'synthetic-v1', '2026-01-06T03:00:00Z', ?, 'unknown', '40',
			'incremental', 'reported', '1', '1', '[]')`
	const insertBucket = `
		INSERT INTO usage_buckets(
			bucket_key, bucket_start, interval_seconds, source, surface, observed_model_id,
			model_resolution, measurement, normalization_version, input_uncached_state,
			input_cache_read_state, input_cache_write_state, output_state,
			reasoning_output_state, api_estimate_state, revision, payload_json
		) VALUES (?, '2026-01-06T03:00:00Z', 60, 'claude', 'cli', ?, 'unknown', 'reported', '1',
			'unavailable', 'unavailable', 'unavailable', 'unavailable', 'unavailable', 'unavailable', '1', '{}')`
	for key, model := range map[string]string{"kept": "claude-sonnet-synthetic", "removed": "<synthetic>"} {
		if _, err := db.ExecContext(t.Context(), insertObservation, key, model); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(), insertBucket, key, model); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(t.Context(),
			`INSERT INTO outbox_records(bucket_key, revision, payload_json) VALUES (?, '1', '{}')`, key); err != nil {
			t.Fatal(err)
		}
	}

	// Stop at the lifetime migration: a later one rebuilds the bucket tables.
	if err := migrate(t.Context(), db, applicationMigrations[:4]); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"observations", "usage_buckets", "outbox_records"} {
		var count int
		if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s rows after migration = %d, want only the real model", table, count)
		}
	}
	// Existing rows keep their combined total with an unknown time-to-live.
	var total string
	var fiveMinute, oneHour sql.NullString
	if err := db.QueryRowContext(t.Context(), `
		SELECT input_cache_write_tokens, input_cache_write_5m_tokens, input_cache_write_1h_tokens
		FROM observations`).Scan(&total, &fiveMinute, &oneHour); err != nil {
		t.Fatal(err)
	}
	if total != "40" || fiveMinute.Valid || oneHour.Valid {
		t.Fatalf("migrated cache write = %q/%v/%v, want 40 with unknown lifetimes", total, fiveMinute, oneHour)
	}
}

func TestSyncV2MigrationRebuildsBucketsAndKeepsSourceEvidence(t *testing.T) {
	t.Parallel()
	db := openTestSQLDB(t)
	if err := migrate(t.Context(), db, applicationMigrations[:4]); err != nil {
		t.Fatal(err)
	}
	const insertObservation = `
		INSERT INTO observations(
			identity, source, surface, source_version, occurred_at, observed_model_id,
			canonical_model_id, model_provider, pricing_key, model_resolution, output_tokens,
			count_semantics, measurement, adapter_version, normalization_version, warnings_json
		) VALUES (?, ?, 'cli', 'synthetic-v1', '2026-01-06T03:00:00Z', ?, ?, ?, ?, ?, '7',
			'incremental', 'reported', '1', '1', ?)`
	rows := []struct {
		identity, source, model string
		canonical, pricing      any
		resolution, warnings    string
		wantEvidence            string
	}{
		{"resolved", "claude", "model-latest", "model-a", "synthetic/model-a", "resolved", `[]`, "reported"},
		{"uncatalogued", "claude", "model-new", nil, nil, "unknown", `[{"code":"model_unresolved"}]`, "reported"},
		{"inferred-known", "codex", "model-b", "model-b", nil, "inferred", `[{"code":"model_inferred"}]`, "inferred"},
		{"inferred-uncatalogued", "codex", "model-c", nil, nil, "unknown", `[{"code":"model_inferred"},{"code":"model_unresolved"}]`, "inferred"},
		{"no-model", "codex", "", nil, nil, "unknown", `[{"code":"model_unavailable"}]`, "unknown"},
	}
	for _, row := range rows {
		var provider any
		if row.canonical != nil {
			provider = "synthetic"
		}
		if _, err := db.ExecContext(t.Context(), insertObservation, row.identity, row.source, row.model,
			row.canonical, provider, row.pricing, row.resolution, row.warnings); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO usage_buckets(
			bucket_key, bucket_start, interval_seconds, source, surface, observed_model_id,
			model_resolution, measurement, normalization_version, input_uncached_state,
			input_cache_read_state, input_cache_write_state, output_state,
			reasoning_output_state, api_estimate_state, revision, payload_json
		) VALUES ('old', '2026-01-06T03:00:00Z', 60, 'claude', 'cli', 'model-new', 'unknown', 'reported', '1',
			'unavailable', 'unavailable', 'unavailable', 'unavailable', 'unavailable', 'unavailable', '9', '{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO outbox_records(bucket_key, revision, payload_json) VALUES ('old', '9', '{}')`); err != nil {
		t.Fatal(err)
	}

	// Stop at the sync v2 migration: a later one discards Codex observations.
	if err := migrate(t.Context(), db, applicationMigrations[:5]); err != nil {
		t.Fatal(err)
	}
	// Buckets are derived state and are rebuilt by the next refresh.
	for _, table := range []string{"usage_buckets", "outbox_records"} {
		var count int
		if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s rows after migration = %d, want 0", table, count)
		}
	}
	for _, row := range rows {
		var evidence string
		var canonical, provider, pricing sql.NullString
		if err := db.QueryRowContext(t.Context(), `
			SELECT model_resolution, canonical_model_id, model_provider, pricing_key
			FROM observations WHERE identity = ?`, row.identity).Scan(&evidence, &canonical, &provider, &pricing); err != nil {
			t.Fatal(err)
		}
		if evidence != row.wantEvidence || canonical.Valid || provider.Valid || pricing.Valid {
			t.Errorf("%s: evidence = %q, catalog columns = %v/%v/%v; want %q and none",
				row.identity, evidence, canonical, provider, pricing, row.wantEvidence)
		}
	}
}

func TestCodexRecollectionMigrationDiscardsOnlyCodexObservations(t *testing.T) {
	t.Parallel()
	db := openTestSQLDB(t)
	if err := migrate(t.Context(), db, applicationMigrations[:5]); err != nil {
		t.Fatal(err)
	}
	const insertObservation = `
		INSERT INTO observations(
			identity, source, surface, source_version, occurred_at, observed_model_id, model_resolution,
			output_tokens, count_semantics, measurement, adapter_version, normalization_version, warnings_json
		) VALUES (?, ?, 'cli', 'synthetic-v1', '2026-01-06T03:00:00Z', 'model-synthetic', ?, '7',
			'incremental', ?, '1', '1', ?)`
	for _, row := range [][]any{
		{"claude-kept", "claude", "reported", "reported", `[]`},
		{"codex-delta", "codex", "inferred", "derived", `[{"code":"model_inferred"}]`},
		{"codex-reset", "codex", "inferred", "derived", `[{"code":"model_inferred"},{"code":"counter_reset"}]`},
	} {
		if _, err := db.ExecContext(t.Context(), insertObservation, row...); err != nil {
			t.Fatal(err)
		}
	}
	// A bucket built from the discarded rows stays until the next rebuild
	// retracts it, so the server is told as well.
	if _, err := db.ExecContext(t.Context(), `
		INSERT INTO usage_buckets(bucket_key, bucket_start, revision, content_hash, payload_json, preview_json)
		VALUES ('codex-bucket', '2026-01-06T03:00:00Z', '1', ?, '{}', '{}')`, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}

	if err := migrate(t.Context(), db, applicationMigrations); err != nil {
		t.Fatal(err)
	}
	var remaining string
	if err := db.QueryRowContext(t.Context(), `SELECT group_concat(identity) FROM observations`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != "claude-kept" {
		t.Fatalf("observations after migration = %q, want only the Claude row", remaining)
	}
	var buckets int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM usage_buckets`).Scan(&buckets); err != nil || buckets != 1 {
		t.Fatalf("usage buckets after migration = %d, %v; want the stored bucket left for retraction", buckets, err)
	}
}

func openTestSQLDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), databaseFileName)
	db, err := driver.Open(dataSourceName(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func createMigrationTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), `
		CREATE TABLE schema_migrations (
			version INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			checksum TEXT NOT NULL,
			applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		) STRICT
	`); err != nil {
		t.Fatal(err)
	}
}

// The day-level migration rebuilds three tables to widen their source lists.
// Every existing row, and the link between a source's health and its
// checkpoint, must come through unchanged.
func TestDayLevelMigrationKeepsEveryExistingRow(t *testing.T) {
	t.Parallel()
	db := openTestSQLDB(t)
	if err := migrate(t.Context(), db, applicationMigrations[:7]); err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`INSERT INTO observations(
			identity, source, surface, source_version, session_fingerprint, occurred_at, observed_model_id, model_resolution,
			input_uncached_tokens, input_cache_write_tokens, input_cache_write_5m_tokens, input_cache_write_1h_tokens,
			output_tokens, count_semantics, measurement, adapter_version, normalization_version, warnings_json,
			service_tier, prompt_tokens
		) VALUES
			('claude-row', 'claude', 'desktop_cowork', 'synthetic-v1', 'session-print', '2026-01-06T03:00:00Z', 'model-synthetic', 'reported',
			 '11', '9', '4', '5', '7', 'incremental', 'reported', '3', '1', '[{"code":"synthetic"}]', 'priority', '120000'),
			('codex-row', 'codex', 'shared_local', 'synthetic-v1', NULL, '2026-01-06T03:01:00Z', 'model-synthetic', 'inferred',
			 NULL, NULL, NULL, NULL, '18446744073709551615', 'cumulative_snapshot', 'derived', '3', '1', '[{"code":"model_inferred"}]', NULL, NULL)`,
		`INSERT INTO source_checkpoints(source_identity, source, surface, cursor_value, last_record_fingerprint, collected_at)
		 VALUES ('claude-source', 'claude', 'cli', '12', 'record-print', '2026-01-06T03:00:00Z'),
		        ('codex-source', 'codex', 'desktop', '3', NULL, '2026-01-06T03:00:00Z')`,
		`INSERT INTO adapter_health(source_identity, source, surface, status, record_count, format, capabilities_json, warnings_json, adapter_version, checked_at, last_success_at)
		 VALUES ('claude-source', 'claude', 'cli', 'detected', '12', 'claude-jsonl-v1', '{"model":"reported"}', '[]', '3', '2026-01-06T03:00:00Z', '2026-01-06T02:00:00Z'),
		        ('codex-source', 'codex', 'desktop', 'degraded', NULL, 'codex-rollout-jsonl', '{}', '[{"code":"no_candidate_records"}]', '3', '2026-01-06T03:00:00Z', NULL)`,
		`INSERT INTO usage_buckets(bucket_key, bucket_start, revision, content_hash, payload_json, preview_json)
		 VALUES ('kept-bucket', '2026-01-06T03:00:00Z', '4', '` + strings.Repeat("a", 64) + `', '{}', '{}')`,
		`INSERT INTO outbox_records(bucket_key, revision, payload_json, state) VALUES ('kept-bucket', '4', '{}', 'acknowledged')`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	dump := func() string {
		t.Helper()
		var all []string
		for _, query := range []string{
			`SELECT identity || '|' || source || '|' || surface || '|' || source_version || '|' || COALESCE(session_fingerprint,'<null>') || '|' || occurred_at || '|' ||
				observed_model_id || '|' || model_resolution || '|' || COALESCE(input_uncached_tokens,'<null>') || '|' || COALESCE(input_cache_read_tokens,'<null>') || '|' ||
				COALESCE(input_cache_write_tokens,'<null>') || '|' || COALESCE(input_cache_write_5m_tokens,'<null>') || '|' || COALESCE(input_cache_write_1h_tokens,'<null>') || '|' ||
				COALESCE(output_tokens,'<null>') || '|' || COALESCE(reasoning_output_tokens,'<null>') || '|' || count_semantics || '|' || measurement || '|' || adapter_version || '|' ||
				normalization_version || '|' || warnings_json || '|' || COALESCE(service_tier,'<null>') || '|' || COALESCE(prompt_tokens,'<null>') FROM observations ORDER BY identity`,
			`SELECT source_identity || '|' || source || '|' || surface || '|' || cursor_value || '|' || COALESCE(last_record_fingerprint,'<null>') || '|' || collected_at FROM source_checkpoints ORDER BY source_identity`,
			`SELECT source_identity || '|' || source || '|' || surface || '|' || status || '|' || COALESCE(record_count,'<null>') || '|' || format || '|' || capabilities_json || '|' ||
				warnings_json || '|' || adapter_version || '|' || checked_at || '|' || COALESCE(last_success_at,'<null>') FROM adapter_health ORDER BY source_identity`,
			`SELECT bucket_key || '|' || revision || '|' || payload_json FROM usage_buckets ORDER BY bucket_key`,
			`SELECT bucket_key || '|' || revision || '|' || state FROM outbox_records ORDER BY bucket_key`,
		} {
			rows, err := db.QueryContext(t.Context(), query)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					t.Fatal(err)
				}
				all = append(all, line)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
		}
		return strings.Join(all, "\n")
	}
	before := dump()
	if strings.Count(before, "\n") != 7 {
		t.Fatalf("seeded rows = %q", before)
	}
	if err := migrate(t.Context(), db, applicationMigrations); err != nil {
		t.Fatal(err)
	}
	if after := dump(); after != before {
		t.Fatalf("rows changed across the migration:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	// The health row still belongs to its checkpoint, and to nothing left
	// over from the rebuild.
	var violations int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil || violations != 0 {
		t.Fatalf("foreign key violations = %d, %v", violations, err)
	}
	if _, err := db.ExecContext(t.Context(), `DELETE FROM source_checkpoints WHERE source_identity = 'codex-source'`); err != nil {
		t.Fatal(err)
	}
	var health int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM adapter_health`).Scan(&health); err != nil || health != 1 {
		t.Fatalf("adapter health rows after deleting one checkpoint = %d, %v; want the cascade to leave 1", health, err)
	}
	var leftovers int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sqlite_master WHERE name LIKE '%\_new' ESCAPE '\' OR sql LIKE '%\_new%' ESCAPE '\'`).Scan(&leftovers); err != nil || leftovers != 0 {
		t.Fatalf("objects still naming a rebuild table = %d, %v", leftovers, err)
	}
	var indexes int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name IN ('observations_occurred_at', 'observations_model')`).Scan(&indexes); err != nil || indexes != 2 {
		t.Fatalf("observation indexes = %d, %v", indexes, err)
	}
}
