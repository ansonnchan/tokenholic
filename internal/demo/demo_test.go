package demo

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/application"
	"github.com/ansonnchan/tokenholic/internal/multiplayer"
	"github.com/ansonnchan/tokenholic/internal/store"
)

var moment = time.Date(2026, 11, 20, 10, 4, 0, 0, time.UTC)

var (
	boardPeriods = []multiplayer.Period{multiplayer.PeriodToday, multiplayer.PeriodWeek, multiplayer.PeriodLast30Days, multiplayer.PeriodAllTime}
	boardMetrics = []multiplayer.Metric{multiplayer.MetricAPIEstimate, multiplayer.MetricTotalTokens}
)

func summaries(dashboard application.Dashboard) []store.UsageSummary {
	return []store.UsageSummary{dashboard.Today, dashboard.Week, dashboard.Last30Days, dashboard.AllTime}
}

func load(t *testing.T, boards *Boards, scope string, period multiplayer.Period, metric multiplayer.Metric) multiplayer.Board {
	t.Helper()
	snapshot, err := boards.Load(context.Background(), scope, period, metric)
	if err != nil || snapshot.Board == nil {
		t.Fatalf("load %s %s %s: %v", scope, period, metric, err)
	}
	return *snapshot.Board
}

func amount(t *testing.T, value multiplayer.Value) uint64 {
	t.Helper()
	if value.Value == nil {
		t.Fatal("value is missing")
	}
	parsed, err := strconv.ParseUint(*value.Value, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// Every breakdown of a period must add up to that period's total, or the
// views of one dashboard would contradict each other.
func TestDashboardBreakdownsAddUp(t *testing.T) {
	t.Parallel()
	dashboard, err := App{Now: func() time.Time { return moment }, Location: time.UTC}.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var smaller uint64
	for _, summary := range summaries(dashboard) {
		total := summary.Tokens.Total()
		if total.Value == nil || *total.Value <= smaller {
			t.Fatalf("%s total %v does not grow past the shorter period's %d", summary.Period, total.Value, smaller)
		}
		smaller = *total.Value
		var sources, surfaces, models, covered uint64
		for _, source := range summary.Sources {
			sources += *source.Tokens.Total().Value
		}
		for _, surface := range summary.Surfaces {
			surfaces += *surface.Tokens.Total().Value
		}
		for _, model := range summary.Models {
			models += *model.Tokens.Total().Value
		}
		covered = summary.Coverage.PricedTokens + summary.Coverage.UnpricedTokens
		for name, sum := range map[string]uint64{"sources": sources, "surfaces": surfaces, "models": models, "coverage": covered} {
			// Each part is cut to a whole token before it is summed.
			if difference := int64(*total.Value) - int64(sum); difference < -64 || difference > 64 {
				t.Errorf("%s: %s add up to %d, total is %d", summary.Period, name, sum, *total.Value)
			}
		}
		// Some of the invented usage has no price, and the estimate says so.
		if summary.APIEstimate.State != store.AvailabilityPartial || summary.APIEstimate.Value == nil {
			t.Errorf("%s estimate = %+v, want a partial value", summary.Period, summary.APIEstimate)
		}
	}
	if len(dashboard.Daily) != application.DailyDays {
		t.Errorf("daily has %d days, want %d", len(dashboard.Daily), application.DailyDays)
	}
	if err := (App{}).Refresh(context.Background()); err != nil {
		t.Errorf("refresh = %v", err)
	}
}

// The viewer's row on a board is their own dashboard, whichever period and
// metric is shown.
func TestViewerRowMatchesTheDashboard(t *testing.T) {
	t.Parallel()
	dashboard := Dashboard(moment)
	boards := &Boards{Now: func() time.Time { return moment }}
	for index, period := range boardPeriods {
		summary := summaries(dashboard)[index]
		for _, metric := range boardMetrics {
			want := *summary.APIEstimate.Value
			if metric == multiplayer.MetricTotalTokens {
				want = *summary.Tokens.Total().Value
			}
			var found bool
			for _, row := range load(t, boards, groups[0].ID, period, metric).Rows {
				if !row.CurrentUser {
					continue
				}
				found = true
				if got := amount(t, row.Value); got != want || row.Rank == nil {
					t.Errorf("%s %s: viewer row = %d rank %v, dashboard = %d", period, metric, got, row.Rank, want)
				}
			}
			if !found {
				t.Errorf("%s %s: the viewer is not on their own group's board", period, metric)
			}
		}
	}
}

func TestBoardsRankLikeTheService(t *testing.T) {
	t.Parallel()
	boards := &Boards{Now: func() time.Time { return moment }}
	for _, scope := range []string{groups[0].ID, groups[1].ID, multiplayer.GlobalScope} {
		for _, period := range boardPeriods {
			for _, metric := range boardMetrics {
				board := load(t, boards, scope, period, metric)
				if board.Period != period || board.Metric != metric || board.TotalMembers != len(board.Rows) || board.Timezone == "" {
					t.Fatalf("%s %s %s: board header = %+v", scope, period, metric, board)
				}
				rank, last, unranked := 0, ^uint64(0), false
				for _, row := range board.Rows {
					if row.Rank == nil {
						unranked = true
						if row.UnrankedReason == "" {
							t.Errorf("%s %s %s: %s is unranked without a reason", scope, period, metric, row.DisplayName)
						}
						continue
					}
					rank++
					value := amount(t, row.Value)
					if unranked || *row.Rank != rank || value > last {
						t.Errorf("%s %s %s: %s has rank %d value %d after %d", scope, period, metric, row.DisplayName, *row.Rank, value, last)
					}
					last = value
				}
			}
		}
	}
}

// A member whose usage is mostly unpriced is left out of the estimate's
// ranking and ranked on tokens, as the service does it.
func TestCoverageDecidesWhoRanksOnEstimate(t *testing.T) {
	t.Parallel()
	boards := &Boards{}
	ranked := func(metric multiplayer.Metric) map[string]multiplayer.Row {
		rows := map[string]multiplayer.Row{}
		for _, row := range load(t, boards, groups[0].ID, multiplayer.PeriodWeek, metric).Rows {
			rows[row.DisplayName] = row
		}
		return rows
	}
	estimate, tokens := ranked(multiplayer.MetricAPIEstimate), ranked(multiplayer.MetricTotalTokens)
	if row := estimate["sam"]; row.Rank != nil || row.UnrankedReason != multiplayer.UnrankedCostCoverage {
		t.Errorf("sam on estimate = %+v, want unranked for coverage", row)
	}
	if tokens["sam"].Rank == nil {
		t.Error("sam is unranked on tokens")
	}
	for _, rows := range []map[string]multiplayer.Row{estimate, tokens} {
		if row := rows["zed"]; row.Rank != nil || row.UnrankedReason != multiplayer.UnrankedNoUsage || row.Value.Value != nil {
			t.Errorf("zed = %+v, want no usage and no value", row)
		}
	}
	if *estimate["lee"].Rank == *tokens["lee"].Rank {
		t.Error("changing the metric moved nobody")
	}
}

func TestGlobalBoardIsOptIn(t *testing.T) {
	t.Parallel()
	boards := &Boards{}
	listed := func() bool {
		for _, row := range load(t, boards, multiplayer.GlobalScope, multiplayer.PeriodWeek, multiplayer.MetricTotalTokens).Rows {
			if row.CurrentUser {
				return true
			}
		}
		return false
	}
	if listed() {
		t.Fatal("the viewer is on the global board before joining it")
	}
	if err := boards.SetGlobalParticipation(context.Background(), true); err != nil || !listed() {
		t.Fatalf("after joining: err %v, listed %v", err, listed())
	}
	if err := boards.SetGlobalParticipation(context.Background(), false); err != nil || listed() {
		t.Fatalf("after leaving: err %v, listed %v", err, listed())
	}
}

func TestActionsThatNeedAnAccountAreRefused(t *testing.T) {
	t.Parallel()
	boards := &Boards{}
	ctx := context.Background()
	if _, err := boards.Create(ctx, "anything"); !errors.Is(err, multiplayer.ErrReadOnly) {
		t.Errorf("create = %v", err)
	}
	if _, err := boards.Join(ctx, "anything"); !errors.Is(err, multiplayer.ErrReadOnly) {
		t.Errorf("join = %v", err)
	}
	if _, err := boards.Invite(ctx, groups[0].ID, time.Hour, 1); !errors.Is(err, multiplayer.ErrReadOnly) {
		t.Errorf("invite = %v", err)
	}
}
