package store

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/modelcatalog"
	"github.com/ansonnchan/tokenholic/internal/normalize"
)

func TestBuildUsageBucketsPreservesPartialAndUnknownPricing(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 17, 20, 1, 10, 0, time.UTC)
	first := storedObservation(start, normalize.SourceClaude, normalize.SurfaceCLI, "model-a", 2, 3)
	second := storedObservation(start.Add(20*time.Second), normalize.SourceClaude, normalize.SurfaceCLI, "model-a", 5, 7)
	second.Tokens.InputCacheWrite = nil
	buckets, err := BuildUsageBuckets([]StoredObservation{first, second}, 1, syntheticPreviewer(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 {
		t.Fatalf("bucket count = %d, want 1", len(buckets))
	}
	bucket := buckets[0]
	if bucket.Start != start.Truncate(time.Minute) || bucket.Tokens.Output.Value == nil || *bucket.Tokens.Output.Value != 10 {
		t.Fatalf("bucket = %#v", bucket)
	}
	if bucket.Tokens.InputCacheWrite.State != AvailabilityPartial || bucket.Tokens.InputCacheWrite.Value == nil {
		t.Fatalf("cache-write coverage = %#v", bucket.Tokens.InputCacheWrite)
	}
	preview := bucket.Preview
	if preview.APIEstimate.State != AvailabilityPartial || preview.CatalogRevision != "synthetic-r1" ||
		preview.CanonicalModelID == nil || *preview.CanonicalModelID != "model-a" || preview.Resolution != normalize.ModelReported {
		t.Fatalf("preview = %#v", preview)
	}
	// 7 input + 2 cache read + 1 cache write + 10 output, all with a rate.
	if preview.PricedTokens != 20 || preview.UnpricedTokens != 0 {
		t.Fatalf("priced/unpriced tokens = %d/%d, want 20/0", preview.PricedTokens, preview.UnpricedTokens)
	}

	unknown := storedObservation(start, normalize.SourceCodex, normalize.SurfaceCLI, "unknown-model", 1, 1)
	unknown.ModelResolution = normalize.ModelInferred
	unknownBuckets, err := BuildUsageBuckets([]StoredObservation{unknown}, 1, syntheticPreviewer(t))
	if err != nil {
		t.Fatal(err)
	}
	unknownPreview := unknownBuckets[0].Preview
	if unknownPreview.APIEstimate.State != AvailabilityUnavailable || unknownPreview.APIEstimate.Value != nil ||
		unknownPreview.UnpricedReason != modelcatalog.ReasonUnknownModel || unknownPreview.UnpricedTokens != 4 {
		t.Fatalf("unknown pricing = %#v", unknownPreview)
	}
	// The upload still says how the source attributed the model.
	if unknownBuckets[0].ModelEvidence != normalize.ModelInferred || unknownPreview.Resolution != normalize.ModelUnknown {
		t.Fatalf("evidence/resolution = %q/%q", unknownBuckets[0].ModelEvidence, unknownPreview.Resolution)
	}
}

func TestUsageBucketPayloadCarriesObservedEvidenceOnly(t *testing.T) {
	t.Parallel()
	db := openStoreForTest(t)
	start := time.Date(2026, 11, 17, 20, 1, 0, 0, time.UTC)
	buckets, err := BuildUsageBuckets([]StoredObservation{
		storedObservation(start, normalize.SourceClaude, normalize.SurfaceCLI, "model-a", 2, 3),
	}, 1, syntheticPreviewer(t))
	if err != nil {
		t.Fatal(err)
	}
	if buckets[0].Preview.APIEstimate.Value == nil {
		t.Fatal("test bucket has no preview estimate to leak")
	}
	if _, err := db.ApplyBucketRevisions(t.Context(), buckets); err != nil {
		t.Fatal(err)
	}
	outbox, err := db.PendingOutbox(t.Context())
	if err != nil || len(outbox) != 1 {
		t.Fatalf("outbox = %#v, %v", outbox, err)
	}
	var payload struct {
		BucketKey string                     `json:"bucket_key"`
		Bucket    map[string]json.RawMessage `json:"bucket"`
	}
	if err := json.Unmarshal(outbox[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	want := []string{"measurement", "model_evidence", "normalization_version", "observed_model_id", "revision", "source", "start", "surface", "tokens"}
	got := make([]string, 0, len(payload.Bucket))
	for field := range payload.Bucket {
		got = append(got, field)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("uploaded bucket fields = %v, want exactly %v", got, want)
	}
	for _, leaked := range []string{"canonical", "provider", "pricing", "estimate", "microusd", "priced", "catalog_revision"} {
		if strings.Contains(string(outbox[0].Payload), leaked) {
			t.Fatalf("upload payload contains client-derived %q: %s", leaked, outbox[0].Payload)
		}
	}
}

func TestUnchangedBucketKeepsItsRevisionAndStaysAcknowledged(t *testing.T) {
	t.Parallel()
	db := openStoreForTest(t)
	start := time.Date(2026, 11, 17, 20, 1, 0, 0, time.UTC)
	observations := []StoredObservation{storedObservation(start, normalize.SourceClaude, normalize.SurfaceCLI, "model-a", 2, 3)}
	if result, err := db.rebuildForTest(t, observations, 1, syntheticPreviewer(t)); err != nil || result.Applied != 1 {
		t.Fatalf("first rebuild = %#v, %v", result, err)
	}
	outbox, err := db.PendingOutbox(t.Context())
	if err != nil || len(outbox) != 1 {
		t.Fatalf("outbox = %#v, %v", outbox, err)
	}
	if _, err := db.AcknowledgeOutbox(t.Context(), []OutboxAcknowledgement{{BucketKey: outbox[0].BucketKey, Revision: 1}}); err != nil {
		t.Fatal(err)
	}

	// A later refresh proposes a higher revision for identical content, and a
	// newer bundled catalog changes only the local preview.
	repriced := syntheticPreviewerAt(t, "synthetic-r2", 20_000_000)
	if result, err := db.rebuildForTest(t, observations, 2, repriced); err != nil || result.Applied != 0 || result.Ignored != 1 {
		t.Fatalf("unchanged rebuild = %#v, %v", result, err)
	}
	if count, err := db.PendingOutboxCount(t.Context()); err != nil || count != 0 {
		t.Fatalf("pending after unchanged rebuild = %d, %v; want nothing to upload", count, err)
	}
	if revision, err := db.NextUsageBucketRevision(t.Context()); err != nil || revision != 2 {
		t.Fatalf("next revision = %d, %v; want 2 because the bucket kept revision 1", revision, err)
	}
	summary, err := db.QueryUsage(t.Context(), PeriodAllTime, start.Add(time.Hour), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	// 2 input*$2 + 1 read*$0.10 + 1 write*$2.50 + 3 output*$20 = 66.6, floored
	// to 66 micro-USD. At the old $10 output rate it was 36.
	if summary.APIEstimate.Value == nil || *summary.APIEstimate.Value != 66 {
		t.Fatalf("refreshed preview estimate = %#v", summary.APIEstimate)
	}

	// Changed usage does upload again, at the new revision.
	observations = append(observations, storedObservation(start.Add(time.Second), normalize.SourceClaude, normalize.SurfaceCLI, "model-a", 1, 1))
	if result, err := db.rebuildForTest(t, observations, 2, repriced); err != nil || result.Applied != 1 {
		t.Fatalf("changed rebuild = %#v, %v", result, err)
	}
	outbox, err = db.PendingOutbox(t.Context())
	if err != nil || len(outbox) != 1 || outbox[0].Revision != 2 {
		t.Fatalf("outbox after change = %#v, %v", outbox, err)
	}
}

// Buckets are absolute. When the observations behind one disappear, the
// bucket must be replaced by zero locally and re-uploaded, or the server would
// keep counting usage the client no longer reports.
func TestRebuildRetractsBucketsThatNoLongerHaveUsage(t *testing.T) {
	t.Parallel()
	db := openStoreForTest(t)
	kept := sampleBatch()
	removed := sampleBatch()
	removed.Observations[0].SourceRecordID = "synthetic-record-to-remove"
	removed.Observations[0].OccurredAt = kept.Observations[0].OccurredAt.Add(5 * time.Minute)
	removed.Observations[0].Tokens = normalize.TokenCounts{InputUncached: func() *uint64 { v := uint64(900); return &v }()}
	for _, batch := range []ObservationBatch{kept, removed} {
		if _, err := db.CommitObservations(t.Context(), batch); err != nil {
			t.Fatal(err)
		}
	}
	if result, err := db.RebuildUsageBuckets(t.Context(), 1, syntheticPreviewer(t)); err != nil || result.Applied != 2 {
		t.Fatalf("first rebuild = %#v, %v", result, err)
	}
	outbox, err := db.PendingOutbox(t.Context())
	if err != nil || len(outbox) != 2 {
		t.Fatalf("outbox = %d records, %v", len(outbox), err)
	}
	var acknowledgements []OutboxAcknowledgement
	for _, record := range outbox {
		acknowledgements = append(acknowledgements, OutboxAcknowledgement{BucketKey: record.BucketKey, Revision: record.Revision})
	}
	if _, err := db.AcknowledgeOutbox(t.Context(), acknowledgements); err != nil {
		t.Fatal(err)
	}
	now := removed.Observations[0].OccurredAt.Add(time.Hour)
	before, err := db.QueryUsage(t.Context(), PeriodAllTime, now, time.UTC)
	if err != nil || before.Tokens.InputUncached.Value == nil || *before.Tokens.InputUncached.Value != 900 {
		t.Fatalf("usage before removal = %#v, %v", before.Tokens.InputUncached, err)
	}

	// A corrected adapter stops producing one record.
	if _, err := db.sql.ExecContext(t.Context(), `DELETE FROM observations WHERE identity = ?`, observationIdentity(removed.Observations[0])); err != nil {
		t.Fatal(err)
	}
	result, err := db.RebuildUsageBuckets(t.Context(), 2, syntheticPreviewer(t))
	if err != nil || result.Applied != 1 || result.Ignored != 1 {
		t.Fatalf("rebuild after removal = %#v, %v; want one retraction and one unchanged bucket", result, err)
	}
	outbox, err = db.PendingOutbox(t.Context())
	if err != nil || len(outbox) != 1 || outbox[0].Revision != 2 {
		t.Fatalf("outbox after removal = %#v, %v; want the retraction pending at revision 2", outbox, err)
	}
	var uploaded struct {
		Bucket struct {
			Tokens map[string]struct {
				Value *uint64 `json:"value"`
				State string  `json:"state"`
			} `json:"tokens"`
		} `json:"bucket"`
	}
	if err := json.Unmarshal(outbox[0].Payload, &uploaded); err != nil {
		t.Fatal(err)
	}
	if len(uploaded.Bucket.Tokens) != 7 {
		t.Fatalf("retraction token categories = %d, want 7", len(uploaded.Bucket.Tokens))
	}
	for category, value := range uploaded.Bucket.Tokens {
		if value.Value == nil || *value.Value != 0 || value.State != string(AvailabilityComplete) {
			t.Fatalf("retraction %s = %v/%s, want a known zero", category, value.Value, value.State)
		}
	}
	after, err := db.QueryUsage(t.Context(), PeriodAllTime, now, time.UTC)
	if err != nil || after.Tokens.InputUncached.Value == nil || *after.Tokens.InputUncached.Value != 0 {
		t.Fatalf("usage after removal = %#v, %v; want the removed 900 tokens gone", after.Tokens.InputUncached, err)
	}

	// Retracting again changes nothing and uploads nothing new.
	if _, err := db.AcknowledgeOutbox(t.Context(), []OutboxAcknowledgement{{BucketKey: outbox[0].BucketKey, Revision: 2}}); err != nil {
		t.Fatal(err)
	}
	if result, err := db.RebuildUsageBuckets(t.Context(), 3, syntheticPreviewer(t)); err != nil || result.Applied != 0 || result.Ignored != 2 {
		t.Fatalf("repeated rebuild = %#v, %v; want nothing applied", result, err)
	}
	if count, err := db.PendingOutboxCount(t.Context()); err != nil || count != 0 {
		t.Fatalf("pending after repeated rebuild = %d, %v", count, err)
	}
}

func TestBucketEstimateUsesCacheWriteLifetimes(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 11, 17, 20, 1, 0, 0, time.UTC)
	value := func(v uint64) *uint64 { return &v }
	// Synthetic rates per million: $2 input, $0.10 read, $2.50 base write,
	// $4 one-hour write, $10 output. Each case writes a million cache tokens.
	tests := []struct {
		name                string
		fiveMinute, oneHour *uint64
		wantMicroUSD        uint64
		wantState           Availability
	}{
		{name: "one-hour writes", fiveMinute: value(0), oneHour: value(1_000_000), wantMicroUSD: 4_000_000, wantState: AvailabilityComplete},
		{name: "five-minute writes", fiveMinute: value(1_000_000), oneHour: value(0), wantMicroUSD: 2_500_000, wantState: AvailabilityComplete},
		{name: "unknown lifetime is a lower bound", wantMicroUSD: 2_500_000, wantState: AvailabilityPartial},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation := storedObservation(start, normalize.SourceClaude, normalize.SurfaceCLI, "model-a", 0, 0)
			observation.Tokens.InputCacheRead = value(0)
			observation.Tokens.InputCacheWrite = value(1_000_000)
			observation.Tokens.InputCacheWrite5m, observation.Tokens.InputCacheWrite1h = tt.fiveMinute, tt.oneHour
			buckets, err := BuildUsageBuckets([]StoredObservation{observation}, 1, syntheticPreviewer(t))
			if err != nil {
				t.Fatal(err)
			}
			estimate := buckets[0].Preview.APIEstimate
			if estimate.Value == nil || *estimate.Value != tt.wantMicroUSD || estimate.State != tt.wantState {
				t.Fatalf("estimate = %#v, want %d micro-USD %s", estimate, tt.wantMicroUSD, tt.wantState)
			}
		})
	}
}

func TestBucketReplayReplacementAndOutOfOrderRevision(t *testing.T) {
	t.Parallel()
	db := openStoreForTest(t)
	bucket := completeBucket(time.Date(2026, 11, 17, 20, 0, 0, 0, time.UTC), normalize.SourceClaude, normalize.SurfaceCLI, "model-a", 10, 1)
	if result, err := db.ApplyBucketRevisions(t.Context(), []UsageBucket{bucket}); err != nil || result.Applied != 1 {
		t.Fatalf("first apply = %#v, %v", result, err)
	}
	if result, err := db.ApplyBucketRevisions(t.Context(), []UsageBucket{bucket}); err != nil || result.Ignored != 1 {
		t.Fatalf("replay = %#v, %v", result, err)
	}
	newer := bucket
	newer.Revision = 3
	setAggregate(&newer.Tokens.Output, 30, AvailabilityComplete)
	if result, err := db.ApplyBucketRevisions(t.Context(), []UsageBucket{newer}); err != nil || result.Applied != 1 {
		t.Fatalf("replacement = %#v, %v", result, err)
	}
	older := bucket
	older.Revision = 2
	setAggregate(&older.Tokens.Output, 20, AvailabilityComplete)
	if result, err := db.ApplyBucketRevisions(t.Context(), []UsageBucket{older}); err != nil || result.Ignored != 1 {
		t.Fatalf("out-of-order apply = %#v, %v", result, err)
	}
	summary, err := db.QueryUsage(t.Context(), PeriodAllTime, time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Tokens.Output.Value == nil || *summary.Tokens.Output.Value != 30 {
		t.Fatalf("replacement output = %#v, want 30", summary.Tokens.Output)
	}
	outbox, err := db.PendingOutbox(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(outbox) != 1 || outbox[0].Revision != 3 || strings.Contains(string(outbox[0].Payload), "source_record") {
		t.Fatalf("outbox = %#v", outbox)
	}
	if count, err := db.AcknowledgeOutbox(t.Context(), []OutboxAcknowledgement{{BucketKey: outbox[0].BucketKey, Revision: 2}}); err != nil || count != 0 {
		t.Fatalf("stale acknowledgement = %d, %v", count, err)
	}
	if count, err := db.AcknowledgeOutbox(t.Context(), []OutboxAcknowledgement{{BucketKey: outbox[0].BucketKey, Revision: 3}}); err != nil || count != 1 {
		t.Fatalf("current acknowledgement = %d, %v", count, err)
	}
	if count, err := db.PendingOutboxCount(t.Context()); err != nil || count != 0 {
		t.Fatalf("pending after acknowledgement = %d, %v", count, err)
	}
	conflict := newer
	setAggregate(&conflict.Tokens.Output, 31, AvailabilityComplete)
	if _, err := db.ApplyBucketRevisions(t.Context(), []UsageBucket{conflict}); !errors.Is(err, ErrBucketRevisionConflict) {
		t.Fatalf("same-revision conflict error = %v", err)
	}
}

func TestUsageBucketsSurviveRestart(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), databaseFileName)
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	bucket := completeBucket(time.Date(2026, 11, 17, 20, 0, 0, 0, time.UTC), normalize.SourceCodex, normalize.SurfaceSharedLocal, "model-a", 5, 1)
	if _, err := db.ApplyBucketRevisions(t.Context(), []UsageBucket{bucket}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close(context.Background()) })
	if count, err := reopened.PendingOutboxCount(t.Context()); err != nil || count != 1 {
		t.Fatalf("PendingOutboxCount() = %d, %v", count, err)
	}
	summary, err := reopened.QueryUsage(t.Context(), PeriodAllTime, time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC), time.UTC)
	if err != nil || summary.Tokens.Output.Value == nil || *summary.Tokens.Output.Value != 5 {
		t.Fatalf("restart summary = %#v, %v", summary, err)
	}
}

