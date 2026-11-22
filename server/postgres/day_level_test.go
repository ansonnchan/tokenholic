package postgres

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/modelcatalog"
	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/store"
	protocol "github.com/ansonnchan/tokenholic/internal/sync"
	"github.com/ansonnchan/tokenholic/server/leaderboard"
	serversync "github.com/ansonnchan/tokenholic/server/sync"
)

type dayUsage struct {
	source normalize.Source
	day    int
	model  string
	tokens uint64
}

// dayBatch serializes day-level buckets through the local store and outbox,
// with one ordinary Claude minute beside them.
func dayBatch(t *testing.T, revision uint64, usages []dayUsage) protocol.Batch {
	t.Helper()
	database, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "client.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close(context.Background()) })
	minute := uint64(9)
	unavailable := store.AggregateValue{State: store.AvailabilityUnavailable}
	preview := store.Preview{Resolution: normalize.ModelUnknown, APIEstimate: unavailable}
	buckets := []store.UsageBucket{{
		Start:  time.Date(2026, 1, 2, 17, 30, 0, 0, time.UTC),
		Source: normalize.SourceClaude, Surface: normalize.SurfaceCLI, ObservedModelID: "model-synthetic",
		ModelEvidence: normalize.ModelReported, Measurement: normalize.MeasurementReported, NormalizationVersion: "1",
		Tokens: store.TokenBreakdown{
			InputUncached: unavailable, InputCacheRead: unavailable, InputCacheWrite: unavailable,
			InputCacheWrite5m: unavailable, InputCacheWrite1h: unavailable,
			Output: store.AggregateValue{Value: &minute, State: store.AvailabilityComplete}, ReasoningOutput: unavailable,
		},
		Preview: preview, Revision: revision,
	}}
	for _, usage := range usages {
		tokens := usage.tokens
		evidence := normalize.ModelReported
		if usage.model == "" {
			evidence = normalize.ModelUnknown
		}
		buckets = append(buckets, store.UsageBucket{
			Start:  time.Date(2026, 1, usage.day, 0, 0, 0, 0, time.UTC),
			Source: usage.source, Surface: normalize.SurfaceCLI, ObservedModelID: usage.model,
			ModelEvidence: evidence, Measurement: normalize.MeasurementReported, NormalizationVersion: "1",
			Granularity: normalize.GranularityDay,
			Tokens: store.TokenBreakdown{
				InputUncached:  store.AggregateValue{Value: &tokens, State: store.AvailabilityComplete},
				InputCacheRead: unavailable, InputCacheWrite: unavailable,
				InputCacheWrite5m: unavailable, InputCacheWrite1h: unavailable,
				Output: unavailable, ReasoningOutput: unavailable,
			},
			Preview: preview, Revision: revision,
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

// A day-level bucket is stored as a day, replaced as its day grows, counted
// once however often it is uploaded, and reported to boards as day-level.
func TestPostgresDayLevelBucketsAreAbsoluteAndLabelled(t *testing.T) {
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
	service, err := serversync.NewService(staticLeaderboardAuthenticator{principal: principal}, repository, bundledEngine(t))
	if err != nil {
		t.Fatal(err)
	}
	usages := func(scale uint64) []dayUsage {
		return []dayUsage{
			{source: normalize.SourceGemini, day: 2, model: "model-synthetic", tokens: 1000 * scale},
			{source: normalize.SourceGemini, day: 2, model: "", tokens: 10 * scale},
			{source: normalize.SourceGemini, day: 3, model: "model-synthetic", tokens: 100 * scale},
			{source: normalize.SourceCopilot, day: 2, model: "model-synthetic", tokens: 1000 * scale},
			{source: normalize.SourceGrok, day: 2, model: "model-synthetic", tokens: 1000 * scale},
			{source: normalize.SourceOpenCode, day: 2, model: "model-synthetic", tokens: 1000 * scale},
			{source: normalize.SourceQwen, day: 2, model: "model-synthetic", tokens: 1000 * scale},
		}
	}
	stored := func() (days, minutes int, dayTokens, minuteTokens string) {
		t.Helper()
		if err := pool.QueryRow(ctx, `
			SELECT COUNT(*) FILTER (WHERE interval_seconds = 86400), COUNT(*) FILTER (WHERE interval_seconds = 60),
			       COALESCE(SUM(input_uncached_tokens) FILTER (WHERE interval_seconds = 86400), 0)::text,
			       COALESCE(SUM(output_tokens) FILTER (WHERE interval_seconds = 60), 0)::text
			FROM usage_buckets WHERE device_id = $1::uuid`, principal.DeviceID).Scan(&days, &minutes, &dayTokens, &minuteTokens); err != nil {
			t.Fatal(err)
		}
		return days, minutes, dayTokens, minuteTokens
	}
	// The day's first reading, the same reading again, then the day grown
	// threefold. Each upload carries the whole value so far.
	for step, upload := range []struct {
		revision uint64
		scale    uint64
		status   protocol.AckStatus
	}{
		{revision: 1, scale: 1, status: protocol.AckApplied},
		{revision: 1, scale: 1, status: protocol.AckUnchanged},
		{revision: 2, scale: 3, status: protocol.AckApplied},
		{revision: 2, scale: 3, status: protocol.AckUnchanged},
		// An older reading arriving late never lowers the stored day.
		{revision: 1, scale: 1, status: protocol.AckStale},
	} {
		response, err := service.Apply(ctx, "credential", dayBatch(t, upload.revision, usages(upload.scale)))
		if err != nil {
			t.Fatalf("step %d: Apply() = %v", step, err)
		}
		for _, acknowledgement := range response.Acknowledgements {
			if acknowledgement.Status != upload.status {
				t.Fatalf("step %d: acknowledgement = %#v, want %s", step, acknowledgement, upload.status)
			}
		}
		scale := upload.scale
		if upload.status == protocol.AckStale {
			scale = 3
		}
		days, minutes, dayTokens, minuteTokens := stored()
		if want := strconv.FormatUint(5110*scale, 10); days != 7 || minutes != 1 || dayTokens != want || minuteTokens != "9" {
			t.Fatalf("step %d: stored %d days holding %s and %d minutes holding %s; want 7 days holding %s and 1 minute holding 9",
				step, days, dayTokens, minutes, minuteTokens, want)
		}
	}

	// A day is filed at the start of its UTC day and covers nothing else.
	for name, statement := range map[string]string{
		"a day filed inside the day": `UPDATE usage_buckets SET bucket_start = bucket_start + interval '1 hour' WHERE interval_seconds = 86400`,
		"an hour":                    `UPDATE usage_buckets SET interval_seconds = 3600`,
		"a minute bucket as a day":   `UPDATE usage_buckets SET interval_seconds = 86400 WHERE interval_seconds = 60`,
	} {
		if _, err := pool.Exec(ctx, statement); err == nil {
			t.Fatalf("the schema accepted %s", name)
		}
	}

	// The global board counts the member's day-level buckets in the window,
	// so a reader can label the day boundary as approximate.
	if _, err := pool.Exec(ctx, `UPDATE users SET global_opt_in = true WHERE id = $1::uuid`, principal.UserID); err != nil {
		t.Fatal(err)
	}
	boards, err := NewLeaderboardRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		period   leaderboard.Period
		at       time.Time
		dayLevel int64
		total    string
	}{
		{period: leaderboard.PeriodAllTime, at: time.Date(2026, 1, 9, 12, 0, 0, 0, time.UTC), dayLevel: 7, total: "15339"},
		// On January 3 UTC only that day's Gemini bucket is in the window.
		{period: leaderboard.PeriodToday, at: time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC), dayLevel: 1, total: "300"},
		// A day with no day-level bucket reports none.
		{period: leaderboard.PeriodToday, at: time.Date(2026, 1, 4, 12, 0, 0, 0, time.UTC), dayLevel: 0, total: ""},
	} {
		board, err := boards.GlobalLeaderboard(ctx, leaderboard.Query{Period: test.period, Metric: leaderboard.MetricTotalTokens, GeneratedAt: test.at})
		if err != nil {
			t.Fatal(err)
		}
		row := leaderboardRowByUser(t, board.Rows, principal.UserID)
		if row.Limitations.DayLevelBuckets != test.dayLevel {
			t.Fatalf("%s at %s: day-level buckets = %d, want %d", test.period, test.at.Format(time.DateOnly), row.Limitations.DayLevelBuckets, test.dayLevel)
		}
		if test.total == "" {
			if row.Value.Value != nil {
				t.Fatalf("%s at %s: value = %v, want none", test.period, test.at.Format(time.DateOnly), *row.Value.Value)
			}
			continue
		}
		if row.Value.Value == nil || *row.Value.Value != test.total {
			t.Fatalf("%s at %s: value = %v, want %s", test.period, test.at.Format(time.DateOnly), row.Value.Value, test.total)
		}
	}
}
