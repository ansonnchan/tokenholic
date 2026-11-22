package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/normalize"
)

func dayTotal(source normalize.Source, day time.Time, model string, input, output uint64) normalize.Observation {
	resolution := normalize.ModelReported
	if model == "" {
		resolution = normalize.ModelUnknown
	}
	return normalize.Observation{
		Source: source, Surface: normalize.SurfaceCLI, SourceVersion: "synthetic-v1",
		SourceRecordID: strings.Join([]string{"day", string(source), day.Format(time.DateOnly), model}, "|"),
		OccurredAt:     day, ObservedModelID: model, ModelResolution: resolution,
		Tokens:         normalize.TokenCounts{InputUncached: &input, Output: &output},
		CountSemantics: normalize.CountDayTotal, Measurement: normalize.MeasurementReported,
		AdapterVersion: "adapter-v1", NormalizationVersion: normalize.SchemaVersion,
	}
}

func dayBatch(source normalize.Source, observations ...normalize.Observation) ObservationBatch {
	now := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	identity := NewSourceIdentity(source, normalize.SurfaceCLI, "synthetic-"+string(source))
	return ObservationBatch{
		Observations: observations,
		Checkpoint:   Checkpoint{Source: identity, Cursor: uint64(len(observations)), CollectedAt: now},
		Health: AdapterHealth{
			Source: identity, AdapterVersion: "adapter-v1", CheckedAt: now,
			Health: normalize.SourceHealth{
				Source: source, Surface: normalize.SurfaceCLI, Status: normalize.HealthDetected,
				Capabilities: normalize.Capabilities{
					Model: normalize.CapabilityReported, InputUncached: normalize.CapabilityReported,
					InputCacheRead: normalize.CapabilityUnsupported, InputCacheWrite: normalize.CapabilityUnsupported,
					Output: normalize.CapabilityReported, ReasoningOutput: normalize.CapabilityUnsupported,
				},
			},
		},
	}
}

type storedBucket struct {
	key, revision, payload, state string
}