func TestNextUsageBucketRevisionUsesStoredMaximum(t *testing.T) {
	t.Parallel()
	db := openStoreForTest(t)
	if revision, err := db.NextUsageBucketRevision(t.Context()); err != nil || revision != 1 {
		t.Fatalf("empty NextUsageBucketRevision() = %d, %v", revision, err)
	}
	start := time.Date(2026, 11, 17, 20, 0, 0, 0, time.UTC)
	buckets := []UsageBucket{
		completeBucket(start, normalize.SourceClaude, normalize.SurfaceCLI, "model-a", 1, 3),
		completeBucket(start, normalize.SourceCodex, normalize.SurfaceSharedLocal, "model-b", 1, 9),
	}
	if _, err := db.ApplyBucketRevisions(t.Context(), buckets); err != nil {
		t.Fatal(err)
	}
	if revision, err := db.NextUsageBucketRevision(t.Context()); err != nil || revision != 10 {
		t.Fatalf("NextUsageBucketRevision() = %d, %v; want 10, nil", revision, err)
	}
	exhausted := completeBucket(start.Add(time.Minute), normalize.SourceClaude, normalize.SurfaceCLI, "model-c", 1, math.MaxUint64)
	if _, err := db.ApplyBucketRevisions(t.Context(), []UsageBucket{exhausted}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.NextUsageBucketRevision(t.Context()); err == nil {
		t.Fatal("NextUsageBucketRevision() succeeded after maximum revision")
	}
}

func TestTimezoneAwarePeriodsAndBreakdowns(t *testing.T) {
	t.Parallel()
	location, err := time.LoadLocation("America/Vancouver")
	if err != nil {
		t.Fatal(err)
	}
	db := openStoreForTest(t)
	// March 8, 2026 starts at 08:00 UTC in Vancouver before the DST jump.
	buckets := []UsageBucket{
		completeBucket(time.Date(2026, 3, 8, 7, 59, 0, 0, time.UTC), normalize.SourceClaude, normalize.SurfaceCLI, "before", 100, 1),
		completeBucket(time.Date(2026, 3, 8, 8, 0, 0, 0, time.UTC), normalize.SourceClaude, normalize.SurfaceDesktopCode, "today-a", 3, 1),
		completeBucket(time.Date(2026, 3, 8, 9, 0, 0, 0, time.UTC), normalize.SourceCodex, normalize.SurfaceSharedLocal, "today-b", 4, 1),
	}
	if _, err := db.ApplyBucketRevisions(t.Context(), buckets); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)
	summary, err := db.QueryUsage(t.Context(), PeriodToday, now, location)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Start == nil || !summary.Start.Equal(time.Date(2026, 3, 8, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("today start = %v", summary.Start)
	}
	if summary.Tokens.Output.Value == nil || *summary.Tokens.Output.Value != 7 {
		t.Fatalf("today output = %#v, want 7", summary.Tokens.Output)
	}
	if len(summary.Models) != 2 || len(summary.Sources) != 2 || len(summary.Surfaces) != 2 {
		t.Fatalf("breakdowns = models:%#v sources:%#v surfaces:%#v", summary.Models, summary.Sources, summary.Surfaces)
	}
	weekStart, _, err := periodBounds(PeriodWeek, time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC), location)
	if err != nil || weekStart == nil || weekStart.In(location).Weekday() != time.Monday || weekStart.In(location).Hour() != 0 {
		t.Fatalf("week start = %v, %v", weekStart, err)
	}
	thirtyStart, _, err := periodBounds(PeriodLast30Days, now, location)
	if err != nil || thirtyStart == nil || thirtyStart.In(location).Day() != 7 || thirtyStart.In(location).Month() != time.February {
		t.Fatalf("30-day start = %v, %v", thirtyStart, err)
	}
}

