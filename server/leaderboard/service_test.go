package leaderboard

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	serveraccount "github.com/ansonnchan/tokenholic/server/account"
	serverauth "github.com/ansonnchan/tokenholic/server/auth"
	servergroup "github.com/ansonnchan/tokenholic/server/group"
)

func TestWindowForCalendarPeriodsAcrossDST(t *testing.T) {
	location, err := time.LoadLocation("America/Vancouver")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 8, 12, 30, 0, 0, location)
	tests := []struct {
		period Period
		start  time.Time
	}{
		{period: PeriodToday, start: time.Date(2026, 3, 8, 0, 0, 0, 0, location)},
		{period: PeriodWeek, start: time.Date(2026, 3, 2, 0, 0, 0, 0, location)},
		{period: PeriodLast30Days, start: time.Date(2026, 2, 7, 0, 0, 0, 0, location)},
	}
	for _, test := range tests {
		t.Run(string(test.period), func(t *testing.T) {
			window, err := WindowFor(test.period, now, location)
			if err != nil {
				t.Fatal(err)
			}
			if window.Start == nil || !window.Start.Equal(test.start.UTC()) || !window.End.Equal(now.UTC()) {
				t.Fatalf("WindowFor() = %#v, want %s through %s", window, test.start.UTC(), now.UTC())
			}
		})
	}
	allTime, err := WindowFor(PeriodAllTime, now, location)
	if err != nil || allTime.Start != nil || !allTime.End.Equal(now.UTC()) {
		t.Fatalf("all-time window = %#v, %v", allTime, err)
	}
}

func TestComparisonForUsesThePreviousPeriodAtTheSameElapsedTime(t *testing.T) {
	// Los Angeles still changes its clocks twice a year; the cases below
	// depend on both changes.
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	at := func(year int, month time.Month, day, hour, minute int) time.Time {
		return time.Date(year, month, day, hour, minute, 0, 0, location)
	}
	tests := []struct {
		name      string
		period    Period
		now       time.Time
		wantStart time.Time
		wantEnd   time.Time
	}{
		// Wednesday 09:30: yesterday from midnight to 09:30.
		{name: "today", period: PeriodToday, now: at(2026, 11, 18, 9, 30), wantStart: at(2026, 11, 17, 0, 0), wantEnd: at(2026, 11, 17, 9, 30)},
		// Wednesday 09:30 of a week that began Monday the 16th.
		{name: "week", period: PeriodWeek, now: at(2026, 11, 18, 9, 30), wantStart: at(2026, 11, 9, 0, 0), wantEnd: at(2026, 11, 11, 9, 30)},
		// 30 local dates ending today began October 20; the 30 before began
		// September 20. The current window holds the extra hour of the day
		// clocks went back, so the same elapsed time ends an hour later on
		// the clock.
		{name: "last 30 days", period: PeriodLast30Days, now: at(2026, 11, 18, 9, 30), wantStart: at(2026, 9, 20, 0, 0), wantEnd: at(2026, 10, 19, 10, 30)},
		// Clocks went back on November 1, a 25-hour day. Yesterday still
		// starts at its own local midnight, and elapsed time is measured in
		// hours actually passed: 9.5 hours after 00:00 PDT is 08:30 PST.
		{name: "day after clocks go back", period: PeriodToday, now: at(2026, 11, 2, 9, 30), wantStart: at(2026, 11, 1, 0, 0), wantEnd: at(2026, 11, 1, 8, 30)},
		// Clocks went forward on March 8, a 23-hour day. At 23:30 on that day
		// 22.5 hours have passed; the same span of the previous day ends 22:30.
		{name: "day clocks go forward", period: PeriodToday, now: at(2026, 3, 8, 23, 30), wantStart: at(2026, 3, 7, 0, 0), wantEnd: at(2026, 3, 7, 22, 30)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			window, err := WindowFor(tt.period, tt.now, location)
			if err != nil {
				t.Fatal(err)
			}
			comparison, err := ComparisonFor(tt.period, window, location)
			if err != nil {
				t.Fatal(err)
			}
			if comparison.Kind != ComparisonPreviousPeriod || comparison.Window.Start == nil ||
				!comparison.Window.Start.Equal(tt.wantStart) || !comparison.Window.End.Equal(tt.wantEnd) {
				t.Fatalf("ComparisonFor() = %s %v through %v, want %v through %v", comparison.Kind,
					comparison.Window.Start, comparison.Window.End, tt.wantStart.UTC(), tt.wantEnd.UTC())
			}
			if comparison.Window.End.After(*window.Start) {
				t.Fatal("the comparison window runs into the current one")
			}
		})
	}

	now := at(2026, 11, 18, 9, 30)
	window, err := WindowFor(PeriodAllTime, now, location)
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := ComparisonFor(PeriodAllTime, window, location)
	if err != nil || comparison.Kind != ComparisonDayEarlier || comparison.Window.Start != nil || !comparison.Window.End.Equal(now.Add(-24*time.Hour)) {
		t.Fatalf("all-time comparison = %#v, %v; want everything up to 24 hours earlier", comparison, err)
	}
	if _, err := ComparisonFor(PeriodToday, Window{End: now}, location); !errors.Is(err, servergroup.ErrInvalidInput) {
		t.Fatalf("calendar period without a start error = %v", err)
	}
}

