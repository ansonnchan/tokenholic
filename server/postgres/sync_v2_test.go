package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"path/filepath"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/modelcatalog"
	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/store"
	protocol "github.com/ansonnchan/tokenholic/internal/sync"
	"github.com/ansonnchan/tokenholic/migrations"
	serverauth "github.com/ansonnchan/tokenholic/server/auth"
	serversync "github.com/ansonnchan/tokenholic/server/sync"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A client that knows no catalog at all uploads through the real service and
// repository. The server alone decides what each model is and costs.
func TestPostgresSyncV2DerivesOnTheServerAndRepeatsAsNoOp(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	_, registry, err := modelcatalog.Bundled()
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsurePricingRevision(ctx, pool, registry); err != nil {
		t.Fatal(err)
	}
	principal := registerSyncDevice(t, pool, 801, "40000000-0000-4000-8000-0000000000")
	repository, err := NewUsageRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := serversync.NewService(staticLeaderboardAuthenticator{principal: principal}, repository, bundledEngine(t))
	if err != nil {
		t.Fatal(err)
	}

	million := uint64(1_000_000)
	batch := clientBatch(t, []clientUsage{
		// $4 input + $0.20 read + $5 five-minute + $8 one-hour + $20 output.
		{minute: 0, model: "claude-opus-5-5", evidence: normalize.ModelReported, each: million, split: true},
		// The same model through Claude Code's context marker.
		{minute: 1, model: "claude-opus-5-5[1m]", evidence: normalize.ModelReported, each: million, split: true},
		// Combined cache write only: both millions at the $5 base rate.
		{minute: 2, model: "claude-opus-5-5", evidence: normalize.ModelReported, each: million},
		{minute: 3, model: "gpt-5.6-sol", evidence: normalize.ModelInferred, each: million},
		{minute: 4, model: "model-released-after-this-server", evidence: normalize.ModelReported, each: million},
		{minute: 5, model: "", evidence: normalize.ModelUnknown, each: million},
	})
	response, err := service.Apply(ctx, "credential", batch)
	if err != nil {
		t.Fatalf("Apply() error = %v; no observed model may reject an upload", err)
	}
	for _, acknowledgement := range response.Acknowledgements {
		if acknowledgement.Status != protocol.AckApplied {
			t.Fatalf("first upload acknowledgement = %#v", acknowledgement)
		}
	}

	type row struct {
		canonical, reason, estimate *string
		resolution, match, state    string
		priced, unpriced, revision  string
	}
	want := map[string]row{
		"claude-opus-5-5 split":     {canonical: text("claude-opus-5-5"), estimate: text("37200000"), resolution: "reported", match: "exact", state: "complete", priced: "5000000", unpriced: "0"},
		"claude-opus-5-5[1m] split": {canonical: text("claude-opus-5-5"), estimate: text("37200000"), resolution: "resolved", match: "reviewed", state: "complete", priced: "5000000", unpriced: "0"},
		"claude-opus-5-5 combined":  {canonical: text("claude-opus-5-5"), estimate: text("34200000"), resolution: "reported", match: "exact", state: "partial", priced: "5000000", unpriced: "0"},
		// Its price depends on prompt length, and this bucket proves none.
		"gpt-5.6-sol split":                {canonical: text("gpt-5.6-sol"), reason: text(modelcatalog.ReasonPromptLengthUnproven), resolution: "inferred", match: "exact", state: "unavailable", priced: "0", unpriced: "5000000"},
		"model-released-after-this-server": {reason: text(modelcatalog.ReasonUnknownModel), resolution: "unknown", match: "unknown", state: "unavailable", priced: "0", unpriced: "5000000"},
		"":                                 {reason: text(modelcatalog.ReasonModelUnavailable), resolution: "unknown", match: "unknown", state: "unavailable", priced: "0", unpriced: "5000000"},
	}
	read := func() map[string]row {
		t.Helper()
		rows, err := pool.Query(ctx, `
			SELECT observed_model_id, input_cache_write_5m_tokens IS NOT NULL, canonical_model_id, unpriced_reason,
			       api_estimate_microusd::text, model_resolution, model_match, api_estimate_state,
			       priced_tokens::text, unpriced_tokens::text, pricing_revision
			FROM usage_buckets WHERE device_id = $1::uuid ORDER BY bucket_start`, principal.DeviceID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		result := map[string]row{}
		for rows.Next() {
			var model string
			var split bool
			var value row
			if err := rows.Scan(&model, &split, &value.canonical, &value.reason, &value.estimate, &value.resolution,
				&value.match, &value.state, &value.priced, &value.unpriced, &value.revision); err != nil {
				t.Fatal(err)
			}
			key := model
			switch {
			case model == "claude-opus-5-5" && !split:
				key += " combined"
			case model == "claude-opus-5-5" || model == "claude-opus-5-5[1m]" || model == "gpt-5.6-sol":
				key += " split"
			}
			result[key] = value
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	stored := read()
	if len(stored) != len(want) {
		t.Fatalf("stored %d rows, want %d", len(stored), len(want))
	}
	for key, expected := range want {
		got := stored[key]
		if !sameText(got.canonical, expected.canonical) || !sameText(got.reason, expected.reason) || !sameText(got.estimate, expected.estimate) ||
			got.resolution != expected.resolution || got.match != expected.match || got.state != expected.state ||
			got.priced != expected.priced || got.unpriced != expected.unpriced || got.revision != registry.Revision {
			t.Errorf("%q stored as canonical=%s reason=%s estimate=%s %s/%s/%s priced=%s unpriced=%s revision=%s",
				key, show(got.canonical), show(got.reason), show(got.estimate), got.resolution, got.match, got.state, got.priced, got.unpriced, got.revision)
		}
	}

	// Repeating the identical upload changes nothing, including receipt times.
	var receivedBefore time.Time
	if err := pool.QueryRow(ctx, `SELECT MAX(received_at) FROM usage_buckets WHERE device_id = $1::uuid`, principal.DeviceID).Scan(&receivedBefore); err != nil {
		t.Fatal(err)
	}
	response, err = service.Apply(ctx, "credential", batch)
	if err != nil {
		t.Fatal(err)
	}
	for _, acknowledgement := range response.Acknowledgements {
		if acknowledgement.Status != protocol.AckUnchanged {
			t.Fatalf("repeated upload acknowledgement = %#v, want unchanged", acknowledgement)
		}
	}
	var receivedAfter time.Time
	if err := pool.QueryRow(ctx, `SELECT MAX(received_at) FROM usage_buckets WHERE device_id = $1::uuid`, principal.DeviceID).Scan(&receivedAfter); err != nil {
		t.Fatal(err)
	}
	if !receivedAfter.Equal(receivedBefore) {
		t.Fatal("a repeated upload rewrote stored rows")
	}
	repeated := read()
	for key := range want {
		if !sameText(stored[key].estimate, repeated[key].estimate) || stored[key].priced != repeated[key].priced {
			t.Fatalf("%q changed after a repeated upload", key)
		}
	}

	// The account total still counts every token, priced or not.
	accounts, err := NewAccountRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := accounts.AccountUsage(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	if usage.TotalTokens.Value == nil || *usage.TotalTokens.Value != "30000000" {
		t.Fatalf("account total tokens = %#v, want 6 buckets of 5M", usage.TotalTokens)
	}
}

// The v2 migration discards demo usage and nothing else.
func TestPostgresSyncV2MigrationResetsUsageOnly(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	available, err := migrations.All()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateTo(ctx, pool, available[:2]); err != nil {
		t.Fatal(err)
	}
	const userID, deviceID = "50000000-0000-4000-8000-000000000001", "50000000-0000-4000-8000-000000000011"
	if _, err := pool.Exec(ctx, `INSERT INTO users(id, display_name) VALUES ($1::uuid, 'Synthetic')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO devices(id, user_id, client_version) VALUES ($1::uuid, $2::uuid, 'test')`, deviceID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO groups(id, name, owner_user_id, timezone) VALUES ('50000000-0000-4000-8000-000000000021'::uuid, 'Synthetic group', $1::uuid, 'UTC')`, userID); err != nil {
		t.Fatal(err)
	}
	// A v1 row: client-resolved model metadata and the retired source list.
	if _, err := pool.Exec(ctx, `
		INSERT INTO usage_buckets(
			device_id, bucket_key, bucket_start, interval_seconds, source, surface, observed_model_id,
			model_resolution, measurement, normalization_version,
			input_uncached_state, input_cache_read_state, input_cache_write_state,
			output_tokens, output_state, reasoning_output_state, api_estimate_state, revision, payload_hash
		) VALUES ($1::uuid, $2, '2026-11-18T07:00:00Z', 60, 'claude', 'cli', 'synthetic-model',
			'unknown', 'reported', '1', 'unavailable', 'unavailable', 'unavailable',
			7, 'complete', 'unavailable', 'unavailable', 1, $3)
	`, deviceID, string(bytes.Repeat([]byte("a"), 64)), bytes.Repeat([]byte{0x44}, 32)); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{"usage_buckets": 0, "users": 1, "devices": 1, "groups": 1}
	for table, want := range counts {
		var got int
		if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM "+table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s rows after the v2 migration = %d, want %d", table, got, want)
		}
	}
	// The schema no longer hard-codes which sources exist; the API validates that.
	if _, err := pool.Exec(ctx, `INSERT INTO pricing_revisions(revision, catalog_checksum, effective_at) VALUES ('synthetic-r1', $1, CURRENT_TIMESTAMP)`, bytes.Repeat([]byte{0x22}, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO usage_buckets(
			device_id, bucket_key, bucket_start, interval_seconds, source, surface, observed_model_id,
			model_evidence, measurement, normalization_version,
			input_uncached_state, input_cache_read_state, input_cache_write_state,
			input_cache_write_5m_state, input_cache_write_1h_state,
			output_tokens, output_state, reasoning_output_state, revision, payload_hash,
			model_resolution, model_match, unpriced_reason, priced_tokens, unpriced_tokens,
			api_estimate_state, pricing_revision
		) VALUES ($1::uuid, $2, '2026-11-18T07:00:00Z', 60, 'future_source', 'future_surface', 'synthetic-model',
			'reported', 'reported', '1', 'unavailable', 'unavailable', 'unavailable', 'unavailable', 'unavailable',
			7, 'complete', 'unavailable', 1, $3,
			'unknown', 'unknown', 'unknown_model', 0, 7, 'unavailable', 'synthetic-r1')
	`, deviceID, string(bytes.Repeat([]byte("b"), 64)), bytes.Repeat([]byte{0x44}, 32)); err != nil {
		t.Fatalf("a well-formed future source was rejected by the schema: %v", err)
	}
	// Derived columns must stay consistent with the evidence beside them.
	violations := map[string]string{
		"priced and unpriced tokens must sum to the total": `UPDATE usage_buckets SET unpriced_tokens = 6`,
		"an unpriced row must say why":                     `UPDATE usage_buckets SET unpriced_reason = NULL`,
		"lifetimes cannot exceed the cache-write total":    `UPDATE usage_buckets SET input_cache_write_tokens = 1, input_cache_write_state = 'complete', input_cache_write_1h_tokens = 2, input_cache_write_1h_state = 'complete', unpriced_tokens = 8`,
		"an estimate needs a pricing key":                  `UPDATE usage_buckets SET api_estimate_microusd = 1, api_estimate_state = 'complete', priced_tokens = 7, unpriced_tokens = 0`,
		"a malformed source is rejected":                   `UPDATE usage_buckets SET source = 'Not A Source'`,
	}
	for name, statement := range violations {
		if _, err := pool.Exec(ctx, statement); pgErrorCode(err) != "23514" {
			t.Errorf("%s: error = %v, want a check violation", name, err)
		}
	}
}

type clientUsage struct {
	minute   int
	model    string
	evidence normalize.ModelResolution
	each     uint64
	split    bool
	// tier and band are pricing evidence; empty means the source proved none.
	tier normalize.ServiceTier
	band normalize.PromptBand
}

// clientBatch serializes buckets through the local store and outbox, the only
// path a real client has. No catalog is involved on this side.
func clientBatch(t *testing.T, usages []clientUsage) protocol.Batch {
	t.Helper()
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "client.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close(context.Background()) })
	buckets := make([]store.UsageBucket, 0, len(usages))
	for _, usage := range usages {
		each, write := usage.each, 2*usage.each
		complete := store.AggregateValue{Value: &each, State: store.AvailabilityComplete}
		lifetime := store.AggregateValue{State: store.AvailabilityUnavailable}
		if usage.split {
			lifetime = complete
		}
		buckets = append(buckets, store.UsageBucket{
			Start:  time.Date(2026, 11, 18, 12, usage.minute, 0, 0, time.UTC),
			Source: normalize.SourceClaude, Surface: normalize.SurfaceCLI,
			ObservedModelID: usage.model, ModelEvidence: usage.evidence,
			Measurement: normalize.MeasurementReported, NormalizationVersion: "1",
			ServiceTier: usage.tier, PromptBand: usage.band,
			Tokens: store.TokenBreakdown{
				InputUncached: complete, InputCacheRead: complete,
				InputCacheWrite:   store.AggregateValue{Value: &write, State: store.AvailabilityComplete},
				InputCacheWrite5m: lifetime, InputCacheWrite1h: lifetime, Output: complete,
				ReasoningOutput: store.AggregateValue{State: store.AvailabilityUnavailable},
			},
			Preview:  store.Preview{Resolution: normalize.ModelUnknown, APIEstimate: store.AggregateValue{State: store.AvailabilityUnavailable}},
			Revision: 1,
		})
	}
	if _, err := database.ApplyBucketRevisions(t.Context(), buckets); err != nil {
		t.Fatal(err)
	}
	records, err := database.PendingOutbox(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	batch, _, err := protocol.BuildBatch(records)
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

func registerSyncDevice(t *testing.T, pool *pgxpool.Pool, githubID int64, prefix string) serverauth.Principal {
	t.Helper()
	authRepository, err := NewAuthRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	secret := sha256.Sum256([]byte(prefix))
	identity, err := authRepository.RegisterDevice(t.Context(), serverauth.GitHubUser{ID: githubID, Login: "sync-v2-user"}, serverauth.DeviceRegistration{
		UserID: prefix + "01", DeviceID: prefix + "11", ClientVersion: "test",
		Credential: serverauth.NewCredential{CredentialID: prefix + "21", SecretHash: secret, ExpiresAt: time.Now().UTC().Add(time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return serverauth.Principal{UserID: identity.UserID, DeviceID: identity.DeviceID}
}

func text(value string) *string { return &value }

func sameText(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func show(value *string) string {
	if value == nil {
		return "<nil>"
	}
	return *value
}