func TestUsageAggregationRejectsOverflow(t *testing.T) {
	t.Parallel()
	db := openStoreForTest(t)
	first := completeBucket(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), normalize.SourceClaude, normalize.SurfaceCLI, "one", math.MaxUint64, 1)
	second := completeBucket(time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC), normalize.SourceClaude, normalize.SurfaceCLI, "two", 1, 1)
	if _, err := db.ApplyBucketRevisions(t.Context(), []UsageBucket{first, second}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.QueryUsage(t.Context(), PeriodAllTime, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), time.UTC); err == nil {
		t.Fatal("overflowing query succeeded")
	}
}

func storedObservation(at time.Time, source normalize.Source, surface normalize.Surface, model string, input, output uint64) StoredObservation {
	cacheRead, cacheWrite := uint64(1), uint64(1)
	return StoredObservation{
		Source: source, Surface: surface, OccurredAt: at, ObservedModelID: model,
		ModelResolution: normalize.ModelReported, Measurement: normalize.MeasurementReported, NormalizationVersion: "1",
		Tokens: normalize.TokenCounts{InputUncached: &input, InputCacheRead: &cacheRead, InputCacheWrite: &cacheWrite, Output: &output},
	}
}

// syntheticPreviewer prices "model-a" at $2 input, $0.10 cache read, $2.50
// base cache write, $4 one-hour cache write, and $10 output per million.
func syntheticPreviewer(t *testing.T) *modelcatalog.Deriver {
	t.Helper()
	return syntheticPreviewerAt(t, "synthetic-r1", 10_000_000)
}

