package leaderboard

import (
	"context"
	"errors"
	"math/big"
	"sort"
	"time"

	serveraccount "github.com/ansonnchan/tokenholic/server/account"
	serverauth "github.com/ansonnchan/tokenholic/server/auth"
	servergroup "github.com/ansonnchan/tokenholic/server/group"
)

// Period is a timezone-relative leaderboard window.
type Period string

const (
	PeriodToday      Period = "today"
	PeriodWeek       Period = "week"
	PeriodLast30Days Period = "last_30_days"
	PeriodAllTime    Period = "all_time"
)

// Metric is the value a board ranks by. Token categories stay in personal
// views; they are not ranking metrics.
type Metric string

const (
	// MetricTotalTokens ranks by uncached input, cache read, cache write, and
	// output tokens together, priced or not.
	MetricTotalTokens Metric = "total_tokens"
	// MetricAPIEstimate ranks by the API-equivalent estimate in micro-dollars.
	// It is never spending.
	MetricAPIEstimate Metric = "api_estimate"
)

const (
	// CostRankMinPricedBasisPoints is the share of a member's tokens that
	// must be priced before their estimate is ranked: 95%. Below it the
	// estimate omits too much usage to compare with anyone else's.
	CostRankMinPricedBasisPoints = 9500

	// PlausibleDailyTokens is the advisory limit on one member's tokens in a
	// single local day. It is about ten times the heaviest day observed while
	// building this, 432 million tokens. Usage is self-reported, so a total
	// above it is flagged for a human to judge. It is never capped, dropped,
	// or excluded from a rank.
	PlausibleDailyTokens = 5_000_000_000
)

// Window is the UTC query range for one period. End is exclusive and Start is
// nil only when the range has no lower bound.
type Window struct {
	Start *time.Time `json:"start"`
	End   time.Time  `json:"end"`
}

// ComparisonKind names what a board's trends are measured against.
type ComparisonKind string

const (
	// ComparisonPreviousPeriod is the preceding day, week, or 30 days, cut
	// off at the same elapsed time as the current window.
	ComparisonPreviousPeriod ComparisonKind = "previous_period"
	// ComparisonDayEarlier is the same all-time board as it stood 24 hours
	// before, because all-time has no preceding period.
	ComparisonDayEarlier ComparisonKind = "24h_earlier"
)

// Comparison is the window every row's trend is measured against.
type Comparison struct {
	Kind   ComparisonKind `json:"kind"`
	Window Window         `json:"window"`
}

// SourceMix is one source's total-token contribution and its coverage.
type SourceMix struct {
	Source      string              `json:"source"`
	TotalTokens serveraccount.Value `json:"total_tokens"`
}

// ModelUsage describes the most-used model without hiding resolution quality.
type ModelUsage struct {
	ModelID     string              `json:"model_id"`
	Resolution  string              `json:"resolution"`
	TotalTokens serveraccount.Value `json:"total_tokens"`
}

// Limitations makes incomplete model and price resolution visible per member.
type Limitations struct {
	UnknownModelBuckets           int64 `json:"unknown_model_buckets"`
	InferredModelBuckets          int64 `json:"inferred_model_buckets"`
	APIEstimateUnavailableBuckets int64 `json:"api_estimate_unavailable_buckets"`
}

// Coverage says how much of a member's usage in a window has a price.
// PricedBasisPoints is the priced share in hundredths of a percent, rounded
// down, and nil when the window holds no tokens.
type Coverage struct {
	PricedTokens      string `json:"priced_tokens"`
	UnpricedTokens    string `json:"unpriced_tokens"`
	PricedBasisPoints *int   `json:"priced_basis_points"`
}

// UnrankedReason says why a row has a value but no rank.
type UnrankedReason string

const (
	// UnrankedNoUsage means the member has no usage in the window.
	UnrankedNoUsage UnrankedReason = "no_usage"
	// UnrankedValueUnavailable means usage exists but none of it yields the
	// ranked value, such as an estimate for entirely unpriced models.
	UnrankedValueUnavailable UnrankedReason = "value_unavailable"
	// UnrankedCostCoverage means too little of the member's usage is priced
	// for the estimate to be ranked. The value is a lower bound.
	UnrankedCostCoverage UnrankedReason = "cost_coverage_below_threshold"
)

