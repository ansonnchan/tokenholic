package application

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/collect"
	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/store"
)

type fakeStorage struct {
	summaries    map[store.Period]store.UsageSummary
	diagnostics  store.DiagnosticSnapshot
	queryErr     error
	dailyErr     error
	nextRevision uint64
	nextErr      error
	rebuildErr   error
	queries      []store.Period
	events       *[]string
	rebuilt      uint64
}

func (s *fakeStorage) QueryUsage(_ context.Context, period store.Period, _ time.Time, _ *time.Location) (store.UsageSummary, error) {
	s.queries = append(s.queries, period)
	if s.queryErr != nil {
		return store.UsageSummary{}, s.queryErr
	}
	return s.summaries[period], nil
}

func (s *fakeStorage) QueryDailyUsage(_ context.Context, _ time.Time, _ *time.Location, days int) ([]store.DailyUsage, error) {
	if s.dailyErr != nil {
		return nil, s.dailyErr
	}
	return make([]store.DailyUsage, days), nil
}

func (s *fakeStorage) Diagnostics(context.Context) (store.DiagnosticSnapshot, error) {
	return s.diagnostics, nil
}

func (s *fakeStorage) NextUsageBucketRevision(context.Context) (uint64, error) {
	if s.events != nil {
		*s.events = append(*s.events, "revision")
	}
	return s.nextRevision, s.nextErr
}

func (s *fakeStorage) RebuildUsageBuckets(_ context.Context, revision uint64, _ store.Previewer) (store.BucketApplyResult, error) {
	if s.events != nil {
		*s.events = append(*s.events, "rebuild")
	}
	s.rebuilt = revision
	return store.BucketApplyResult{Applied: 1}, s.rebuildErr
}

type fakeCollector struct {
	result collect.ScanResult
	err    error
	events *[]string
}

type committingCollector struct {
	database *store.DB
	batch    store.ObservationBatch
}

func (c committingCollector) Scan(ctx context.Context) (collect.ScanResult, error) {
	result, err := c.database.CommitObservations(ctx, c.batch)
	return collect.ScanResult{InsertedObservations: result.InsertedObservations, Persisted: err == nil}, err
}

func (c fakeCollector) Scan(context.Context) (collect.ScanResult, error) {
	if c.events != nil {
		*c.events = append(*c.events, "collect")
	}
	return c.result, c.err
}

func TestServiceLoadsEveryLocalPeriodAndDiagnostics(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 18, 7, 0, 0, 0, time.UTC)
	storage := &fakeStorage{
		summaries: map[store.Period]store.UsageSummary{
			store.PeriodToday:      {Period: store.PeriodToday},
			store.PeriodWeek:       {Period: store.PeriodWeek},
			store.PeriodLast30Days: {Period: store.PeriodLast30Days},
			store.PeriodAllTime:    {Period: store.PeriodAllTime},
		},
		diagnostics: store.DiagnosticSnapshot{IntegrityOK: true, SchemaVersion: 3},
	}
	location, err := time.LoadLocation("America/Vancouver")
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(storage, fakeCollector{}, nil, location, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	dashboard, err := service.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	wantPeriods := []store.Period{store.PeriodToday, store.PeriodWeek, store.PeriodLast30Days, store.PeriodAllTime}
	if !reflect.DeepEqual(storage.queries, wantPeriods) || dashboard.Timezone != "America/Vancouver" || !dashboard.GeneratedAt.Equal(now) {
		t.Fatalf("Load() dashboard=%#v queries=%v", dashboard, storage.queries)
	}
	if !dashboard.Diagnostics.IntegrityOK || dashboard.AllTime.Period != store.PeriodAllTime {
		t.Fatalf("Load() = %#v", dashboard)
	}
	if len(dashboard.Daily) != DailyDays {
		t.Fatalf("Load() daily series has %d days, want %d", len(dashboard.Daily), DailyDays)
	}
	storage.dailyErr = errors.New("synthetic failure")
	if _, err := service.Load(t.Context()); err == nil {
		t.Fatal("Load() ignored a daily usage failure")
	}
}