func syntheticPreviewerAt(t *testing.T, revision string, outputRate uint64) *modelcatalog.Deriver {
	t.Helper()
	input, read, write, writeHour := uint64(2_000_000), uint64(100_000), uint64(2_500_000), uint64(4_000_000)
	registry, err := modelcatalog.NewPricingRegistry(revision, []modelcatalog.Price{{
		Key: "synthetic/model-a", Provider: "synthetic",
		Rate:      modelcatalog.Rate{InputUncached: &input, InputCacheRead: &read, InputCacheWrite: &write, InputCacheWrite1h: &writeHour, Output: &outputRate},
		SourceURL: "https://synthetic.invalid/pricing", SourceRevision: "synthetic-r1", EffectiveDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}})
	if err != nil {
		t.Fatal(err)
	}
	deriver, err := modelcatalog.NewDeriver(modelcatalog.Catalog{
		Revision: revision,
		Models:   []modelcatalog.Model{{CanonicalID: "model-a", Provider: "synthetic", PricingKey: "synthetic/model-a"}},
	}, registry)
	if err != nil {
		t.Fatal(err)
	}
	return deriver
}

func (db *DB) rebuildForTest(t *testing.T, observations []StoredObservation, revision uint64, previewer Previewer) (BucketApplyResult, error) {
	t.Helper()
	buckets, err := BuildUsageBuckets(observations, revision, previewer)
	if err != nil {
		return BucketApplyResult{}, err
	}
	return db.ApplyBucketRevisions(t.Context(), buckets)
}

