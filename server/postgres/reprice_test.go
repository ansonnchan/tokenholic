package postgres

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/modelcatalog"
	"github.com/ansonnchan/tokenholic/internal/normalize"
	serverpricing "github.com/ansonnchan/tokenholic/server/pricing"
	serversync "github.com/ansonnchan/tokenholic/server/sync"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Four buckets of one million tokens per category are uploaded under revision
// r1 and then repriced. Expected values are worked by hand in micro-dollars.
//
//	r1: model-a is $2 input, $0.10 read, $2.50 write, $4 one-hour write, $10 output.
//	    model-b is not in the catalog.
//	r2: model-a output rises to $20. model-b appears at $1 input, $0.05 read,
//	    $1.25 write, $5 output, with no one-hour rate.
func TestPostgresRepriceUsageFollowsThePricingRevision(t *testing.T) {
	pool := integrationPool(t)
	ctx := t.Context()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	first, firstRegistry := repriceEngine(t, "reprice-r1", 10_000_000, false)
	second, secondRegistry := repriceEngine(t, "reprice-r2", 20_000_000, true)
	if err := EnsurePricingRevision(ctx, pool, firstRegistry); err != nil {
		t.Fatal(err)
	}
	principal := registerSyncDevice(t, pool, 901, "70000000-0000-4000-8000-0000000000")
	repository, err := NewUsageRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	service, err := serversync.NewService(staticLeaderboardAuthenticator{principal: principal}, repository, first)
	if err != nil {
		t.Fatal(err)
	}
	million := uint64(1_000_000)
	if _, err := service.Apply(ctx, "credential", clientBatch(t, []clientUsage{
		{minute: 0, model: "model-a", evidence: normalize.ModelReported, each: million, split: true},
		{minute: 1, model: "model-a", evidence: normalize.ModelReported, each: million},
		{minute: 2, model: "model-b", evidence: normalize.ModelReported, each: million, split: true},
		{minute: 3, model: "", evidence: normalize.ModelUnknown, each: million},
	})); err != nil {
		t.Fatal(err)
	}

	// r1: 18.6 (split) + 17.1 (combined, lower bound) = $35.70; model-b and
	// the model-less bucket are unpriced.
	wantFirst := repriceTotals{estimate: "35700000", priced: "10000000", unpriced: "10000000", tokens: "20000000", revisions: "reprice-r1"}
	assertRepriceTotals(t, pool, principal.DeviceID, wantFirst)
	firstSnapshot := derivedSnapshot(t, pool, principal.DeviceID)
	evidence := evidenceSnapshot(t, pool, principal.DeviceID)

	// The active revision already derived every row: nothing to do.
	if result, err := RepriceUsage(ctx, pool, first); err != nil || result.Repriced != 0 || result.Revision != "reprice-r1" {
		t.Fatalf("reprice at the stored revision = %#v, %v; want no rows", result, err)
	}

	// An unregistered revision cannot be applied, and nothing changes.
	if _, err := RepriceUsage(ctx, pool, second); pgErrorCode(err) != "23503" {
		t.Fatalf("reprice to an unregistered revision error = %v, want a foreign-key violation", err)
	}
	if got := derivedSnapshot(t, pool, principal.DeviceID); got != firstSnapshot {
		t.Fatalf("a failed reprice changed stored rows:\n%s", got)
	}

	if err := EnsurePricingRevision(ctx, pool, secondRegistry); err != nil {
		t.Fatal(err)
	}
	// A pricer that fails partway through the second batch leaves no row behind.
	failing := &failingPricer{UsagePricer: second, failAt: 3}
	if _, err := repriceUsage(ctx, pool, failing, 2); err == nil {
		t.Fatal("repriceUsage() succeeded with a failing pricer")
	}
	if got := derivedSnapshot(t, pool, principal.DeviceID); got != firstSnapshot {
		t.Fatalf("an interrupted reprice left a mix of revisions:\n%s", got)
	}

	// r2, in batches of two to cross a batch boundary.
	result, err := repriceUsage(ctx, pool, second, 2)
	if err != nil || result.Repriced != 4 || result.Revision != "reprice-r2" {
		t.Fatalf("reprice to r2 = %#v, %v; want 4 rows", result, err)
	}
	// model-a: 28.6 + 27.1. model-b: 1 + 0.05 + 1.25 + 5 = 7.3, with its
	// one-hour million unpriced. Total $63.00.
	wantSecond := repriceTotals{estimate: "63000000", priced: "14000000", unpriced: "6000000", tokens: "20000000", revisions: "reprice-r2"}
	assertRepriceTotals(t, pool, principal.DeviceID, wantSecond)
	wantRows := []string{
		"model-a|true|model-a|provider/model-a|reported|exact|<nil>|28600000|complete|5000000|0|reprice-r2",
		"model-a|false|model-a|provider/model-a|reported|exact|<nil>|27100000|partial|5000000|0|reprice-r2",
		"model-b|true|model-b|provider/model-b|reported|exact|<nil>|7300000|partial|4000000|1000000|reprice-r2",
		"|false|<nil>|<nil>|unknown|unknown|model_unavailable|<nil>|unavailable|0|5000000|reprice-r2",
	}
	secondSnapshot := derivedSnapshot(t, pool, principal.DeviceID)
	if secondSnapshot != strings.Join(wantRows, "\n") {
		t.Fatalf("repriced rows =\n%s\nwant\n%s", secondSnapshot, strings.Join(wantRows, "\n"))
	}
	// Repricing derives; it never touches what the client reported.
	if got := evidenceSnapshot(t, pool, principal.DeviceID); got != evidence {
		t.Fatalf("repricing changed client evidence:\nbefore %s\nafter  %s", evidence, got)
	}

	// Repricing twice gives identical totals and rows.
	if result, err := RepriceUsage(ctx, pool, second); err != nil || result.Repriced != 0 {
		t.Fatalf("second reprice to r2 = %#v, %v; want no rows", result, err)
	}
	assertRepriceTotals(t, pool, principal.DeviceID, wantSecond)
	if got := derivedSnapshot(t, pool, principal.DeviceID); got != secondSnapshot {
		t.Fatalf("a repeated reprice changed stored rows:\n%s", got)
	}

	// The account view reads the repriced values.
	accounts, err := NewAccountRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := accounts.AccountUsage(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	if usage.APIEstimate.Value == nil || *usage.APIEstimate.Value != "63000000" || usage.TotalTokens.Value == nil || *usage.TotalTokens.Value != "20000000" {
		t.Fatalf("account usage after repricing = estimate %#v, tokens %#v", usage.APIEstimate, usage.TotalTokens)
	}

	// Derivation is a pure function of evidence and revision, so returning to
	// r1 reproduces the original rows exactly.
	if result, err := RepriceUsage(ctx, pool, first); err != nil || result.Repriced != 4 {
		t.Fatalf("reprice back to r1 = %#v, %v; want 4 rows", result, err)
	}
	assertRepriceTotals(t, pool, principal.DeviceID, wantFirst)
	if got := derivedSnapshot(t, pool, principal.DeviceID); got != firstSnapshot {
		t.Fatalf("returning to r1 did not reproduce the original rows:\n%s\nwant\n%s", got, firstSnapshot)
	}
}

func TestRepriceUsageRequiresAPoolAndRevision(t *testing.T) {
	t.Parallel()
	if _, err := RepriceUsage(t.Context(), nil, nil); err == nil {
		t.Fatal("RepriceUsage() accepted missing dependencies")
	}
}

type repriceTotals struct {
	estimate, priced, unpriced, tokens, revisions string
}

func assertRepriceTotals(t *testing.T, pool *pgxpool.Pool, deviceID string, want repriceTotals) {
	t.Helper()
	var got repriceTotals
	if err := pool.QueryRow(t.Context(), `
		SELECT COALESCE(SUM(api_estimate_microusd), 0)::text, SUM(priced_tokens)::text, SUM(unpriced_tokens)::text,
		       SUM(COALESCE(input_uncached_tokens,0)+COALESCE(input_cache_read_tokens,0)+COALESCE(input_cache_write_tokens,0)+COALESCE(output_tokens,0))::text,
		       string_agg(DISTINCT pricing_revision, ',')
		FROM usage_buckets WHERE device_id = $1::uuid`, deviceID).Scan(&got.estimate, &got.priced, &got.unpriced, &got.tokens, &got.revisions); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("totals = %+v, want %+v", got, want)
	}
}