func TestServiceRefreshCommitsBeforeRebuildingBuckets(t *testing.T) {
	t.Parallel()
	var events []string
	storage := &fakeStorage{nextRevision: 7, events: &events}
	collector := fakeCollector{result: collect.ScanResult{Persisted: true}, events: &events}
	service, err := NewService(storage, collector, nil, time.UTC, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{"collect", "revision", "rebuild"}) || storage.rebuilt != 7 {
		t.Fatalf("Refresh() events=%v revision=%d", events, storage.rebuilt)
	}
}

func TestServiceRefreshStopsAfterCollectionFailure(t *testing.T) {
	t.Parallel()
	storage := &fakeStorage{nextRevision: 2}
	service, err := NewService(storage, fakeCollector{err: errors.New("synthetic")}, nil, time.UTC, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Refresh(t.Context()); err == nil || storage.rebuilt != 0 {
		t.Fatalf("Refresh() err=%v rebuilt=%d", err, storage.rebuilt)
	}
}

func TestNewServiceRequiresBoundariesAndTimezone(t *testing.T) {
	t.Parallel()
	storage := &fakeStorage{}
	collector := fakeCollector{}
	for _, test := range []struct {
		storage   Storage
		collector Collector
		location  *time.Location
	}{
		{collector: collector, location: time.UTC},
		{storage: storage, location: time.UTC},
		{storage: storage, collector: collector},
	} {
		if _, err := NewService(test.storage, test.collector, nil, test.location, time.Now); err == nil {
			t.Fatalf("NewService(%#v) succeeded", test)
		}
	}
}

func TestRepeatedApplicationRefreshPreservesStoredTotals(t *testing.T) {
	t.Parallel()
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "application.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close(context.Background()) })
	now := time.Date(2026, 11, 18, 7, 0, 0, 0, time.UTC)
	input, output, count := uint64(10), uint64(7), uint64(1)
	identity := store.NewSourceIdentity(normalize.SourceClaude, normalize.SurfaceCLI, "synthetic-source")
	observation := normalize.Observation{
		Source: normalize.SourceClaude, Surface: normalize.SurfaceCLI,
		SourceVersion: "synthetic-v1", SourceRecordID: "synthetic-record", OccurredAt: now.Add(-time.Hour),
		ObservedModelID: "synthetic-model", ModelResolution: normalize.ModelUnknown,
		Tokens:         normalize.TokenCounts{InputUncached: &input, Output: &output},
		CountSemantics: normalize.CountIncremental, Measurement: normalize.MeasurementReported,
		AdapterVersion: "synthetic-v1", NormalizationVersion: normalize.SchemaVersion,
	}
	capabilities := normalize.Capabilities{
		Model: normalize.CapabilityReported, InputUncached: normalize.CapabilityReported,
		InputCacheRead: normalize.CapabilityUnsupported, InputCacheWrite: normalize.CapabilityUnsupported,
		Output: normalize.CapabilityReported, ReasoningOutput: normalize.CapabilityUnsupported,
	}
	batch := store.ObservationBatch{
		Observations: []normalize.Observation{observation},
		Checkpoint:   store.Checkpoint{Source: identity, Cursor: 1, LastSourceRecordID: observation.SourceRecordID, CollectedAt: now},
		Health: store.AdapterHealth{
			Source: identity, Health: normalize.SourceHealth{
				Source: normalize.SourceClaude, Surface: normalize.SurfaceCLI, Status: normalize.HealthDetected,
				RecordCount: &count, Format: "synthetic", Capabilities: capabilities,
			},
			AdapterVersion: "synthetic-v1", CheckedAt: now, LastSuccessAt: &now,
		},
	}
	service, err := NewService(database, committingCollector{database: database, batch: batch}, nil, time.UTC, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	for iteration := 0; iteration < 2; iteration++ {
		if err := service.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		dashboard, err := service.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if dashboard.AllTime.Tokens.InputUncached.Value == nil || *dashboard.AllTime.Tokens.InputUncached.Value != input || dashboard.AllTime.Tokens.Output.Value == nil || *dashboard.AllTime.Tokens.Output.Value != output {
			t.Fatalf("refresh %d totals = %#v", iteration, dashboard.AllTime.Tokens)
		}
		if dashboard.Diagnostics.PendingOutbox != 1 {
			t.Fatalf("refresh %d pending outbox = %d", iteration, dashboard.Diagnostics.PendingOutbox)
		}
	}
}