// Trend compares a row with the board's comparison window.
//
// RankChange is the previous rank minus the current one, so a positive number
// is a climb. It is nil when the member is unranked in either window. New
// marks a member with usage now and none in the comparison window.
type Trend struct {
	PreviousValue serveraccount.Value `json:"previous_value"`
	PreviousRank  *int                `json:"previous_rank"`
	RankChange    *int                `json:"rank_change"`
	New           bool                `json:"new"`
}

// Plausibility is an advisory note on self-reported usage. It never changes a
// value or a rank.
type Plausibility struct {
	// DailyTokensAboveLimit is true when the member's tokens on some local
	// day in the window exceed PlausibleDailyTokens.
	DailyTokensAboveLimit bool `json:"daily_tokens_above_limit"`
}

// Row is one member's leaderboard result.
type Row struct {
	Rank           *int                `json:"rank"`
	UnrankedReason UnrankedReason      `json:"unranked_reason,omitempty"`
	UserID         string              `json:"user_id"`
	DisplayName    string              `json:"display_name"`
	CurrentUser    bool                `json:"current_user"`
	Value          serveraccount.Value `json:"value"`
	Coverage       Coverage            `json:"cost_coverage"`
	Trend          Trend               `json:"trend"`
	Plausibility   Plausibility        `json:"plausibility"`
	LastSyncedAt   *time.Time          `json:"last_synced_at"`
	SourceMix      []SourceMix         `json:"source_mix"`
	MostUsedModel  *ModelUsage         `json:"most_used_model"`
	Limitations    Limitations         `json:"limitations"`

	// PreviousCoverage decides whether the comparison-window estimate was
	// rankable. It is internal to ranking and not part of the response.
	PreviousCoverage Coverage `json:"-"`
}

// Board is one leaderboard snapshot.
type Board struct {
	GroupID     string     `json:"group_id"`
	GroupName   string     `json:"group_name"`
	Timezone    string     `json:"timezone"`
	Period      Period     `json:"period"`
	Metric      Metric     `json:"metric"`
	Window      Window     `json:"window"`
	Comparison  Comparison `json:"comparison"`
	GeneratedAt time.Time  `json:"generated_at"`
	// CostRankMinPricedBasisPoints and PlausibleDailyTokens state the rules
	// behind unranked estimates and plausibility notes, so a client can
	// explain them without hard-coding server policy.
	CostRankMinPricedBasisPoints int    `json:"cost_rank_min_priced_basis_points"`
	PlausibleDailyTokens         string `json:"plausible_daily_tokens"`
	Rows                         []Row  `json:"rows"`
}

// Query is validated repository input.
type Query struct {
	GroupID     string
	Period      Period
	Metric      Metric
	GeneratedAt time.Time
}

// Authenticator resolves a tokenholic device credential.
type Authenticator interface {
	Authenticate(context.Context, string) (serverauth.Principal, error)
}

// Repository reads one authorized group ranking snapshot. Rows carry values
// and coverage for both windows; ranks and trends are assigned by the service.
type Repository interface {
	Leaderboard(context.Context, serverauth.Principal, Query) (Board, error)
}

// Service owns leaderboard input validation, authentication, ranking, and
// trends.
type Service struct {
	auth       Authenticator
	repository Repository
	now        func() time.Time
}

// NewService constructs the group leaderboard boundary.
func NewService(auth Authenticator, repository Repository) (*Service, error) {
	if auth == nil || repository == nil {
		return nil, errors.New("initialize leaderboards: authenticator and repository are required")
	}
	return &Service{auth: auth, repository: repository, now: time.Now}, nil
}

