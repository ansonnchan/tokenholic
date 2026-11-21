package postgres

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ansonnchan/tokenholic/internal/modelcatalog"
	"github.com/ansonnchan/tokenholic/internal/normalize"
	protocol "github.com/ansonnchan/tokenholic/internal/sync"
	"github.com/ansonnchan/tokenholic/migrations"
	serversync "github.com/ansonnchan/tokenholic/server/sync"
)

// Buckets that differ only in pricing evidence are stored with that evidence
// and priced from the rate card it proves. Each bucket holds one million
// tokens of input, cache read, and output plus two million of cache write, of
// which gpt-5.6-sol publishes a rate for the five-minute million only.
func TestPostgresSyncStoresPricingEvidenceAndSelectsRateCards(t *testing.T) {
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
	principal := registerSyncDevice(t, pool, 811, "41000000-0000-4000-8000-0000000000")
	repository, err := NewUsageRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	engine := bundledEngine(t)
	service, err := serversync.NewService(staticLeaderboardAuthenticator{principal: principal}, repository, engine)
	if err != nil {
		t.Fatal(err)
	}
	million := uint64(1_000_000)
	const model = "gpt-5.6-sol"
	batch := clientBatch(t, []clientUsage{
		{minute: 0, model: model, evidence: normalize.ModelInferred, each: million, split: true, tier: normalize.ServiceTierStandard, band: normalize.PromptBandUpTo100K},
		{minute: 0, model: model, evidence: normalize.ModelInferred, each: million, split: true, tier: normalize.ServiceTierPriority, band: normalize.PromptBandUpTo100K},
		{minute: 0, model: model, evidence: normalize.ModelInferred, each: million, split: true, tier: normalize.ServiceTierFlex, band: normalize.PromptBandUpTo272K},
		{minute: 0, model: model, evidence: normalize.ModelInferred, each: million, split: true, tier: normalize.ServiceTierPriority, band: normalize.PromptBandOver272K},
		{minute: 0, model: model, evidence: normalize.ModelInferred, each: million, split: true, band: normalize.PromptBandUpTo200K},
		{minute: 0, model: model, evidence: normalize.ModelInferred, each: million, split: true, tier: normalize.ServiceTierPriority},
		{minute: 0, model: model, evidence: normalize.ModelInferred, each: million, split: true, tier: normalize.ServiceTierBatch, band: normalize.PromptBandUpTo100K},
		{minute: 0, model: model, evidence: normalize.ModelInferred, each: million, split: true},
	})
	if len(batch.Buckets) != 8 {
		t.Fatalf("client built %d buckets, want 8: evidence must separate usage of one model in one minute", len(batch.Buckets))
	}
	if _, err := service.Apply(ctx, "credential", batch); err != nil {
		t.Fatal(err)
	}
	// tier|band|pricing key|reason|estimate|state|priced|unpriced.
	// Standard: 4 + 0.40 + 5 + 20 = $29.40; the one-hour million has no rate.
	want := []string{
		"-|-|<nil>|prompt_length_unproven|<nil>|unavailable|0|5000000",
		"-|le_200k|openai/gpt-5.6-sol|<nil>|29400000|partial|4000000|1000000",
		"batch|le_100k|<nil>|tier_rate_unavailable|<nil>|unavailable|0|5000000",
		"flex|le_272k|openai/gpt-5.6-sol:flex|<nil>|14700000|partial|4000000|1000000",
		"priority|-|<nil>|prompt_length_unproven|<nil>|unavailable|0|5000000",
		"priority|gt_272k|openai/gpt-5.6-sol:priority:above-272k|<nil>|97600000|partial|4000000|1000000",
		"priority|le_100k|openai/gpt-5.6-sol:priority|<nil>|58800000|partial|4000000|1000000",
		"standard|le_100k|openai/gpt-5.6-sol|<nil>|29400000|partial|4000000|1000000",
	}
	const query = `
		SELECT concat_ws('|', COALESCE(service_tier, '-'), COALESCE(prompt_band, '-'), COALESCE(pricing_key, '<nil>'),
		       COALESCE(unpriced_reason, '<nil>'), COALESCE(api_estimate_microusd::text, '<nil>'), api_estimate_state,
		       priced_tokens::text, unpriced_tokens::text)
		FROM usage_buckets WHERE device_id = $1::uuid ORDER BY 1`
	stored := snapshotQuery(t, pool, principal.DeviceID, query)
	if stored != strings.Join(want, "\n") {
		t.Fatalf("stored rows =\n%s\nwant\n%s", stored, strings.Join(want, "\n"))
	}

	// A retry is unchanged: the evidence is part of the payload hash.
	response, err := service.Apply(ctx, "credential", batch)
	if err != nil {
		t.Fatal(err)
	}
	for _, acknowledgement := range response.Acknowledgements {
		if acknowledgement.Status != protocol.AckUnchanged {
			t.Fatalf("repeated upload acknowledgement = %#v, want unchanged", acknowledgement)
		}
	}

	// Repricing reads the stored evidence back. Marking every row stale and
	// repricing with the same catalog must reproduce each card exactly.
	if _, err := pool.Exec(ctx, `INSERT INTO pricing_revisions(revision, catalog_checksum, effective_at) VALUES ('synthetic-stale', $1, CURRENT_TIMESTAMP)`, bytes.Repeat([]byte{0x33}, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE usage_buckets SET pricing_revision = 'synthetic-stale' WHERE device_id = $1::uuid`, principal.DeviceID); err != nil {
		t.Fatal(err)
	}
	if result, err := RepriceUsage(ctx, pool, engine); err != nil || result.Repriced != 8 {
		t.Fatalf("RepriceUsage() = %#v, %v; want 8 rows", result, err)
	}
	if repriced := snapshotQuery(t, pool, principal.DeviceID, query); repriced != stored {
		t.Fatalf("repricing lost the pricing evidence:\n%s\nwant\n%s", repriced, stored)
	}
}

// The evidence columns are additive: usage stored before them is kept, reads
// as unproven, and is priced accordingly at the next reprice.
func TestPostgresPricingEvidenceMigrationKeepsStoredUsage(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	available, err := migrations.All()
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateTo(ctx, pool, available[:3]); err != nil {
		t.Fatal(err)
	}
	const userID, deviceID = "51000000-0000-4000-8000-000000000001", "51000000-0000-4000-8000-000000000011"
	if _, err := pool.Exec(ctx, `INSERT INTO users(id, display_name) VALUES ($1::uuid, 'Synthetic')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO devices(id, user_id, client_version) VALUES ($1::uuid, $2::uuid, 'test')`, deviceID, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO pricing_revisions(revision, catalog_checksum, effective_at) VALUES ('synthetic-old', $1, CURRENT_TIMESTAMP)`, bytes.Repeat([]byte{0x22}, 32)); err != nil {
		t.Fatal(err)
	}
	// A row as the previous server stored it: a model then left unpriced
	// because its price depends on prompt length.
	if _, err := pool.Exec(ctx, `
		INSERT INTO usage_buckets(
			device_id, bucket_key, bucket_start, interval_seconds, source, surface, observed_model_id,
			model_evidence, measurement, normalization_version,
			input_uncached_tokens, input_uncached_state, input_cache_read_state, input_cache_write_state,
			input_cache_write_5m_state, input_cache_write_1h_state,
			output_tokens, output_state, reasoning_output_state, revision, payload_hash,
			canonical_model_id, model_provider, model_resolution, model_match, unpriced_reason,
			priced_tokens, unpriced_tokens, api_estimate_state, pricing_revision
		) VALUES ($1::uuid, $2, '2026-11-18T07:00:00Z', 60, 'codex', 'cli', 'gpt-5.6-sol',
			'inferred', 'reported', '1', 1000000, 'complete', 'unavailable', 'unavailable', 'unavailable', 'unavailable',
			1000000, 'complete', 'unavailable', 1, $3,
			'gpt-5.6-sol', 'openai', 'inferred', 'exact', 'context_tiered', 0, 2000000, 'unavailable', 'synthetic-old')
	`, deviceID, string(bytes.Repeat([]byte("c"), 64)), bytes.Repeat([]byte{0x55}, 32)); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var tier, band *string
	var hash []byte
	if err := pool.QueryRow(ctx, `SELECT service_tier, prompt_band, payload_hash FROM usage_buckets`).Scan(&tier, &band, &hash); err != nil {
		t.Fatal(err)
	}
	if tier != nil || band != nil || !bytes.Equal(hash, bytes.Repeat([]byte{0x55}, 32)) {
		t.Fatalf("migrated row = tier %v band %v hash %x; want unproven evidence and an untouched payload hash", tier, band, hash)
	}

	_, registry, err := modelcatalog.Bundled()
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsurePricingRevision(ctx, pool, registry); err != nil {
		t.Fatal(err)
	}
	if result, err := RepriceUsage(ctx, pool, bundledEngine(t)); err != nil || result.Repriced != 1 {
		t.Fatalf("RepriceUsage() = %#v, %v; want the stored row", result, err)
	}
	var reason, state, revision string
	if err := pool.QueryRow(ctx, `SELECT unpriced_reason, api_estimate_state, pricing_revision FROM usage_buckets`).Scan(&reason, &state, &revision); err != nil {
		t.Fatal(err)
	}
	if reason != modelcatalog.ReasonPromptLengthUnproven || state != "unavailable" || revision != registry.Revision {
		t.Fatalf("repriced row = %s/%s/%s; usage without evidence must stay unpriced with the new reason", reason, state, revision)
	}

	// The schema checks the shape of evidence, not which names exist.
	for name, statement := range map[string]string{
		"a malformed service tier is rejected": `UPDATE usage_buckets SET service_tier = 'Not A Tier'`,
		"a malformed prompt band is rejected":  `UPDATE usage_buckets SET prompt_band = '272000 tokens'`,
	} {
		if _, err := pool.Exec(ctx, statement); pgErrorCode(err) != "23514" {
			t.Errorf("%s: error = %v, want a check violation", name, err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE usage_buckets SET service_tier = 'future_tier', prompt_band = 'le_1m'`); err != nil {
		t.Fatalf("a well-formed future tier was rejected by the schema: %v", err)
	}
}