func TestNewCoverageRoundsThePricedShareDown(t *testing.T) {
	tests := []struct {
		priced, unpriced string
		want             int
		none             bool
	}{
		{priced: "100", unpriced: "0", want: 10_000},
		{priced: "95", unpriced: "5", want: 9_500},
		{priced: "94999", unpriced: "5001", want: 9_499},
		{priced: "0", unpriced: "7", want: 0},
		{priced: "0", unpriced: "0", none: true},
		// Beyond uint64: sums stay exact.
		{priced: "190000000000000000000", unpriced: "10000000000000000000", want: 9_500},
	}
	for _, tt := range tests {
		coverage := NewCoverage(tt.priced, tt.unpriced)
		if tt.none != (coverage.PricedBasisPoints == nil) || !tt.none && *coverage.PricedBasisPoints != tt.want {
			t.Errorf("NewCoverage(%s, %s) = %v, want %d (none %t)", tt.priced, tt.unpriced, coverage.PricedBasisPoints, tt.want, tt.none)
		}
	}
}

// member builds a row from current and previous values. A value of -1 is a
// window without usage; coverage is in basis points of 10,000 tokens.
func member(name string, current, previous int64, state serveraccount.Availability, currentBasisPoints, previousBasisPoints int) Row {
	value := func(v int64, state serveraccount.Availability) serveraccount.Value {
		if v < 0 {
			return serveraccount.Value{State: serveraccount.AvailabilityEmpty}
		}
		text := strconv.FormatInt(v, 10)
		return serveraccount.Value{Value: &text, State: state}
	}
	coverage := func(basisPoints int) Coverage {
		return NewCoverage(strconv.Itoa(basisPoints), strconv.Itoa(10_000-basisPoints))
	}
	return Row{
		UserID: "user-" + name, DisplayName: name, Value: value(current, state), Coverage: coverage(currentBasisPoints),
		Trend: Trend{PreviousValue: value(previous, serveraccount.AvailabilityComplete)}, PreviousCoverage: coverage(previousBasisPoints),
	}
}

func TestRankTokensWithTiesLowerBoundsAndTrends(t *testing.T) {
	complete, partial := serveraccount.AvailabilityComplete, serveraccount.AvailabilityPartial
	board := Board{Metric: MetricTotalTokens, Rows: []Row{
		member("Echo", -1, 40, complete, 10_000, 10_000),  // had usage before, none now
		member("Delta", 70, -1, complete, 10_000, 10_000), // first period with usage
		member("Beta", 100, 10, complete, 0, 0),           // entirely unpriced tokens still rank
		member("Alpha", 100, 30, partial, 10_000, 10_000), // a lower bound is ranked and tied
		member("Charlie", 50, 20, complete, 10_000, 10_000),
		member("Foxtrot", -1, -1, complete, 10_000, 10_000), // never any usage
	}}
	board.Rows[len(board.Rows)-1].Value = serveraccount.Value{State: serveraccount.AvailabilityEmpty}
	Rank(&board)
	type result struct {
		name     string
		rank     int // 0 is unranked
		reason   UnrankedReason
		previous int
		change   int // 99 is nil
		isNew    bool
	}
	want := []result{
		{name: "Alpha", rank: 1, previous: 2, change: 1},
		{name: "Beta", rank: 1, previous: 4, change: 3},
		{name: "Delta", rank: 3, change: 99, isNew: true},
		{name: "Charlie", rank: 4, previous: 3, change: -1},
		{name: "Echo", reason: UnrankedNoUsage, previous: 1, change: 99},
		{name: "Foxtrot", reason: UnrankedNoUsage, change: 99},
	}
	assertRanking(t, board.Rows, func(row Row) result {
		got := result{name: row.DisplayName, reason: row.UnrankedReason, change: 99, isNew: row.Trend.New}
		if row.Rank != nil {
			got.rank = *row.Rank
		}
		if row.Trend.PreviousRank != nil {
			got.previous = *row.Trend.PreviousRank
		}
		if row.Trend.RankChange != nil {
			got.change = *row.Trend.RankChange
		}
		return got
	}, want)
	if board.CostRankMinPricedBasisPoints != 9_500 || board.PlausibleDailyTokens != "5000000000" {
		t.Fatalf("board rules = %d/%s", board.CostRankMinPricedBasisPoints, board.PlausibleDailyTokens)
	}
}