// Get authenticates one device and returns a ranked group snapshot.
func (s *Service) Get(ctx context.Context, credential, groupID string, period Period, metric Metric) (Board, error) {
	principal, err := s.auth.Authenticate(ctx, credential)
	if err != nil {
		return Board{}, err
	}
	if !validUUID(groupID) {
		return Board{}, servergroup.ErrNotFound
	}
	if !period.Valid() || !metric.Valid() {
		return Board{}, servergroup.ErrInvalidInput
	}
	board, err := s.repository.Leaderboard(ctx, principal, Query{
		GroupID: groupID, Period: period, Metric: metric, GeneratedAt: s.now().UTC(),
	})
	if err != nil {
		return Board{}, err
	}
	for index := range board.Rows {
		board.Rows[index].CurrentUser = board.Rows[index].UserID == principal.UserID
	}
	Rank(&board)
	return board, nil
}

// Valid reports whether a period is supported by the query contract.
func (p Period) Valid() bool {
	switch p {
	case PeriodToday, PeriodWeek, PeriodLast30Days, PeriodAllTime:
		return true
	default:
		return false
	}
}

// Valid reports whether a metric is supported by the query contract.
func (m Metric) Valid() bool {
	return m == MetricTotalTokens || m == MetricAPIEstimate
}

// WindowFor computes calendar boundaries in an IANA timezone. Today starts at
// local midnight, weeks start Monday, and 30 days means 30 local dates.
func WindowFor(period Period, generatedAt time.Time, location *time.Location) (Window, error) {
	if !period.Valid() || location == nil {
		return Window{}, servergroup.ErrInvalidInput
	}
	local := generatedAt.In(location)
	end := generatedAt.UTC()
	if period == PeriodAllTime {
		return Window{End: end}, nil
	}
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	switch period {
	case PeriodWeek:
		daysSinceMonday := (int(start.Weekday()) + 6) % 7
		start = start.AddDate(0, 0, -daysSinceMonday)
	case PeriodLast30Days:
		start = start.AddDate(0, 0, -29)
	}
	startUTC := start.UTC()
	return Window{Start: &startUTC, End: end}, nil
}

// ComparisonFor returns the window a period's trends are measured against.
//
// A calendar period is compared with the one before it, cut off after the
// same elapsed time, so a Tuesday-morning board is measured against last
// week's Monday through Tuesday morning rather than a whole week. The
// previous period starts on a local calendar boundary, which keeps it aligned
// across a daylight-saving change, and never runs into the current window.
//
// All-time has no previous period. It is compared with itself as it stood 24
// hours earlier.
func ComparisonFor(period Period, window Window, location *time.Location) (Comparison, error) {
	if !period.Valid() || location == nil {
		return Comparison{}, servergroup.ErrInvalidInput
	}
	if period == PeriodAllTime {
		return Comparison{Kind: ComparisonDayEarlier, Window: Window{End: window.End.Add(-24 * time.Hour)}}, nil
	}
	if window.Start == nil {
		return Comparison{}, servergroup.ErrInvalidInput
	}
	days := map[Period]int{PeriodToday: 1, PeriodWeek: 7, PeriodLast30Days: 30}[period]
	start := window.Start.In(location).AddDate(0, 0, -days).UTC()
	end := start.Add(window.End.Sub(*window.Start))
	if end.After(*window.Start) {
		end = *window.Start
	}
	return Comparison{Kind: ComparisonPreviousPeriod, Window: Window{Start: &start, End: end}}, nil
}

// NewCoverage builds a Coverage from decimal token sums.
func NewCoverage(priced, unpriced string) Coverage {
	coverage := Coverage{PricedTokens: priced, UnpricedTokens: unpriced}
	pricedValue, pricedOK := new(big.Int).SetString(priced, 10)
	unpricedValue, unpricedOK := new(big.Int).SetString(unpriced, 10)
	if !pricedOK || !unpricedOK {
		return coverage
	}
	total := new(big.Int).Add(pricedValue, unpricedValue)
	if total.Sign() <= 0 {
		return coverage
	}
	share := new(big.Int).Mul(pricedValue, big.NewInt(10_000))
	basisPoints := int(share.Quo(share, total).Int64())
	coverage.PricedBasisPoints = &basisPoints
	return coverage
}