func completeBucket(start time.Time, source normalize.Source, surface normalize.Surface, model string, output, revision uint64) UsageBucket {
	zero := uint64(0)
	complete := AggregateValue{Value: &zero, State: AvailabilityComplete}
	return UsageBucket{
		Start: start, Source: source, Surface: surface, ObservedModelID: model,
		ModelEvidence: normalize.ModelReported, Measurement: normalize.MeasurementReported, NormalizationVersion: "1",
		Tokens: TokenBreakdown{
			InputUncached: complete, InputCacheRead: complete, InputCacheWrite: complete,
			InputCacheWrite5m: complete, InputCacheWrite1h: complete,
			Output: AggregateValue{Value: &output, State: AvailabilityComplete}, ReasoningOutput: AggregateValue{State: AvailabilityUnavailable},
		},
		Preview:  Preview{Resolution: normalize.ModelUnknown, APIEstimate: AggregateValue{State: AvailabilityUnavailable}},
		Revision: revision,
	}
}

func setAggregate(value *AggregateValue, number uint64, state Availability) {
	value.Value = &number
	value.State = state
}

// A bucket without pricing evidence must be exactly what it was before the
// evidence existed, or every stored bucket would be uploaded again. The
// expected values were computed by the previous release of this package.
func TestBucketWithoutPricingEvidenceKeepsItsKeyJSONAndContentHash(t *testing.T) {
	t.Parallel()
	bucket := completeBucket(time.Date(2026, 11, 18, 12, 1, 0, 0, time.UTC), normalize.SourceClaude, normalize.SurfaceCLI, "synthetic-model", 7, 7)
	const (
		wantKey     = "c2759133b96430065ed666ad7f77531790deb3464a38d8f472ecb7ba63878ab7"
		wantContent = "a87bd859ce9861497baa256528481d99a6855f49b04a969d6b13b4b99b83f0f8"
		wantJSON    = `{"bucket_key":"c2759133b96430065ed666ad7f77531790deb3464a38d8f472ecb7ba63878ab7","bucket":{"start":"2026-11-18T12:01:00Z","source":"claude","surface":"cli","observed_model_id":"synthetic-model","model_evidence":"reported","measurement":"reported","normalization_version":"1","tokens":{"input_uncached":{"value":0,"state":"complete"},"input_cache_read":{"value":0,"state":"complete"},"input_cache_write":{"value":0,"state":"complete"},"input_cache_write_5m":{"value":0,"state":"complete"},"input_cache_write_1h":{"value":0,"state":"complete"},"output":{"value":7,"state":"complete"},"reasoning_output":{"value":null,"state":"unavailable"}},"revision":7}}`
	)
	key := usageBucketKey(bucket)
	content, err := bucketContentHash(bucket)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(bucketPayload{BucketKey: key, Bucket: bucket})
	if err != nil {
		t.Fatal(err)
	}
	if key != wantKey || content != wantContent || string(payload) != wantJSON {
		t.Fatalf("bucket without evidence changed:\nkey     %s\ncontent %s\njson    %s", key, content, payload)
	}

	// Either kind of evidence gives the bucket its own identity and content.
	for name, mutate := range map[string]func(*UsageBucket){
		"service tier": func(b *UsageBucket) { b.ServiceTier = normalize.ServiceTierPriority },
		"prompt band":  func(b *UsageBucket) { b.PromptBand = normalize.PromptBandUpTo100K },
	} {
		changed := bucket
		mutate(&changed)
		changedContent, err := bucketContentHash(changed)
		if err != nil {
			t.Fatal(err)
		}
		if usageBucketKey(changed) == wantKey || changedContent == wantContent {
			t.Fatalf("%s did not change the bucket's key and content hash", name)
		}
	}
}

