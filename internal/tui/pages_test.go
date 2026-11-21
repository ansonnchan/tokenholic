package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/application"
	"github.com/ansonnchan/tokenholic/internal/modelcatalog"
	"github.com/ansonnchan/tokenholic/internal/multiplayer"
	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/store"
)

func TestOverviewPageLabelsPartialUnknownAndEstimates(t *testing.T) {
	t.Parallel()
	dashboard := usageDashboard(store.AvailabilityPartial, 42)
	dashboard.Today.APIEstimate = store.AggregateValue{State: store.AvailabilityUnavailable}
	dashboard.Week.APIEstimate = store.AggregateValue{State: store.AvailabilityUnavailable}
	dashboard.Last30Days.APIEstimate = store.AggregateValue{State: store.AvailabilityUnavailable}
	dashboard.AllTime.APIEstimate = store.AggregateValue{State: store.AvailabilityUnavailable}
	dashboard.Diagnostics.PendingOutbox = 3
	page := overviewPage(dashboard)
	joined := strings.Join(page.Lines, "\n")
	for _, want := range []string{
		"Total tokens: 42 (partial)",
		"API-equivalent estimate: unavailable",
		"  5-minute: unavailable",
		"  1-hour: unavailable",
		"Estimates here are a local preview",
		"Today includes partial or unavailable values",
		"Pending local buckets: 3",
	} {
		if want == page.Warning {
			continue
		}
		if !strings.Contains(joined, want) {
			t.Fatalf("overview lines = %q, want %q", joined, want)
		}
	}
	if page.Warning != "Today includes partial or unavailable values" || strings.Contains(joined, "$0.000000") {
		t.Fatalf("overview page = %#v", page)
	}
}

func TestModelsPagePreservesAttributionQuality(t *testing.T) {
	t.Parallel()
	value := uint64(10)
	dashboard := application.Dashboard{AllTime: store.UsageSummary{Models: []store.ModelBreakdown{
		{
			ObservedModelID: "unmapped-model", Resolution: normalize.ModelUnknown,
			Tokens:      store.TokenBreakdown{InputUncached: store.AggregateValue{Value: &value, State: store.AvailabilityComplete}},
			APIEstimate: store.AggregateValue{State: store.AvailabilityUnavailable},
		},
	}}}
	page := modelsPage(dashboard)
	joined := strings.Join(page.Lines, "\n")
	if !strings.Contains(joined, "canonical unavailable") || !strings.Contains(joined, "attribution unknown") || !strings.Contains(joined, "estimate unavailable") {
		t.Fatalf("models page = %#v", page)
	}
	if page.Warning == "" {
		t.Fatal("unknown attribution did not produce a warning")
	}
}

func TestModelsPageLabelsAPartlyPricedModel(t *testing.T) {
	t.Parallel()
	value, estimate := uint64(10), uint64(1_000_000)
	canonical := "model-tiered"
	page := modelsPage(application.Dashboard{AllTime: store.UsageSummary{Models: []store.ModelBreakdown{{
		ObservedModelID: "model-tiered", CanonicalModelID: &canonical, Resolution: normalize.ModelReported,
		UnpricedReason: modelcatalog.ReasonPromptLengthUnproven,
		Tokens:         store.TokenBreakdown{InputUncached: store.AggregateValue{Value: &value, State: store.AvailabilityComplete}},
		APIEstimate:    store.AggregateValue{Value: &estimate, State: store.AvailabilityPartial},
	}}}})
	joined := strings.Join(page.Lines, "\n")
	if !strings.Contains(joined, "partly unpriced: price depends on prompt length") || strings.Contains(joined, "no estimate") {
		t.Fatalf("models page = %q, want the reason labelled as covering part of the usage", joined)
	}
}