// Rank orders a board's rows and assigns ranks and trends in place. The
// repository supplies values and coverage for the current and comparison
// windows; everything derived from comparing members is decided here.
//
// Tokens are ranked whenever a total is known. A partial total is a lower
// bound, because some sources never report every category, and it stays
// labelled as one. An estimate is ranked only when at least
// CostRankMinPricedBasisPoints of the member's tokens are priced. Equal
// values share a rank and the next rank skips, as in a competition.
func Rank(board *Board) {
	board.CostRankMinPricedBasisPoints = CostRankMinPricedBasisPoints
	board.PlausibleDailyTokens = new(big.Int).SetUint64(PlausibleDailyTokens).String()
	rows := board.Rows

	previous := make(map[string]*int, len(rows))
	assignRanks(rows, board.Metric,
		func(row *Row) (serveraccount.Value, Coverage) { return row.Trend.PreviousValue, row.PreviousCoverage },
		func(row *Row, rank *int, _ UnrankedReason) { previous[row.UserID] = rank })
	assignRanks(rows, board.Metric,
		func(row *Row) (serveraccount.Value, Coverage) { return row.Value, row.Coverage },
		func(row *Row, rank *int, reason UnrankedReason) { row.Rank, row.UnrankedReason = rank, reason })

	for index := range rows {
		row := &rows[index]
		row.Trend.PreviousRank = previous[row.UserID]
		row.Trend.RankChange = nil
		if row.Rank != nil && row.Trend.PreviousRank != nil {
			change := *row.Trend.PreviousRank - *row.Rank
			row.Trend.RankChange = &change
		}
		row.Trend.New = row.Value.State != serveraccount.AvailabilityEmpty &&
			row.Trend.PreviousValue.State == serveraccount.AvailabilityEmpty
	}
}

// assignRanks sorts rows by the selected window's value and reports each
// row's rank. Rows end in display order: ranked rows by rank, then unranked
// rows that still have a value, then rows without one.
func assignRanks(rows []Row, metric Metric, selectValue func(*Row) (serveraccount.Value, Coverage), assign func(*Row, *int, UnrankedReason)) {
	type entry struct {
		value  *big.Int
		reason UnrankedReason
	}
	entries := make(map[string]entry, len(rows))
	for index := range rows {
		value, coverage := selectValue(&rows[index])
		entries[rows[index].UserID] = entry{value: decimal(value), reason: unrankedReason(metric, value, coverage)}
	}
	sort.SliceStable(rows, func(left, right int) bool {
		a, b := entries[rows[left].UserID], entries[rows[right].UserID]
		if (a.reason == "") != (b.reason == "") {
			return a.reason == ""
		}
		if (a.value != nil) != (b.value != nil) {
			return a.value != nil
		}
		if a.value != nil {
			if comparison := a.value.Cmp(b.value); comparison != 0 {
				return comparison > 0
			}
		}
		if rows[left].DisplayName != rows[right].DisplayName {
			return rows[left].DisplayName < rows[right].DisplayName
		}
		return rows[left].UserID < rows[right].UserID
	})
	var previousValue *big.Int
	previousRank := 0
	for index := range rows {
		current := entries[rows[index].UserID]
		if current.reason != "" {
			assign(&rows[index], nil, current.reason)
			continue
		}
		rank := index + 1
		if previousValue != nil && current.value.Cmp(previousValue) == 0 {
			rank = previousRank
		}
		previousValue, previousRank = current.value, rank
		assign(&rows[index], &rank, "")
	}
}

// unrankedReason returns why a value cannot be ranked, or "" when it can.
func unrankedReason(metric Metric, value serveraccount.Value, coverage Coverage) UnrankedReason {
	switch {
	case value.State == serveraccount.AvailabilityEmpty:
		return UnrankedNoUsage
	case decimal(value) == nil:
		return UnrankedValueUnavailable
	case metric == MetricAPIEstimate && (coverage.PricedBasisPoints == nil || *coverage.PricedBasisPoints < CostRankMinPricedBasisPoints):
		return UnrankedCostCoverage
	default:
		return ""
	}
}

func decimal(value serveraccount.Value) *big.Int {
	if value.Value == nil {
		return nil
	}
	parsed, ok := new(big.Int).SetString(*value.Value, 10)
	if !ok {
		return nil
	}
	return parsed
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}