// Requests that differ only in pricing evidence must not share a bucket: the
// server prices a bucket as a whole, from one rate card.
func TestBuildUsageBucketsSeparatesPricingEvidence(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 18, 12, 1, 0, 0, time.UTC)
	size := func(v uint64) *uint64 { return &v }
	request := func(second int, tier normalize.ServiceTier, prompt *uint64) StoredObservation {
		observation := storedObservation(at.Add(time.Duration(second)*time.Second), normalize.SourceCodex, normalize.SurfaceCLI, "model-a", 10, 5)
		observation.ServiceTier, observation.PromptTokens = tier, prompt
		return observation
	}
	buckets, err := BuildUsageBuckets([]StoredObservation{
		request(1, normalize.ServiceTierPriority, size(50_000)),
		request(2, normalize.ServiceTierPriority, size(90_000)), // same tier and band as the first
		request(3, normalize.ServiceTierPriority, size(280_000)),
		request(4, normalize.ServiceTierStandard, size(50_000)),
		request(5, normalize.ServiceTierUnknown, size(50_000)),
		request(6, normalize.ServiceTierUnknown, nil),
	}, 1, syntheticPreviewer(t))
	if err != nil {
		t.Fatal(err)
	}
	type evidence struct {
		tier normalize.ServiceTier
		band normalize.PromptBand
	}
	got := map[evidence]uint64{}
	keys := map[string]bool{}
	for _, bucket := range buckets {
		got[evidence{bucket.ServiceTier, bucket.PromptBand}] += *bucket.Tokens.InputUncached.Value
		keys[usageBucketKey(bucket)] = true
		payload, err := json.Marshal(bucket)
		if err != nil {
			t.Fatal(err)
		}
		// Absent evidence is omitted, never written as an empty or default value.
		if hasTier := strings.Contains(string(payload), `"service_tier"`); hasTier != (bucket.ServiceTier != "") {
			t.Fatalf("bucket JSON service_tier presence = %t for tier %q: %s", hasTier, bucket.ServiceTier, payload)
		}
		if hasBand := strings.Contains(string(payload), `"prompt_band"`); hasBand != (bucket.PromptBand != "") {
			t.Fatalf("bucket JSON prompt_band presence = %t for band %q: %s", hasBand, bucket.PromptBand, payload)
		}
	}
	want := map[evidence]uint64{
		{normalize.ServiceTierPriority, normalize.PromptBandUpTo100K}: 20,
		{normalize.ServiceTierPriority, normalize.PromptBandOver272K}: 10,
		{normalize.ServiceTierStandard, normalize.PromptBandUpTo100K}: 10,
		{normalize.ServiceTierUnknown, normalize.PromptBandUpTo100K}:  10,
		{normalize.ServiceTierUnknown, normalize.PromptBandUnknown}:   10,
	}
	if len(buckets) != len(want) || len(keys) != len(want) {
		t.Fatalf("buckets = %d with %d keys, want %d distinct", len(buckets), len(keys), len(want))
	}
	for key, tokens := range want {
		if got[key] != tokens {
			t.Fatalf("bucket %q/%q input = %d, want %d", key.tier, key.band, got[key], tokens)
		}
	}
}