func TestRankEstimateRequiresCostCoverage(t *testing.T) {
	complete, partial := serveraccount.AvailabilityComplete, serveraccount.AvailabilityPartial
	unavailable := member("Golf", 0, 0, complete, 0, 0)
	unavailable.Value = serveraccount.Value{State: serveraccount.AvailabilityUnavailable}
	board := Board{Metric: MetricAPIEstimate, Rows: []Row{
		member("Alpha", 900, 100, complete, 9_499, 10_000),    // largest value, one basis point short
		member("Beta", 500, 300, partial, 9_500, 9_000),       // exactly on the threshold; was below it before
		member("Charlie", 500, 200, complete, 10_000, 10_000), // ties Beta
		member("Delta", 400, 900, complete, 9_800, 9_800),
		member("Echo", 50, 50, complete, 0, 10_000), // entirely unpriced now
		unavailable, // usage, but no estimate at all
		member("Hotel", -1, -1, complete, 10_000, 10_000),
	}}
	Rank(&board)
	type result struct {
		name     string
		rank     int
		reason   UnrankedReason
		previous int
		change   int
	}
	want := []result{
		{name: "Beta", rank: 1, change: 99},                // unranked before: no movement to report
		{name: "Charlie", rank: 1, previous: 2, change: 1}, // Delta 900 led; Beta was unranked
		{name: "Delta", rank: 3, previous: 1, change: -2},
		{name: "Alpha", reason: UnrankedCostCoverage, previous: 3, change: 99},
		{name: "Echo", reason: UnrankedCostCoverage, previous: 4, change: 99},
		{name: "Golf", reason: UnrankedValueUnavailable, change: 99},
		{name: "Hotel", reason: UnrankedNoUsage, change: 99},
	}
	assertRanking(t, board.Rows, func(row Row) result {
		got := result{name: row.DisplayName, reason: row.UnrankedReason, change: 99}
		if row.Rank != nil {
			got.rank = *row.Rank
		}
		if row.Trend.PreviousRank != nil {
			got.previous = *row.Trend.PreviousRank
		}
		if row.Trend.RankChange != nil {
			got.change = *row.Trend.RankChange
		}
		return got
	}, want)
	// An unranked estimate is still shown, as a lower bound.
	if board.Rows[3].Value.Value == nil || *board.Rows[3].Value.Value != "900" {
		t.Fatalf("unranked estimate = %#v, want its value kept", board.Rows[3].Value)
	}
}

func assertRanking[T comparable](t *testing.T, rows []Row, describe func(Row) T, want []T) {
	t.Helper()
	if len(rows) != len(want) {
		t.Fatalf("rows = %d, want %d", len(rows), len(want))
	}
	for index, row := range rows {
		if got := describe(row); got != want[index] {
			t.Errorf("row %d = %+v, want %+v", index, got, want[index])
		}
	}
}

func TestServiceAuthenticatesValidatesAndMarksCurrentUser(t *testing.T) {
	repository := &fakeRepository{board: Board{Rows: []Row{{UserID: testUserID, DisplayName: "current"}}}}
	service, err := NewService(fakeAuthenticator{}, repository)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) }
	board, err := service.Get(t.Context(), "credential", testGroupID, PeriodToday, MetricTotalTokens)
	if err != nil {
		t.Fatal(err)
	}
	if repository.query.GroupID != testGroupID || repository.query.GeneratedAt.IsZero() {
		t.Fatalf("query = %#v", repository.query)
	}
	if !board.Rows[0].CurrentUser {
		t.Fatal("current user was not marked")
	}
	if _, err := service.Get(t.Context(), "credential", "bad", PeriodToday, MetricTotalTokens); !errors.Is(err, servergroup.ErrNotFound) {
		t.Fatalf("bad group error = %v", err)
	}
	if _, err := service.Get(t.Context(), "credential", testGroupID, "future", MetricTotalTokens); !errors.Is(err, servergroup.ErrInvalidInput) {
		t.Fatalf("bad period error = %v", err)
	}
	// Output tokens are a personal-view category, no longer a ranking metric.
	if _, err := service.Get(t.Context(), "credential", testGroupID, PeriodToday, "output_tokens"); !errors.Is(err, servergroup.ErrInvalidInput) {
		t.Fatalf("retired metric error = %v", err)
	}
	unauthorized, err := NewService(fakeAuthenticator{err: serverauth.ErrUnauthorized}, repository)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unauthorized.Get(t.Context(), "bad", "bad", "future", "future"); !errors.Is(err, serverauth.ErrUnauthorized) {
		t.Fatalf("unauthorized validation error = %v", err)
	}
}

const (
	testGroupID = "50000000-0000-4000-8000-000000000001"
	testUserID  = "50000000-0000-4000-8000-000000000002"
)

type fakeAuthenticator struct {
	err error
}

func (f fakeAuthenticator) Authenticate(context.Context, string) (serverauth.Principal, error) {
	return serverauth.Principal{UserID: testUserID}, f.err
}

type fakeRepository struct {
	query Query
	board Board
	err   error
}

func (f *fakeRepository) Leaderboard(_ context.Context, _ serverauth.Principal, query Query) (Board, error) {
	f.query = query
	return f.board, f.err
}