// derivedSnapshot renders every server-derived column, one bucket per line.
func derivedSnapshot(t *testing.T, pool *pgxpool.Pool, deviceID string) string {
	t.Helper()
	return snapshotQuery(t, pool, deviceID, `
		SELECT concat_ws('|', observed_model_id, (input_cache_write_5m_tokens IS NOT NULL)::text,
		       COALESCE(canonical_model_id, '<nil>'), COALESCE(pricing_key, '<nil>'), model_resolution, model_match,
		       COALESCE(unpriced_reason, '<nil>'), COALESCE(api_estimate_microusd::text, '<nil>'), api_estimate_state,
		       priced_tokens::text, unpriced_tokens::text, pricing_revision)
		FROM usage_buckets WHERE device_id = $1::uuid ORDER BY bucket_start`)
}

// evidenceSnapshot renders everything the client reported plus receipt state.
func evidenceSnapshot(t *testing.T, pool *pgxpool.Pool, deviceID string) string {
	t.Helper()
	return snapshotQuery(t, pool, deviceID, `
		SELECT concat_ws('|', bucket_key, bucket_start::text, source, surface, observed_model_id, model_evidence,
		       measurement, normalization_version, COALESCE(input_uncached_tokens::text, '-'), input_uncached_state,
		       COALESCE(input_cache_read_tokens::text, '-'), COALESCE(input_cache_write_tokens::text, '-'),
		       COALESCE(input_cache_write_5m_tokens::text, '-'), input_cache_write_5m_state,
		       COALESCE(input_cache_write_1h_tokens::text, '-'), COALESCE(output_tokens::text, '-'),
		       revision::text, encode(payload_hash, 'hex'), received_at::text,
		       COALESCE(service_tier, '-'), COALESCE(prompt_band, '-'))
		FROM usage_buckets WHERE device_id = $1::uuid ORDER BY bucket_start`)
}

