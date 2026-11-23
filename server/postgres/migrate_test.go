package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testDatabaseEnv = "TOKENHOLIC_TEST_DATABASE_URL"

func TestOpenRequiresDatabaseURL(t *testing.T) {
	t.Parallel()
	if _, err := Open(t.Context(), ""); err == nil {
		t.Fatal("Open() succeeded without a database URL")
	}
}

func TestValidateAppliedRejectsChangedAndNewerHistory(t *testing.T) {
	t.Parallel()
	available := []migrations.Migration{{Version: 1, Name: "first", Checksum: "sum"}}
	tests := []struct {
		name    string
		applied []appliedMigration
	}{
		{name: "changed name", applied: []appliedMigration{{version: 1, name: "other", checksum: "sum"}}},
		{name: "changed checksum", applied: []appliedMigration{{version: 1, name: "first", checksum: "other"}}},
		{name: "newer", applied: []appliedMigration{{version: 1}, {version: 2}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateApplied(test.applied, available); !errors.Is(err, ErrIncompatibleSchema) {
				t.Fatalf("validateApplied() error = %v", err)
			}
		})
	}
}

func TestPostgresMigrationsAndOwnershipConstraints(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	var migrationCount int
	available, err := migrations.All()
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&migrationCount); err != nil || migrationCount != len(available) {
		t.Fatalf("migration count = %d, %v", migrationCount, err)
	}

	userID := "00000000-0000-0000-0000-000000000001"
	otherUserID := "00000000-0000-0000-0000-000000000002"
	deviceID := "00000000-0000-0000-0000-000000000011"
	otherDeviceID := "00000000-0000-0000-0000-000000000012"
	credentialID := "00000000-0000-0000-0000-000000000021"
	if _, err := pool.Exec(ctx, `INSERT INTO users(id, display_name) VALUES ($1::uuid, 'Synthetic One'), ($2::uuid, 'Synthetic Two')`, userID, otherUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO github_identities(github_user_id, user_id, login) VALUES (101, $1::uuid, 'synthetic-one')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO devices(id, user_id, client_version) VALUES ($1::uuid, $2::uuid, 'test'), ($3::uuid, $4::uuid, 'test')`, deviceID, userID, otherDeviceID, otherUserID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO device_credentials(id, device_id, secret_hash) VALUES ($1::uuid, $2::uuid, $3)`, credentialID, deviceID, bytes.Repeat([]byte{0x11}, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO device_credentials(id, device_id, secret_hash) VALUES ('00000000-0000-0000-0000-000000000022'::uuid, $1::uuid, $2)`, otherDeviceID, []byte("plaintext")); pgErrorCode(err) != "23514" {
		t.Fatalf("short credential hash error = %v, want check violation", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO github_identities(github_user_id, user_id, login) VALUES (102, $1::uuid, 'synthetic-two')`, userID); pgErrorCode(err) != "23505" {
		t.Fatalf("second identity for user error = %v, want unique violation", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO pricing_revisions(revision, catalog_checksum, effective_at) VALUES ('synthetic-r1', $1, $2)`, bytes.Repeat([]byte{0x22}, 32), time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	key := strings.Repeat("a", 64)
	if err := insertSyntheticBucket(ctx, pool, deviceID, key, int64(10), "complete"); err != nil {
		t.Fatal(err)
	}
	if err := insertSyntheticBucket(ctx, pool, deviceID, key, int64(10), "complete"); pgErrorCode(err) != "23505" {
		t.Fatalf("duplicate bucket error = %v, want unique violation", err)
	}
	if err := insertSyntheticBucket(ctx, pool, otherDeviceID, key, int64(10), "complete"); err != nil {
		t.Fatalf("same bucket key on another device: %v", err)
	}
	if err := insertSyntheticBucket(ctx, pool, "00000000-0000-0000-0000-000000000099", strings.Repeat("b", 64), int64(10), "complete"); pgErrorCode(err) != "23503" {
		t.Fatalf("unknown device error = %v, want foreign-key violation", err)
	}
	if err := insertSyntheticBucket(ctx, pool, deviceID, strings.Repeat("c", 64), int64(1), "unavailable"); pgErrorCode(err) != "23514" {
		t.Fatalf("contradictory availability error = %v, want check violation", err)
	}
	if err := insertSyntheticBucket(ctx, pool, deviceID, strings.Repeat("d", 64), nil, "complete"); pgErrorCode(err) != "23514" {
		t.Fatalf("missing complete value error = %v, want check violation", err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, userID); err != nil {
		t.Fatal(err)
	}
	var credentials, buckets int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM device_credentials`).Scan(&credentials); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM usage_buckets WHERE device_id = $1::uuid`, deviceID).Scan(&buckets); err != nil {
		t.Fatal(err)
	}
	if credentials != 0 || buckets != 0 {
		t.Fatalf("ownership cascade left credentials=%d buckets=%d", credentials, buckets)
	}

	if _, err := pool.Exec(ctx, `UPDATE schema_migrations SET checksum = 'changed' WHERE version = 1`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); !errors.Is(err, ErrIncompatibleSchema) {
		t.Fatalf("changed migration Migrate() error = %v", err)
	}
}

// insertSyntheticBucket writes one unpriced v2 bucket: 7 output tokens plus
// the given uncached input, all unpriced because the model is unknown.
func insertSyntheticBucket(ctx context.Context, pool *pgxpool.Pool, deviceID, key string, input any, inputState string) error {
	_, err := pool.Exec(ctx, `
		INSERT INTO usage_buckets(
			device_id, bucket_key, bucket_start, interval_seconds, source, surface,
			observed_model_id, model_evidence, measurement, normalization_version,
			input_uncached_tokens, input_uncached_state,
			input_cache_read_tokens, input_cache_read_state,
			input_cache_write_tokens, input_cache_write_state,
			input_cache_write_5m_tokens, input_cache_write_5m_state,
			input_cache_write_1h_tokens, input_cache_write_1h_state,
			output_tokens, output_state,
			reasoning_output_tokens, reasoning_output_state,
			revision, payload_hash,
			model_resolution, model_match, unpriced_reason, priced_tokens, unpriced_tokens,
			api_estimate_microusd, api_estimate_state, pricing_revision
		) VALUES (
			$1::uuid, $2, $3, 60, 'claude', 'cli',
			'synthetic-model', 'reported', 'reported', '1',
			$4, $5, NULL, 'unavailable', 0, 'complete', 0, 'complete', 0, 'complete',
			7, 'complete', 3, 'complete',
			1, $6,
			'unknown', 'unknown', 'unknown_model', 0, COALESCE($4::numeric, 0) + 7,
			NULL, 'unavailable', 'synthetic-r1'
		)
	`, deviceID, key, time.Date(2026, 11, 18, 7, 0, 0, 0, time.UTC), input, inputState, bytes.Repeat([]byte{0x33}, 32))
	return err
}

func integrationPool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv(testDatabaseEnv)
	if databaseURL == "" {
		t.Skipf("%s is not set", testDatabaseEnv)
	}
	admin, err := Open(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	schema := "tokenholic_test_" + hex.EncodeToString(random[:])
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), `CREATE SCHEMA `+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	if err := pool.Ping(t.Context()); err != nil {
		pool.Close()
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = admin.Exec(cleanupCtx, `DROP SCHEMA `+identifier+` CASCADE`)
		admin.Close()
	})
	return pool
}

func pgErrorCode(err error) string {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		return postgresError.Code
	}
	return fmt.Sprintf("not-postgres-error:%v", err)
}