func storedBuckets(t *testing.T, db *DB) map[string]storedBucket {
	t.Helper()
	rows, err := db.sql.QueryContext(t.Context(), `
		SELECT b.bucket_key, b.revision, b.payload_json, COALESCE(o.state, '')
		FROM usage_buckets b LEFT JOIN outbox_records o ON o.bucket_key = b.bucket_key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	buckets := make(map[string]storedBucket)
	for rows.Next() {
		var bucket storedBucket
		if err := rows.Scan(&bucket.key, &bucket.revision, &bucket.payload, &bucket.state); err != nil {
			t.Fatal(err)
		}
		buckets[bucket.key] = bucket
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return buckets
}

func rebuild(t *testing.T, db *DB) BucketApplyResult {
	t.Helper()
	revision, err := db.NextUsageBucketRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result, err := db.RebuildUsageBuckets(t.Context(), revision, syntheticPreviewer(t))
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// A day's row grows until the day ends. Each scan reports the day's whole
// value so far, and the store must end up holding the latest value once: not
// the sum of the readings, and not one record per reading.
func TestRescanningAGrowingDayReplacesItsValue(t *testing.T) {
	t.Parallel()
	day := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		// readings are successive scans of the same day and model.
		readings [][2]uint64
		// changed says whether each reading should change the stored record.
		changed []int64
	}{
		{name: "a day that grows", readings: [][2]uint64{{100, 10}, {250, 30}, {900, 75}}, changed: []int64{1, 1, 1}},
		{name: "the same reading again", readings: [][2]uint64{{100, 10}, {100, 10}, {100, 10}}, changed: []int64{1, 0, 0}},
		{name: "growth, then a quiet scan, then growth", readings: [][2]uint64{{5, 1}, {5, 1}, {6, 1}}, changed: []int64{1, 0, 1}},
		{name: "only output grows", readings: [][2]uint64{{5, 1}, {5, 2}}, changed: []int64{1, 1}},
		// Records can disappear from an agent's folder. The day then reads
		// lower, and the lower reading is still the day's value.
		{name: "a day that shrinks", readings: [][2]uint64{{900, 75}, {400, 20}}, changed: []int64{1, 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			db := openStoreForTest(t)
			var lastRevision string
			for index, reading := range test.readings {
				result, err := db.CommitObservations(t.Context(), dayBatch(normalize.SourceGemini,
					dayTotal(normalize.SourceGemini, day, "model-a", reading[0], reading[1])))
				if err != nil {
					t.Fatal(err)
				}
				if result.InsertedObservations != test.changed[index] {
					t.Fatalf("reading %d changed %d records, want %d", index, result.InsertedObservations, test.changed[index])
				}
				stored := onlyObservation(t, db)
				if *stored.Tokens.InputUncached != reading[0] || *stored.Tokens.Output != reading[1] {
					t.Fatalf("reading %d stored %d/%d, want the reading itself %d/%d",
						index, *stored.Tokens.InputUncached, *stored.Tokens.Output, reading[0], reading[1])
				}
				if stored.CountSemantics != normalize.CountDayTotal || !stored.OccurredAt.Equal(day) {
					t.Fatalf("stored record = %+v", stored)
				}

				rebuild(t, db)
				buckets := storedBuckets(t, db)
				if len(buckets) != 1 {
					t.Fatalf("reading %d left %d buckets, want the one day", index, len(buckets))
				}
				for _, bucket := range buckets {
					var payload bucketPayload
					if err := json.Unmarshal([]byte(bucket.payload), &payload); err != nil {
						t.Fatal(err)
					}
					if total := payload.Bucket.Tokens.Total(); total.Value == nil || *total.Value != reading[0]+reading[1] {
						t.Fatalf("reading %d bucket total = %+v, want %d", index, total, reading[0]+reading[1])
					}
					if payload.Bucket.Granularity != normalize.GranularityDay || !payload.Bucket.Start.Equal(day) {
						t.Fatalf("bucket = %+v, want a day-level value filed at the day's start", payload.Bucket)
					}
					// An unchanged reading must not be uploaded again.
					if test.changed[index] == 0 && bucket.revision != lastRevision {
						t.Fatalf("reading %d moved the revision from %s to %s without a change", index, lastRevision, bucket.revision)
					}
					if test.changed[index] == 1 && bucket.revision == lastRevision {
						t.Fatalf("reading %d kept revision %s although the day changed", index, bucket.revision)
					}
					lastRevision = bucket.revision
				}
			}
			// Every revision replaced the last in the outbox: one pending
			// upload, holding the final value.
			pending, err := db.PendingOutbox(t.Context())
			if err != nil || len(pending) != 1 {
				t.Fatalf("PendingOutbox() = %d, %v; want one bucket", len(pending), err)
			}
			final := test.readings[len(test.readings)-1]
			summary, err := db.QueryUsage(t.Context(), PeriodAllTime, time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC), time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			if total := summary.Tokens.Total(); total.Value == nil || *total.Value != final[0]+final[1] {
				t.Fatalf("all-time total = %+v, want %d", total, final[0]+final[1])
			}
		})
	}
}

// Day totals are keyed by agent, day, and model. A different agent, day, or
// model is a different value, and replacing one leaves the others alone.
func TestDayTotalsAreKeyedByAgentDayAndModel(t *testing.T) {
	t.Parallel()
	db := openStoreForTest(t)
	first := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	second := first.AddDate(0, 0, 1)
	commit := func(source normalize.Source, observations ...normalize.Observation) {
		t.Helper()
		if _, err := db.CommitObservations(t.Context(), dayBatch(source, observations...)); err != nil {
			t.Fatal(err)
		}
	}
	commit(normalize.SourceGemini,
		dayTotal(normalize.SourceGemini, first, "model-a", 100, 10),
		dayTotal(normalize.SourceGemini, first, "model-b", 20, 2),
		dayTotal(normalize.SourceGemini, second, "model-a", 7, 1),
		dayTotal(normalize.SourceGemini, first, "", 3, 0))
	commit(normalize.SourceQwen, dayTotal(normalize.SourceQwen, first, "model-a", 50, 5))
	rebuild(t, db)
	if buckets := storedBuckets(t, db); len(buckets) != 5 {
		t.Fatalf("buckets = %d, want one per agent, day, and model", len(buckets))
	}
	before := storedBuckets(t, db)

	// Only Gemini's first day of model-a grows.
	commit(normalize.SourceGemini, dayTotal(normalize.SourceGemini, first, "model-a", 400, 40))
	if result := rebuild(t, db); result.Applied != 1 || result.Ignored != 4 {
		t.Fatalf("rebuild = %+v, want one replaced bucket and four untouched", result)
	}
	changed := 0
	for key, bucket := range storedBuckets(t, db) {
		if bucket != before[key] {
			changed++
			if !strings.Contains(bucket.payload, `"source":"gemini"`) || !strings.Contains(bucket.payload, `"observed_model_id":"model-a"`) ||
				!strings.Contains(bucket.payload, `"start":"2026-01-02T00:00:00Z"`) {
				t.Fatalf("the wrong bucket changed: %s", bucket.payload)
			}
		}
	}
	if changed != 1 {
		t.Fatalf("%d buckets changed, want 1", changed)
	}
	summary, err := db.QueryUsage(t.Context(), PeriodAllTime, time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	// 440 + 22 + 8 + 3 for Gemini, 55 for Qwen.
	if total := summary.Tokens.Total(); total.Value == nil || *total.Value != 528 {
		t.Fatalf("all-time total = %+v, want 528", total)
	}
}

// Admitting day-level sources must leave Claude and Codex exactly as they
// were: the same records, the same buckets under the same keys and
// revisions, the same upload bytes, and the same totals.
func TestDayLevelSourcesLeaveClaudeAndCodexUntouched(t *testing.T) {
	t.Parallel()
	db := openStoreForTest(t)
	at := time.Date(2026, 1, 2, 9, 30, 15, 0, time.UTC)
	native := func() []ObservationBatch {
		claude := sampleBatch()
		small := uint64(41)
		claude.Observations[0].OccurredAt = at
		claude.Observations[0].Tokens = normalize.TokenCounts{InputUncached: &small, Output: &small}
		codexInput, codexOutput := uint64(700), uint64(90)
		identity := NewSourceIdentity(normalize.SourceCodex, normalize.SurfaceSharedLocal, "synthetic-codex")
		codex := ObservationBatch{
			Observations: []normalize.Observation{{
				Source: normalize.SourceCodex, Surface: normalize.SurfaceSharedLocal, SourceVersion: "synthetic-v1",
				SourceRecordID: "codex-request-1", OccurredAt: at.Add(time.Minute), ObservedModelID: "model-a",
				ModelResolution: normalize.ModelInferred, Warnings: []normalize.Warning{{Code: "model_inferred"}},
				Tokens:         normalize.TokenCounts{InputUncached: &codexInput, Output: &codexOutput},
				CountSemantics: normalize.CountIncremental, Measurement: normalize.MeasurementDerived,
				AdapterVersion: "adapter-v1", NormalizationVersion: normalize.SchemaVersion,
			}},
			Checkpoint: Checkpoint{Source: identity, Cursor: 1, CollectedAt: at},
			Health: AdapterHealth{Source: identity, AdapterVersion: "adapter-v1", CheckedAt: at, Health: normalize.SourceHealth{
				Source: normalize.SourceCodex, Surface: normalize.SurfaceSharedLocal, Status: normalize.HealthDetected,
				Capabilities: normalize.Capabilities{
					Model: normalize.CapabilityInferred, InputUncached: normalize.CapabilityDerived,
					InputCacheRead: normalize.CapabilityReported, InputCacheWrite: normalize.CapabilityUnsupported,
					Output: normalize.CapabilityReported, ReasoningOutput: normalize.CapabilityReported,
				},
			}},
		}
		return []ObservationBatch{claude, codex}
	}
	if _, err := db.CommitScan(t.Context(), native()); err != nil {
		t.Fatal(err)
	}
	rebuild(t, db)
	before := storedBuckets(t, db)
	if len(before) != 2 {
		t.Fatalf("native buckets = %d, want 2", len(before))
	}
	for _, bucket := range before {
		if strings.Contains(bucket.payload, "granularity") {
			t.Fatalf("a native bucket carries a granularity: %s", bucket.payload)
		}
	}
	nativeTotal := func() uint64 {
		t.Helper()
		summary, err := db.QueryUsage(t.Context(), PeriodAllTime, at.Add(48*time.Hour), time.UTC)
		if err != nil {
			t.Fatal(err)
		}
		var total uint64
		for _, source := range summary.Sources {
			if source.Name == string(normalize.SourceClaude) || source.Name == string(normalize.SourceCodex) {
				total += *source.Tokens.Total().Value
			}
		}
		return total
	}
	wantTotal := nativeTotal()
	if wantTotal != 41+41+700+90 {
		t.Fatalf("native total = %d", wantTotal)
	}

	// Now every day-level source reports the same day, twice, growing, with
	// the native sources rescanned in between.
	day := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	for round := uint64(1); round <= 2; round++ {
		batches := native()
		for _, source := range normalize.DayLevelSources() {
			batches = append(batches, dayBatch(source, dayTotal(source, day, "model-a", 1000*round, 100*round)))
		}
		if _, err := db.CommitScan(t.Context(), batches); err != nil {
			t.Fatal(err)
		}
		rebuild(t, db)
		after := storedBuckets(t, db)
		if len(after) != 2+len(normalize.DayLevelSources()) {
			t.Fatalf("round %d: buckets = %d", round, len(after))
		}
		for key, bucket := range before {
			if after[key] != bucket {
				t.Fatalf("round %d changed a native bucket:\nbefore %+v\nafter  %+v", round, bucket, after[key])
			}
		}
		if got := nativeTotal(); got != wantTotal {
			t.Fatalf("round %d: native total = %d, want %d", round, got, wantTotal)
		}
	}
}

func TestDayLevelSchemaConstraints(t *testing.T) {
	t.Parallel()
	db := openStoreForTest(t)
	insert := func(source, surface, semantics string) error {
		_, err := db.sql.ExecContext(t.Context(), `
			INSERT INTO observations(
				identity, source, surface, source_version, occurred_at, observed_model_id, model_resolution,
				output_tokens, count_semantics, measurement, adapter_version, normalization_version, warnings_json
			) VALUES (?, ?, ?, 'synthetic-v1', '2026-01-02T00:00:00Z', 'model-synthetic', 'reported', '7',
				?, 'reported', '1', '1', '[]')`, source+surface+semantics, source, surface, semantics)
		return err
	}
	tests := []struct {
		source, surface, semantics string
		wantErr                    bool
	}{
		{"gemini", "cli", "day_total", false},
		{"copilot", "cli", "day_total", false},
		{"grok", "cli", "day_total", false},
		{"opencode", "cli", "day_total", false},
		{"qwen", "cli", "day_total", false},
		{"claude", "cli", "incremental", false},
		{"codex", "shared_local", "cumulative_snapshot", false},
		{"claude", "cli", "day_total", true},
		{"codex", "cli", "day_total", true},
		{"gemini", "cli", "incremental", true},
		{"gemini", "desktop", "day_total", true},
		{"cursor", "cli", "day_total", true},
	}
	for _, test := range tests {
		if err := insert(test.source, test.surface, test.semantics); (err != nil) != test.wantErr {
			t.Errorf("%s/%s/%s: error = %v, wantErr %t", test.source, test.surface, test.semantics, err, test.wantErr)
		}
	}
}

// A day-level source can be present while nothing can read it. That state is
// stored as its own status, with its reason, and round-trips.
func TestUnavailableHealthRoundTrips(t *testing.T) {
	t.Parallel()
	db := openStoreForTest(t)
	batch := dayBatch(normalize.SourceGemini)
	batch.Health.Health.Status = normalize.HealthUnavailable
	batch.Health.Health.Warnings = []normalize.Warning{{Code: "synthetic_reason"}}
	if _, err := db.CommitObservations(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	records, err := db.AdapterHealthRecords(t.Context())
	if err != nil || len(records) != 1 {
		t.Fatalf("AdapterHealthRecords() = %d, %v", len(records), err)
	}
	if health := records[0].Health; health.Status != normalize.HealthUnavailable || len(health.Warnings) != 1 || health.Warnings[0].Code != "synthetic_reason" {
		t.Fatalf("stored health = %+v", health)
	}
	batch.Health.Health.Status = "retired"
	if _, err := db.CommitObservations(t.Context(), batch); err == nil {
		t.Fatal("an unknown health status was stored")
	}
}