func TestModelsPageExplainsMissingEstimates(t *testing.T) {
	t.Parallel()
	value := uint64(10)
	canonical := "model-tiered"
	tokens := store.TokenBreakdown{InputUncached: store.AggregateValue{Value: &value, State: store.AvailabilityComplete}}
	unavailable := store.AggregateValue{State: store.AvailabilityUnavailable}
	tests := []struct {
		reason string
		want   string
	}{
		{reason: modelcatalog.ReasonPromptLengthUnproven, want: "no estimate: price depends on prompt length"},
		{reason: modelcatalog.ReasonTierRateUnavailable, want: "no estimate: no reviewed price for this service tier"},
		{reason: modelcatalog.ReasonUnknownModel, want: "no estimate: model is not in this version's price list"},
		{reason: modelcatalog.ReasonRoutingLabel, want: "no estimate: a routing label"},
		{reason: "reason_from_a_newer_catalog", want: "no estimate: reason_from_a_newer_catalog"},
	}
	for _, tt := range tests {
		t.Run(tt.reason, func(t *testing.T) {
			page := modelsPage(application.Dashboard{AllTime: store.UsageSummary{Models: []store.ModelBreakdown{{
				ObservedModelID: "model-tiered", CanonicalModelID: &canonical, Resolution: normalize.ModelReported,
				UnpricedReason: tt.reason, Tokens: tokens, APIEstimate: unavailable,
			}}}})
			if joined := strings.Join(page.Lines, "\n"); !strings.Contains(joined, tt.want) {
				t.Fatalf("models page = %q, want %q", joined, tt.want)
			}
		})
	}
	priced := modelsPage(application.Dashboard{AllTime: store.UsageSummary{Models: []store.ModelBreakdown{{
		ObservedModelID: "model-a", CanonicalModelID: &canonical, Resolution: normalize.ModelReported,
		Tokens: tokens, APIEstimate: store.AggregateValue{Value: &value, State: store.AvailabilityComplete},
	}}}})
	if strings.Contains(strings.Join(priced.Lines, "\n"), "no estimate") {
		t.Fatalf("priced model shows a missing-estimate reason: %#v", priced.Lines)
	}
}

func TestMissingSupportedSourcesHaveActionableEmptyPage(t *testing.T) {
	t.Parallel()
	dashboard := application.Dashboard{Diagnostics: store.DiagnosticSnapshot{
		AdapterHealth: []store.StoredAdapterHealth{
			{Health: normalize.SourceHealth{Source: normalize.SourceClaude, Surface: normalize.SurfaceCLI, Status: normalize.HealthNotFound}},
			{Health: normalize.SourceHealth{Source: normalize.SourceCodex, Surface: normalize.SurfaceSharedLocal, Status: normalize.HealthNotFound}},
		},
	}}
	for _, page := range []Page{overviewPage(dashboard), modelsPage(dashboard)} {
		joined := strings.Join(page.Lines, "\n")
		if page.EmptyMessage != "No supported coding-agent usage found." {
			t.Fatalf("empty message = %q", page.EmptyMessage)
		}
		for _, want := range []string{"Claude Code/Desktop", "Codex CLI/Desktop", "press r", "tokenholic doctor"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("empty page = %#v, want %q", page, want)
			}
		}
		if page.Warning != "" {
			t.Fatalf("missing optional sources produced warning = %q", page.Warning)
		}
	}
}

func TestMissingSourceFallbackDoesNotHideExistingUsage(t *testing.T) {
	t.Parallel()
	dashboard := usageDashboard(store.AvailabilityComplete, 1)
	dashboard.Diagnostics.AdapterHealth = []store.StoredAdapterHealth{{
		Health: normalize.SourceHealth{Source: normalize.SourceClaude, Surface: normalize.SurfaceCLI, Status: normalize.HealthNotFound},
	}}
	if page := overviewPage(dashboard); page.EmptyMessage != "" {
		t.Fatalf("existing usage rendered missing-source fallback = %#v", page)
	}
}

func TestSourcesAndSettingsRemainLocalAndExplicit(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	dashboard := application.Dashboard{
		GeneratedAt: now, Timezone: "America/Vancouver",
		Diagnostics: store.DiagnosticSnapshot{
			IntegrityOK: true, SchemaVersion: 3, SupportedSchemaVersion: 3,
			AdapterHealth: []store.StoredAdapterHealth{{Health: normalize.SourceHealth{
				Source: normalize.SourceClaude, Surface: normalize.SurfaceDesktopCowork, Status: normalize.HealthPermissionDenied,
			}}},
		},
	}
	sources := sourcesPage(dashboard)
	if joined := strings.Join(sources.Lines, "\n"); !strings.Contains(joined, "records unavailable") || !strings.Contains(joined, "last success never") || sources.Warning == "" {
		t.Fatalf("sources page = %#v", sources)
	}
	settings := strings.Join(settingsPage(dashboard).Lines, "\n")
	for _, want := range []string{"No account, server, group, or synchronization", "prompts, responses, source code", "r collect and refresh", "tokenholic doctor"} {
		if !strings.Contains(settings, want) {
			t.Fatalf("settings page = %q, want %q", settings, want)
		}
	}
}

