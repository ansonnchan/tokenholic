package store

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/normalize"
)

func TestCommitObservationsRoundTripsNullableFieldsAndHealth(t *testing.T) {
	t.Parallel()
	db := openStoreForTest(t)
	batch := sampleBatch()
	result, err := db.CommitObservations(t.Context(), batch)
	if err != nil {
		t.Fatal(err)
	}
	if result.InsertedObservations != 1 {
		t.Fatalf("inserted observations = %d, want 1", result.InsertedObservations)
	}

	observations, err := db.Observations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 1 {
		t.Fatalf("observation count = %d, want 1", len(observations))
	}
	stored := observations[0]
	if stored.Tokens.InputUncached != nil {
		t.Fatalf("input uncached = %v, want unavailable", *stored.Tokens.InputUncached)
	}
	if stored.Tokens.InputCacheRead == nil || *stored.Tokens.InputCacheRead != 0 {
		t.Fatalf("cache read = %v, want reported zero", stored.Tokens.InputCacheRead)
	}
	if stored.Tokens.Output == nil || *stored.Tokens.Output != math.MaxUint64 {
		t.Fatalf("output = %v, want MaxUint64", stored.Tokens.Output)
	}
	if !reflect.DeepEqual(stored.Warnings, batch.Observations[0].Warnings) {
		t.Fatalf("warnings = %#v, want %#v", stored.Warnings, batch.Observations[0].Warnings)
	}
	if stored.ObservedModelID != batch.Observations[0].ObservedModelID ||
		!reflect.DeepEqual(stored.CanonicalModelID, batch.Observations[0].CanonicalModelID) ||
		!reflect.DeepEqual(stored.PricingKey, batch.Observations[0].PricingKey) {
		t.Fatalf("model metadata did not round trip: %#v", stored)
	}

	checkpoint, found, err := db.SourceCheckpoint(t.Context(), batch.Checkpoint.Source)
	if err != nil || !found {
		t.Fatalf("SourceCheckpoint() = %#v, %t, %v", checkpoint, found, err)
	}
	if checkpoint.Cursor != batch.Checkpoint.Cursor || checkpoint.LastRecordFingerprint == nil {
		t.Fatalf("checkpoint = %#v", checkpoint)
	}
	health, err := db.AdapterHealthRecords(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(health) != 1 || !reflect.DeepEqual(health[0].Health, batch.Health.Health) {
		t.Fatalf("health = %#v, want %#v", health, batch.Health.Health)
	}
}

func TestCommitObservationsIsDuplicateSafeAndUpdatesCheckpoint(t *testing.T) {
	t.Parallel()
	db := openStoreForTest(t)
	batch := sampleBatch()
	if _, err := db.CommitObservations(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	batch.Checkpoint.Cursor = 99
	batch.Checkpoint.CollectedAt = batch.Checkpoint.CollectedAt.Add(time.Minute)
	result, err := db.CommitObservations(t.Context(), batch)
	if err != nil {
		t.Fatal(err)
	}
	if result.InsertedObservations != 0 {
		t.Fatalf("duplicate inserted observations = %d, want 0", result.InsertedObservations)
	}
	observations, err := db.Observations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 1 {
		t.Fatalf("observation count = %d, want 1", len(observations))
	}
	checkpoint, found, err := db.SourceCheckpoint(t.Context(), batch.Checkpoint.Source)
	if err != nil || !found || checkpoint.Cursor != 99 {
		t.Fatalf("updated checkpoint = %#v, %t, %v", checkpoint, found, err)
	}
}

func TestCommitObservationsRollsBackWhenCheckpointFails(t *testing.T) {
	t.Parallel()
	db := openStoreForTest(t)
	if _, err := db.sql.ExecContext(t.Context(), `
		CREATE TRIGGER reject_checkpoint BEFORE INSERT ON source_checkpoints
		BEGIN SELECT RAISE(ABORT, 'synthetic checkpoint interruption'); END
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CommitObservations(t.Context(), sampleBatch()); err == nil {
		t.Fatal("CommitObservations() succeeded")
	}
	assertStoredCounts(t, db, 0, 0, 0)
}

func TestCommitScanRollsBackAllSourcesTogether(t *testing.T) {
	t.Parallel()
	db := openStoreForTest(t)
	first := sampleBatch()
	second := sampleBatch()
	second.Checkpoint.Source = NewSourceIdentity(normalize.SourceClaude, normalize.SurfaceDesktopCode, "second")
	second.Health.Source = second.Checkpoint.Source
	second.Health.Health.Surface = normalize.SurfaceDesktopCode
	second.Observations[0].Surface = normalize.SurfaceDesktopCode
	second.Observations[0].SourceRecordID = "second-record"
	if _, err := db.sql.ExecContext(t.Context(), `
		CREATE TRIGGER reject_second_checkpoint BEFORE INSERT ON source_checkpoints
		WHEN NEW.surface = 'desktop_code'
		BEGIN SELECT RAISE(ABORT, 'synthetic second-source interruption'); END
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CommitScan(t.Context(), []ObservationBatch{first, second}); err == nil {
		t.Fatal("CommitScan() succeeded")
	}
	assertStoredCounts(t, db, 0, 0, 0)
}

func TestCommitObservationsHonorsCancellation(t *testing.T) {
	t.Parallel()
	db := openStoreForTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.CommitObservations(ctx, sampleBatch()); !errors.Is(err, context.Canceled) {
		t.Fatalf("CommitObservations(canceled) error = %v, want context cancellation", err)
	}
	assertStoredCounts(t, db, 0, 0, 0)
}

func TestStoredIdentityDoesNotRetainPrivateSourceValues(t *testing.T) {
	t.Parallel()
	db := openStoreForTest(t)
	batch := sampleBatch()
	rawValues := []string{
		"/private/provider/source/root",
		"/private/provider/source/record.jsonl",
		"private-session-identifier",
	}
	batch.Checkpoint.Source = NewSourceIdentity(normalize.SourceClaude, normalize.SurfaceCLI, rawValues[0])
	batch.Health.Source = batch.Checkpoint.Source
	batch.Observations[0].SourceRecordID = rawValues[1]
	batch.Observations[0].SessionIDLocal = &rawValues[2]
	batch.Checkpoint.LastSourceRecordID = rawValues[1]
	if _, err := db.CommitObservations(t.Context(), batch); err != nil {
		t.Fatal(err)
	}

	var storedText string
	if err := db.sql.QueryRowContext(t.Context(), `
		SELECT identity || ' ' || COALESCE(session_fingerprint, '') FROM observations
	`).Scan(&storedText); err != nil {
		t.Fatal(err)
	}
	var sourceText string
	if err := db.sql.QueryRowContext(t.Context(), `
		SELECT source_identity || ' ' || COALESCE(last_record_fingerprint, '') FROM source_checkpoints
	`).Scan(&sourceText); err != nil {
		t.Fatal(err)
	}
	for _, raw := range rawValues {
		if strings.Contains(storedText+sourceText, raw) {
			t.Fatalf("stored identity contains private value %q", raw)
		}
	}
	var schema string
	if err := db.sql.QueryRowContext(t.Context(), `SELECT group_concat(sql, ' ') FROM sqlite_master WHERE sql IS NOT NULL`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(schema), "path") {
		t.Fatalf("storage schema contains a path field: %s", schema)
	}
}

func TestObservationStorageSurvivesRestart(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), databaseFileName)
	db, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	batch := sampleBatch()
	if _, err := db.CommitObservations(t.Context(), batch); err != nil {
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
	assertStoredCounts(t, reopened, 1, 1, 1)
}

func TestObservationSchemaConstraintsRejectInvalidState(t *testing.T) {
	t.Parallel()
	db := openStoreForTest(t)
	if _, err := db.CommitObservations(t.Context(), sampleBatch()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(t.Context(), `UPDATE observations SET source = 'unsupported'`); err == nil {
		t.Fatal("invalid source update succeeded")
	}
	if _, err := db.sql.ExecContext(t.Context(), `UPDATE observations SET output_tokens = 'not-a-number'`); err == nil {
		t.Fatal("invalid token update succeeded")
	}
}

func TestCommitObservationsReplacesUsageOnlyForANewAdapterRevision(t *testing.T) {
	t.Parallel()
	value := func(v uint64) *uint64 { return &v }
	db := openStoreForTest(t)

	// An earlier adapter revision knew only the combined cache-write total.
	batch := sampleBatch()
	batch.Observations[0].Tokens = normalize.TokenCounts{InputCacheWrite: value(300), Output: value(20)}
	if _, err := db.CommitObservations(t.Context(), batch); err != nil {
		t.Fatal(err)
	}

	// The same revision reporting different values is a rescan, not a repair.
	same := sampleBatch()
	same.Observations[0].Tokens = normalize.TokenCounts{InputCacheWrite: value(999), Output: value(999)}
	if result, err := db.CommitObservations(t.Context(), same); err != nil || result.InsertedObservations != 0 {
		t.Fatalf("same-revision commit = %#v, %v; want no change", result, err)
	}
	if stored := onlyObservation(t, db); *stored.Tokens.InputCacheWrite != 300 || stored.Tokens.InputCacheWrite1h != nil {
		t.Fatalf("same-revision rescan changed tokens: %#v", stored.Tokens)
	}

	upgraded := sampleBatch()
	upgraded.Observations[0].AdapterVersion = "adapter-v2"
	upgraded.Observations[0].ObservedModelID = "claude-renamed"
	upgraded.Observations[0].Tokens = normalize.TokenCounts{
		InputCacheWrite: value(300), InputCacheWrite5m: value(100), InputCacheWrite1h: value(200), Output: value(20),
	}
	for attempt, wantChanged := range []int64{1, 0} {
		result, err := db.CommitObservations(t.Context(), upgraded)
		if err != nil || result.InsertedObservations != wantChanged {
			t.Fatalf("upgrade attempt %d = %#v, %v; want %d changed", attempt, result, err, wantChanged)
		}
	}
	stored := onlyObservation(t, db)
	if *stored.Tokens.InputCacheWrite != 300 || stored.Tokens.InputCacheWrite5m == nil || *stored.Tokens.InputCacheWrite5m != 100 ||
		stored.Tokens.InputCacheWrite1h == nil || *stored.Tokens.InputCacheWrite1h != 200 || stored.AdapterVersion != "adapter-v2" {
		t.Fatalf("upgraded observation = %#v", stored)
	}
	// Attribution is bucket identity and must not move with a usage repair.
	if stored.ObservedModelID != "claude-observed" {
		t.Fatalf("observed model = %q, want the first stored value", stored.ObservedModelID)
	}
}

// Pricing evidence is stored as NULL until a source proves it, and a newer
// adapter revision adds it to a record stored without it.
func TestObservationPricingEvidenceRoundTripsAndFollowsTheAdapterRevision(t *testing.T) {
	t.Parallel()
	db := openStoreForTest(t)
	if _, err := db.CommitObservations(t.Context(), sampleBatch()); err != nil {
		t.Fatal(err)
	}
	if stored := onlyObservation(t, db); stored.ServiceTier != normalize.ServiceTierUnknown || stored.PromptTokens != nil {
		t.Fatalf("evidence before any was reported = %q/%v, want none", stored.ServiceTier, stored.PromptTokens)
	}
	var tier, prompt *string
	if err := db.sql.QueryRowContext(t.Context(), `SELECT service_tier, prompt_tokens FROM observations`).Scan(&tier, &prompt); err != nil || tier != nil || prompt != nil {
		t.Fatalf("stored evidence = %v/%v, %v; want NULL, never a default tier", tier, prompt, err)
	}

	size := uint64(280_000)
	sameRevision := sampleBatch()
	sameRevision.Observations[0].ServiceTier, sameRevision.Observations[0].PromptTokens = normalize.ServiceTierPriority, &size
	if result, err := db.CommitObservations(t.Context(), sameRevision); err != nil || result.InsertedObservations != 0 {
		t.Fatalf("rescan by the same adapter revision = %#v, %v; want a no-op", result, err)
	}
	if stored := onlyObservation(t, db); stored.ServiceTier != normalize.ServiceTierUnknown || stored.PromptTokens != nil {
		t.Fatalf("evidence after a same-revision rescan = %q/%v, want none", stored.ServiceTier, stored.PromptTokens)
	}

	upgraded := sampleBatch()
	upgraded.Observations[0].AdapterVersion = "adapter-v2"
	upgraded.Observations[0].ServiceTier, upgraded.Observations[0].PromptTokens = normalize.ServiceTierPriority, &size
	if result, err := db.CommitObservations(t.Context(), upgraded); err != nil || result.InsertedObservations != 1 {
		t.Fatalf("rescan by a newer adapter revision = %#v, %v; want one replacement", result, err)
	}
	stored := onlyObservation(t, db)
	if stored.ServiceTier != normalize.ServiceTierPriority || stored.PromptTokens == nil || *stored.PromptTokens != size {
		t.Fatalf("evidence after the upgrade = %q/%v, want priority/%d", stored.ServiceTier, stored.PromptTokens, size)
	}
}

func onlyObservation(t *testing.T, db *DB) StoredObservation {
	t.Helper()
	observations, err := db.Observations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 1 {
		t.Fatalf("observation count = %d, want 1", len(observations))
	}
	return observations[0]
}

func sampleBatch() ObservationBatch {
	zero := uint64(0)
	maximum := uint64(math.MaxUint64)
	session := "private-session-identifier"
	canonical := "claude-sonnet"
	provider := "anthropic"
	pricing := "anthropic/claude-sonnet"
	now := time.Date(2026, time.November, 17, 12, 30, 0, 123, time.FixedZone("fixture", -8*60*60))
	recordCount := uint64(1)
	lastSuccess := now.Add(-time.Minute)
	source := NewSourceIdentity(normalize.SourceClaude, normalize.SurfaceCLI, "/private/provider/source/root")
	capabilities := normalize.Capabilities{
		Model: normalize.CapabilityReported, InputUncached: normalize.CapabilityReported,
		InputCacheRead: normalize.CapabilityReported, InputCacheWrite: normalize.CapabilityReported,
		Output: normalize.CapabilityReported, ReasoningOutput: normalize.CapabilityUnsupported,
	}
	return ObservationBatch{
		Observations: []normalize.Observation{{
			Source: normalize.SourceClaude, Surface: normalize.SurfaceCLI,
			SourceVersion: "fixture-v1", SourceRecordID: "/private/provider/source/record.jsonl",
			SessionIDLocal: &session, OccurredAt: now, ObservedModelID: "claude-observed",
			CanonicalModelID: &canonical, ModelProvider: &provider, PricingKey: &pricing,
			ModelResolution: normalize.ModelResolved,
			Tokens:          normalize.TokenCounts{InputCacheRead: &zero, Output: &maximum},
			CountSemantics:  normalize.CountIncremental, Measurement: normalize.MeasurementReported,
			AdapterVersion: "adapter-v1", NormalizationVersion: normalize.SchemaVersion,
			Warnings: []normalize.Warning{{Code: "partial_category", Message: "cache write unavailable"}},
		}},
		Checkpoint: Checkpoint{
			Source: source, Cursor: 1, LastSourceRecordID: "/private/provider/source/record.jsonl", CollectedAt: now,
		},
		Health: AdapterHealth{
			Source: source,
			Health: normalize.SourceHealth{
				Source: normalize.SourceClaude, Surface: normalize.SurfaceCLI,
				Status: normalize.HealthDetected, RecordCount: &recordCount,
				Format: "claude-jsonl-v1", Capabilities: capabilities,
				Warnings: []normalize.Warning{{Code: "source_partial", Message: "one safe warning"}},
			},
			AdapterVersion: "adapter-v1", CheckedAt: now, LastSuccessAt: &lastSuccess,
		},
	}
}

func openStoreForTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(t.Context(), filepath.Join(t.TempDir(), databaseFileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(context.Background()) })
	return db
}

func assertStoredCounts(t *testing.T, db *DB, observations, checkpoints, health int) {
	t.Helper()
	for table, want := range map[string]int{
		"observations": observations, "source_checkpoints": checkpoints, "adapter_health": health,
	} {
		var got int
		if err := db.sql.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s count = %d, want %d", table, got, want)
		}
	}
}