func snapshotQuery(t *testing.T, pool *pgxpool.Pool, deviceID, query string) string {
	t.Helper()
	rows, err := pool.Query(t.Context(), query, deviceID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}

// repriceEngine builds a synthetic catalog revision. withModelB adds a second
// model that publishes no one-hour cache-write rate.
func repriceEngine(t *testing.T, revision string, outputRate uint64, withModelB bool) (*serverpricing.Engine, *modelcatalog.PricingRegistry) {
	t.Helper()
	rate := func(v uint64) *uint64 { return &v }
	effective := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	models := []modelcatalog.Model{{CanonicalID: "model-a", Provider: "provider", PricingKey: "provider/model-a"}}
	prices := []modelcatalog.Price{{
		Key: "provider/model-a", Provider: "provider",
		Rate: modelcatalog.Rate{
			InputUncached: rate(2_000_000), InputCacheRead: rate(100_000), InputCacheWrite: rate(2_500_000),
			InputCacheWrite1h: rate(4_000_000), Output: &outputRate,
		},
		SourceURL: "https://provider.invalid/pricing", SourceRevision: "synthetic", EffectiveDate: effective,
	}}
	if withModelB {
		models = append(models, modelcatalog.Model{CanonicalID: "model-b", Provider: "provider", PricingKey: "provider/model-b"})
		prices = append(prices, modelcatalog.Price{
			Key: "provider/model-b", Provider: "provider",
			Rate:      modelcatalog.Rate{InputUncached: rate(1_000_000), InputCacheRead: rate(50_000), InputCacheWrite: rate(1_250_000), Output: rate(5_000_000)},
			SourceURL: "https://provider.invalid/pricing", SourceRevision: "synthetic", EffectiveDate: effective,
		})
	}
	registry, err := modelcatalog.NewPricingRegistry(revision, prices)
	if err != nil {
		t.Fatal(err)
	}
	deriver, err := modelcatalog.NewDeriver(modelcatalog.Catalog{Revision: revision, Models: models}, registry)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := serverpricing.NewEngine(deriver)
	if err != nil {
		t.Fatal(err)
	}
	return engine, registry
}

// failingPricer fails on its Nth derivation to interrupt a reprice mid-flight.
type failingPricer struct {
	UsagePricer
	failAt int
	calls  int
}

func (f *failingPricer) Derive(usage serverpricing.Usage) (serverpricing.Derived, error) {
	f.calls++
	if f.calls == f.failAt {
		return serverpricing.Derived{}, errors.New("synthetic derivation failure")
	}
	return f.UsagePricer.Derive(usage)
}