func TestCompleteZeroIsNotUnavailable(t *testing.T) {
	t.Parallel()
	zero := uint64(0)
	value := store.AggregateValue{Value: &zero, State: store.AvailabilityComplete}
	if got := formatValue(value); got != "0" {
		t.Fatalf("formatValue() = %q", got)
	}
	if got := formatEstimate(value); got != "$0.000000 (estimate)" {
		t.Fatalf("formatEstimate() = %q", got)
	}
}

func TestTotalTokensDoesNotDoubleCountReasoningSubset(t *testing.T) {
	t.Parallel()
	input, output, reasoning := uint64(10), uint64(7), uint64(3)
	tokens := store.TokenBreakdown{
		InputUncached:   store.AggregateValue{Value: &input, State: store.AvailabilityComplete},
		InputCacheRead:  store.AggregateValue{Value: new(uint64), State: store.AvailabilityComplete},
		InputCacheWrite: store.AggregateValue{Value: new(uint64), State: store.AvailabilityComplete},
		Output:          store.AggregateValue{Value: &output, State: store.AvailabilityComplete},
		ReasoningOutput: store.AggregateValue{Value: &reasoning, State: store.AvailabilityComplete},
	}
	if got := totalTokens(tokens); got != "17" {
		t.Fatalf("totalTokens() = %q, want 17", got)
	}
}

func TestGroupPageKeepsPartialAndEstimateMeaningVisible(t *testing.T) {
	t.Parallel()
	partial, estimate := "12345", "1500001"
	rank, climbed, priced := 1, 2, 9425
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	page := groupPage(multiplayer.Snapshot{
		Groups:   []multiplayer.Group{{ID: "group", Name: "Quiet", Timezone: "UTC", Role: "member"}},
		Selected: multiplayer.Group{ID: "group", Name: "Quiet", Timezone: "UTC", Role: "member"},
		Board: &multiplayer.Board{
			Period: multiplayer.PeriodToday, Metric: multiplayer.MetricAPIEstimate, GeneratedAt: now,
			Comparison:                   multiplayer.Comparison{Kind: multiplayer.ComparisonPreviousPeriod},
			CostRankMinPricedBasisPoints: 9500,
			Rows: []multiplayer.Row{
				{Rank: &rank, DisplayName: "Complete", Value: multiplayer.Value{Value: &estimate, State: multiplayer.AvailabilityComplete},
					Trend: multiplayer.Trend{RankChange: &climbed}},
				{DisplayName: "Partial", CurrentUser: true, Value: multiplayer.Value{Value: &partial, State: multiplayer.AvailabilityPartial},
					UnrankedReason: multiplayer.UnrankedCostCoverage, Coverage: multiplayer.Coverage{PricedBasisPoints: &priced},
					Trend: multiplayer.Trend{New: true}, Plausibility: multiplayer.Plausibility{DailyTokensAboveLimit: true}},
			},
		},
	})
	joined := strings.Join(page.Lines, "\n")
	for _, want := range []string{
		"Metric: API estimate", "Movement is against yesterday", "$1.500001 (estimate) · up 2",
		"at least $0.012345 (estimate) · new", "94.25% of tokens priced; an estimate is ranked from 95%",
		"above the plausibility limit", "Your standing: unranked",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("group page = %q, want %q", joined, want)
		}
	}
	if page.Warning == "" {
		t.Fatal("partial leaderboard did not warn")
	}
}

func TestCleanLineRemovesTerminalControlCharacters(t *testing.T) {
	t.Parallel()
	cleaned := cleanLine("safe\x1b[31m\nnext")
	if strings.ContainsRune(cleaned, '\x1b') || strings.ContainsRune(cleaned, '\n') || !strings.Contains(cleaned, "safe") {
		t.Fatalf("cleanLine() = %q", cleaned)
	}
}