// When a newer adapter adds evidence to a stored record, the record moves to
// a bucket with that evidence and the bucket it left is retracted to zero.
func TestRebuildMovesUsageThatGainsPricingEvidence(t *testing.T) {
	t.Parallel()
	db := openStoreForTest(t)
	previewer := syntheticPreviewer(t)
	batch := sampleBatch()
	input, output := uint64(10), uint64(5)
	batch.Observations[0].Tokens = normalize.TokenCounts{InputUncached: &input, Output: &output}
	if _, err := db.CommitObservations(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	if result, err := db.RebuildUsageBuckets(t.Context(), 1, previewer); err != nil || result.Applied != 1 {
		t.Fatalf("first rebuild = %#v, %v", result, err)
	}
	before, err := db.PendingOutbox(t.Context())
	if err != nil || len(before) != 1 {
		t.Fatalf("outbox = %d, %v", len(before), err)
	}

	size := uint64(15)
	batch.Observations[0].AdapterVersion = "adapter-v2"
	batch.Observations[0].ServiceTier, batch.Observations[0].PromptTokens = normalize.ServiceTierPriority, &size
	if _, err := db.CommitObservations(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	if result, err := db.RebuildUsageBuckets(t.Context(), 2, previewer); err != nil || result.Applied != 2 {
		t.Fatalf("rebuild after the upgrade = %#v, %v; want the new bucket and one retraction", result, err)
	}
	after, err := db.PendingOutbox(t.Context())
	if err != nil || len(after) != 2 {
		t.Fatalf("outbox = %d, %v; want 2", len(after), err)
	}
	var moved, retracted int
	for _, record := range after {
		var stored bucketPayload
		if err := json.Unmarshal(record.Payload, &stored); err != nil {
			t.Fatal(err)
		}
		switch {
		case record.BucketKey == before[0].BucketKey:
			retracted++
			if stored.Bucket.ServiceTier != "" || *stored.Bucket.Tokens.InputUncached.Value != 0 || *stored.Bucket.Tokens.Output.Value != 0 {
				t.Fatalf("bucket the record left = %s, want a known zero without evidence", record.Payload)
			}
		case stored.Bucket.ServiceTier == normalize.ServiceTierPriority && stored.Bucket.PromptBand == normalize.PromptBandUpTo100K:
			moved++
			if *stored.Bucket.Tokens.InputUncached.Value != 10 || *stored.Bucket.Tokens.Output.Value != 5 {
				t.Fatalf("bucket the record moved to = %s", record.Payload)
			}
		}
	}
	if moved != 1 || retracted != 1 {
		t.Fatalf("moved/retracted = %d/%d, want 1/1", moved, retracted)
	}
	summary, err := db.QueryUsage(t.Context(), PeriodAllTime, time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC), time.UTC)
	if err != nil || summary.Tokens.InputUncached.Value == nil || *summary.Tokens.InputUncached.Value != 10 {
		t.Fatalf("local total after the move = %#v, %v; want the usage counted once", summary.Tokens.InputUncached, err)
	}
}

// One model can be priced in some buckets and not in others once pricing
// evidence decides the rate card, so the summary names the reason behind the
// most unpriced tokens rather than whichever bucket came first.
func TestModelSummaryNamesTheLargestUnpricedReason(t *testing.T) {
	t.Parallel()
	db := openStoreForTest(t)
	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	estimate := uint64(100)
	bucket := func(minute int, band normalize.PromptBand, reason string, priced, unpriced uint64) UsageBucket {
		value := completeBucket(start.Add(time.Duration(minute)*time.Minute), normalize.SourceCodex, normalize.SurfaceCLI, "model-a", priced+unpriced, 1)
		value.PromptBand = band
		canonical := "model-a"
		value.Preview = Preview{
			CanonicalModelID: &canonical, Resolution: normalize.ModelInferred, UnpricedReason: reason,
			APIEstimate: AggregateValue{State: AvailabilityUnavailable}, PricedTokens: priced, UnpricedTokens: unpriced,
		}
		if priced > 0 {
			value.Preview.APIEstimate = AggregateValue{Value: &estimate, State: AvailabilityComplete}
		}
		return value
	}
	if _, err := db.ApplyBucketRevisions(t.Context(), []UsageBucket{
		bucket(0, normalize.PromptBandUpTo100K, "", 500, 0),
		bucket(1, normalize.PromptBandUnknown, modelcatalog.ReasonPromptLengthUnproven, 0, 40),
		bucket(2, normalize.PromptBandOver272K, modelcatalog.ReasonTierRateUnavailable, 0, 70),
	}); err != nil {
		t.Fatal(err)
	}
	summary, err := db.QueryUsage(t.Context(), PeriodAllTime, start.Add(time.Hour), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Models) != 1 {
		t.Fatalf("models = %d, want one row for one model", len(summary.Models))
	}
	model := summary.Models[0]
	if model.UnpricedReason != modelcatalog.ReasonTierRateUnavailable || model.APIEstimate.Value == nil || *model.APIEstimate.Value != 100 || model.APIEstimate.State != AvailabilityPartial {
		t.Fatalf("model summary = reason %q estimate %#v; want the 70-token reason beside a partial estimate", model.UnpricedReason, model.APIEstimate)
	}
}

func TestTokenTotalAddsFourCategoriesAndKeepsCoverage(t *testing.T) {
	t.Parallel()
	value := func(number uint64, state Availability) AggregateValue {
		return AggregateValue{Value: &number, State: state}
	}
	cases := []struct {
		name      string
		tokens    TokenBreakdown
		want      uint64
		wantState Availability
	}{
		{"nothing recorded", TokenBreakdown{}, 0, AvailabilityEmpty},
		{"empty parts", TokenBreakdown{InputUncached: AggregateValue{State: AvailabilityEmpty}}, 0, AvailabilityEmpty},
		{"subsets are not added again", TokenBreakdown{
			InputUncached: value(1, AvailabilityComplete), InputCacheRead: value(2, AvailabilityComplete),
			InputCacheWrite: value(4, AvailabilityComplete), InputCacheWrite5m: value(3, AvailabilityComplete),
			InputCacheWrite1h: value(1, AvailabilityComplete), Output: value(8, AvailabilityComplete),
			ReasoningOutput: value(5, AvailabilityComplete),
		}, 15, AvailabilityComplete},
		{"a missing category makes a lower bound", TokenBreakdown{
			InputUncached: value(10, AvailabilityComplete), InputCacheWrite: AggregateValue{State: AvailabilityUnavailable},
			Output: value(5, AvailabilityComplete),
		}, 15, AvailabilityPartial},
		{"nothing known", TokenBreakdown{Output: AggregateValue{State: AvailabilityUnavailable}}, 0, AvailabilityUnavailable},
		{"saturates", TokenBreakdown{
			InputUncached: value(math.MaxUint64, AvailabilityComplete), Output: value(1, AvailabilityComplete),
		}, math.MaxUint64, AvailabilityPartial},
	}
	for _, test := range cases {
		total := test.tokens.Total()
		if total.State != test.wantState {
			t.Errorf("%s: state = %q, want %q", test.name, total.State, test.wantState)
		}
		if known := test.wantState == AvailabilityComplete || test.wantState == AvailabilityPartial; known != (total.Value != nil) || known && *total.Value != test.want {
			t.Errorf("%s: value = %v, want %d", test.name, total.Value, test.want)
		}
	}
}

func TestSummaryReportsThePricedShare(t *testing.T) {
	t.Parallel()
	db := openStoreForTest(t)
	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	priced := completeBucket(start, normalize.SourceClaude, normalize.SurfaceCLI, "model-a", 9499, 1)
	priced.Preview.PricedTokens = 9499
	unpriced := completeBucket(start.Add(time.Minute), normalize.SourceCodex, normalize.SurfaceCLI, "model-b", 501, 1)
	unpriced.Preview.UnpricedTokens = 501
	if _, err := db.ApplyBucketRevisions(t.Context(), []UsageBucket{priced, unpriced}); err != nil {
		t.Fatal(err)
	}
	summary, err := db.QueryUsage(t.Context(), PeriodAllTime, start.Add(time.Hour), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	// 9499 of 10000 is 94.99%: the share is cut, never rounded up to 95%.
	if got := summary.Coverage; got.PricedTokens != 9499 || got.UnpricedTokens != 501 || got.PricedBasisPoints == nil || *got.PricedBasisPoints != 9499 {
		t.Fatalf("summary coverage = %+v", got)
	}
	shares := map[string]int{}
	for _, source := range summary.Sources {
		shares[source.Name] = *source.Coverage.PricedBasisPoints
	}
	if shares["claude"] != 10000 || shares["codex"] != 0 {
		t.Fatalf("source shares = %v", shares)
	}
	empty, err := db.QueryUsage(t.Context(), PeriodToday, start.AddDate(0, 0, 3), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Coverage.PricedBasisPoints != nil {
		t.Fatalf("empty period has a priced share: %+v", empty.Coverage)
	}
}

func TestDailyUsageFollowsLocalCalendarDays(t *testing.T) {
	t.Parallel()
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	db := openStoreForTest(t)
	// New York left daylight time on November 2, 2025, a 25-hour day that
	// ran from 04:00 UTC to 05:00 UTC the next day. A past change is used so
	// the result does not depend on the installed timezone data.
	buckets := []UsageBucket{
		completeBucket(time.Date(2025, 10, 31, 3, 59, 0, 0, time.UTC), normalize.SourceClaude, normalize.SurfaceCLI, "too-old", 1000, 1),
		completeBucket(time.Date(2025, 10, 31, 4, 0, 0, 0, time.UTC), normalize.SourceClaude, normalize.SurfaceCLI, "oct-31", 1, 1),
		completeBucket(time.Date(2025, 11, 2, 4, 0, 0, 0, time.UTC), normalize.SourceClaude, normalize.SurfaceCLI, "nov-2-first", 10, 1),
		completeBucket(time.Date(2025, 11, 3, 4, 59, 0, 0, time.UTC), normalize.SourceCodex, normalize.SurfaceCLI, "nov-2-last", 20, 1),
		completeBucket(time.Date(2025, 11, 3, 5, 0, 0, 0, time.UTC), normalize.SourceCodex, normalize.SurfaceCLI, "nov-3", 300, 1),
		completeBucket(time.Date(2025, 11, 4, 9, 0, 0, 0, time.UTC), normalize.SourceCodex, normalize.SurfaceCLI, "after-now", 5000, 1),
	}
	if _, err := db.ApplyBucketRevisions(t.Context(), buckets); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2025, 11, 3, 20, 0, 0, 0, time.UTC)
	series, err := db.QueryDailyUsage(t.Context(), now, location, 4)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct {
		day   int
		month time.Month
		total uint64
		state Availability
	}{
		{31, time.October, 1, AvailabilityComplete},
		{1, time.November, 0, AvailabilityEmpty},
		{2, time.November, 30, AvailabilityComplete},
		{3, time.November, 300, AvailabilityComplete},
	}
	if len(series) != len(want) {
		t.Fatalf("series has %d days, want %d", len(series), len(want))
	}
	for index, expected := range want {
		got := series[index]
		if got.Day.Day() != expected.day || got.Day.Month() != expected.month || got.Day.Hour() != 0 || got.Day.Location() != location {
			t.Errorf("day %d = %v", index, got.Day)
		}
		if got.Tokens.State != expected.state || (got.Tokens.Value == nil) != (expected.state == AvailabilityEmpty) {
			t.Errorf("day %d tokens = %+v, want state %q", index, got.Tokens, expected.state)
		} else if got.Tokens.Value != nil && *got.Tokens.Value != expected.total {
			// An empty day stays empty: it is not reported as a zero.
			t.Errorf("day %d total = %d, want %d", index, *got.Tokens.Value, expected.total)
		}
	}
	if _, err := db.QueryDailyUsage(t.Context(), now, location, 0); err == nil {
		t.Error("zero days accepted")
	}
	if _, err := db.QueryDailyUsage(t.Context(), now, nil, 30); err == nil {
		t.Error("missing location accepted")
	}
}
