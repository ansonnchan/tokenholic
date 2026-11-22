// Package demo supplies invented usage for `tokenholic demo`: a local
// dashboard and a few boards that look like a busy month, built in memory.
// Nothing is read from the machine, stored, or sent, and no name or number
// in it belongs to anyone.
//
// It exists so the interface can be shown, recorded, and explored without
// exposing real usage. It implements the interfaces the TUI consumes and
// never touches collection, storage, or the network.
package demo

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/ansonnchan/tokenholic/internal/application"
	"github.com/ansonnchan/tokenholic/internal/modelcatalog"
	"github.com/ansonnchan/tokenholic/internal/multiplayer"
	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/store"
)

// Notice is the label a demo carries on screen, so a picture or recording of
// it cannot be mistaken for anyone's real usage.
const Notice = "DEMO: invented data"

// App is the local half of the demo: the dashboard, with nothing to scan.
type App struct {
	// Now is the moment the demo pretends to be. Nil means the real clock.
	Now func() time.Time
	// Location is the timezone shown. Nil means the machine's own.
	Location *time.Location
}

// Load returns the invented dashboard.
func (a App) Load(context.Context) (application.Dashboard, error) {
	now, location := time.Now, a.Location
	if a.Now != nil {
		now = a.Now
	}
	if location == nil {
		location = time.Local
	}
	return Dashboard(now().In(location)), nil
}

// Refresh does nothing: a demo has no records to scan.
func (a App) Refresh(context.Context) error { return nil }

// usage is one model's invented 30-day usage, in tokens. A negative count
// means the source does not record that category.
type usage struct {
	source    normalize.Source
	surface   normalize.Surface
	observed  string
	canonical string
	evidence  normalize.ModelResolution
	input     int64
	read      int64
	write5m   int64
	write1h   int64
	output    int64
	reasoning int64
	// microUSD is the estimate for the priced share; pricedPercent is that
	// share of the model's tokens. reason says why the rest has no price.
	microUSD      int64
	pricedPercent int
	reason        string
}

var month = []usage{
	{source: normalize.SourceClaude, surface: normalize.SurfaceCLI, observed: "claude-opus-5-5", canonical: "claude-opus-5-5", evidence: normalize.ModelReported,
		input: 21_400_000, read: 1_790_000_000, write5m: 152_000_000, write1h: 41_300_000, output: 14_600_000, reasoning: -1, microUSD: 661_200_000, pricedPercent: 100},
	{source: normalize.SourceCodex, surface: normalize.SurfaceSharedLocal, observed: "gpt-5.2-codex", canonical: "gpt-5.2-codex", evidence: normalize.ModelInferred,
		input: 68_000_000, read: 871_000_000, write5m: -1, write1h: -1, output: 42_500_000, reasoning: 16_900_000, microUSD: 304_800_000, pricedPercent: 96, reason: modelcatalog.ReasonPromptLengthUnproven},
	{source: normalize.SourceClaude, surface: normalize.SurfaceDesktopCode, observed: "claude-sonnet-5-5", canonical: "claude-sonnet-5-5", evidence: normalize.ModelReported,
		input: 7_900_000, read: 632_000_000, write5m: 36_500_000, write1h: 0, output: 5_700_000, reasoning: -1, microUSD: 117_400_000, pricedPercent: 100},
	{source: normalize.SourceGemini, surface: normalize.SurfaceCLI, observed: "gemini-2.5-flash", canonical: "gemini-2.5-flash", evidence: normalize.ModelReported,
		input: 58_000_000, read: 141_000_000, write5m: -1, write1h: -1, output: 9_100_000, reasoning: -1, microUSD: 44_380_000, pricedPercent: 100},
	{source: normalize.SourceOpenCode, surface: normalize.SurfaceCLI, observed: "claude-haiku-5-5", canonical: "claude-haiku-5-5", evidence: normalize.ModelReported,
		input: 4_100_000, read: 77_000_000, write5m: 6_200_000, write1h: -1, output: 1_300_000, reasoning: -1, microUSD: 19_650_000, pricedPercent: 100},
	{source: normalize.SourceGemini, surface: normalize.SurfaceCLI, observed: "gemini-2.5-pro", canonical: "gemini-2.5-pro", evidence: normalize.ModelReported,
		input: 6_300_000, read: 22_000_000, write5m: -1, write1h: -1, output: 1_900_000, reasoning: -1, pricedPercent: 0, reason: modelcatalog.ReasonPromptLengthUnproven},
	{source: normalize.SourceCodex, surface: normalize.SurfaceSharedLocal, observed: "codex-auto-review", evidence: normalize.ModelUnknown,
		input: 4_600_000, read: 52_000_000, write5m: -1, write1h: -1, output: 2_400_000, reasoning: 800_000, pricedPercent: 0, reason: modelcatalog.ReasonRoutingLabel},
}

// periods lists each dashboard period with its share of the month, per
// mille, and the board period that shows the same span.
var periods = []struct {
	period   store.Period
	board    multiplayer.Period
	perMille int64
}{
	{store.PeriodToday, multiplayer.PeriodToday, 34},
	{store.PeriodWeek, multiplayer.PeriodWeek, 236},
	{store.PeriodLast30Days, multiplayer.PeriodLast30Days, 1000},
	{store.PeriodAllTime, multiplayer.PeriodAllTime, 3280},
}

// viewerSummary is the viewer's own dashboard summary for a board period, so
// their row on a board carries the numbers their home view shows.
func viewerSummary(period multiplayer.Period) store.UsageSummary {
	for _, entry := range periods {
		if entry.board == period {
			return summarize(entry.period, entry.perMille)
		}
	}
	return summarize(store.PeriodWeek, 236)
}

// Dashboard builds the invented local dashboard as of now.
func Dashboard(now time.Time) application.Dashboard {
	scanned := now.Add(-2 * time.Minute)
	dashboard := application.Dashboard{GeneratedAt: now, Timezone: now.Location().String()}
	for _, entry := range periods {
		summary := summarize(entry.period, entry.perMille)
		switch entry.period {
		case store.PeriodToday:
			dashboard.Today = summary
		case store.PeriodWeek:
			dashboard.Week = summary
		case store.PeriodLast30Days:
			dashboard.Last30Days = summary
		default:
			dashboard.AllTime = summary
		}
	}
	dashboard.Daily = daily(now, *dashboard.Last30Days.Tokens.Total().Value)
	count := func(value uint64) *uint64 { return &value }
	health := func(source normalize.Source, surface normalize.Surface, status normalize.HealthStatus, records *uint64) store.StoredAdapterHealth {
		record := store.StoredAdapterHealth{Health: normalize.SourceHealth{Source: source, Surface: surface, Status: status, RecordCount: records}}
		if records != nil {
			record.LastSuccessAt = &scanned
		}
		return record
	}
	dashboard.Diagnostics = store.DiagnosticSnapshot{
		IntegrityOK: true, SchemaVersion: store.SupportedSchemaVersion(), SupportedSchemaVersion: store.SupportedSchemaVersion(),
		CheckpointCount: 5, LastSuccessfulCollection: &scanned,
		AdapterHealth: []store.StoredAdapterHealth{
			health(normalize.SourceClaude, normalize.SurfaceCLI, normalize.HealthDetected, count(12_480)),
			health(normalize.SourceClaude, normalize.SurfaceDesktopCode, normalize.HealthDetected, count(318)),
			health(normalize.SourceCodex, normalize.SurfaceSharedLocal, normalize.HealthDetected, count(3_902)),
			health(normalize.SourceCopilot, normalize.SurfaceCLI, normalize.HealthNotFound, nil),
			health(normalize.SourceGemini, normalize.SurfaceCLI, normalize.HealthDetected, count(27)),
			health(normalize.SourceGrok, normalize.SurfaceCLI, normalize.HealthNotFound, nil),
			health(normalize.SourceOpenCode, normalize.SurfaceCLI, normalize.HealthDetected, count(11)),
			health(normalize.SourceQwen, normalize.SurfaceCLI, normalize.HealthNotFound, nil),
		},
	}
	return dashboard
}

// tally sums one token category across records, keeping the difference
// between a known count and one a source does not record.
type tally struct {
	sum            uint64
	known, missing bool
}

func (t *tally) add(count, perMille int64) {
	if count < 0 {
		t.missing = true
		return
	}
	t.known = true
	t.sum += uint64(count * perMille / 1000)
}

func (t tally) value() store.AggregateValue {
	switch {
	case !t.known:
		return store.AggregateValue{State: store.AvailabilityUnavailable}
	case t.missing:
		return store.AggregateValue{Value: &t.sum, State: store.AvailabilityPartial}
	default:
		return store.AggregateValue{Value: &t.sum, State: store.AvailabilityComplete}
	}
}

// totals accumulates a breakdown, its estimate, and its priced share.
type totals struct {
	input, read, write, write5m, write1h, output, reasoning tally
	estimate                                                tally
	priced, unpriced                                        uint64
}

func (t *totals) add(record usage, perMille int64) {
	t.input.add(record.input, perMille)
	t.read.add(record.read, perMille)
	t.write5m.add(record.write5m, perMille)
	t.write1h.add(record.write1h, perMille)
	t.output.add(record.output, perMille)
	t.reasoning.add(record.reasoning, perMille)
	write := int64(-1)
	if record.write5m >= 0 || record.write1h >= 0 {
		write = max(record.write5m, 0) + max(record.write1h, 0)
	}
	t.write.add(write, perMille)
	var tokens uint64
	for _, count := range []int64{record.input, record.read, write, record.output} {
		if count > 0 {
			tokens += uint64(count * perMille / 1000)
		}
	}
	priced := tokens * uint64(record.pricedPercent) / 100
	t.priced, t.unpriced = t.priced+priced, t.unpriced+tokens-priced
	if record.pricedPercent == 0 {
		t.estimate.missing = true
		return
	}
	t.estimate.add(record.microUSD, perMille)
	if record.pricedPercent < 100 {
		t.estimate.missing = true
	}
}

func (t totals) tokens() store.TokenBreakdown {
	return store.TokenBreakdown{
		InputUncached: t.input.value(), InputCacheRead: t.read.value(), InputCacheWrite: t.write.value(),
		InputCacheWrite5m: t.write5m.value(), InputCacheWrite1h: t.write1h.value(),
		Output: t.output.value(), ReasoningOutput: t.reasoning.value(),
	}
}

func (t totals) coverage() store.Coverage {
	coverage := store.Coverage{PricedTokens: t.priced, UnpricedTokens: t.unpriced}
	if total := t.priced + t.unpriced; total > 0 {
		share := int(t.priced * 10_000 / total)
		coverage.PricedBasisPoints = &share
	}
	return coverage
}

func summarize(period store.Period, perMille int64) store.UsageSummary {
	var all totals
	sources, surfaces := map[string]*totals{}, map[string]*totals{}
	var sourceOrder, surfaceOrder []string
	summary := store.UsageSummary{Period: period}
	for _, record := range month {
		all.add(record, perMille)
		for _, group := range []struct {
			name   string
			groups map[string]*totals
			order  *[]string
		}{{string(record.source), sources, &sourceOrder}, {string(record.surface), surfaces, &surfaceOrder}} {
			if group.groups[group.name] == nil {
				group.groups[group.name] = &totals{}
				*group.order = append(*group.order, group.name)
			}
			group.groups[group.name].add(record, perMille)
		}
		var one totals
		one.add(record, perMille)
		model := store.ModelBreakdown{
			ObservedModelID: record.observed, Resolution: record.evidence, UnpricedReason: record.reason,
			Tokens: one.tokens(), APIEstimate: one.estimate.value(), Coverage: one.coverage(),
		}
		if record.canonical != "" {
			canonical := record.canonical
			model.CanonicalModelID = &canonical
		}
		summary.Models = append(summary.Models, model)
	}
	summary.Tokens, summary.APIEstimate, summary.Coverage = all.tokens(), all.estimate.value(), all.coverage()
	named := func(order []string, groups map[string]*totals) []store.NamedBreakdown {
		breakdowns := make([]store.NamedBreakdown, 0, len(order))
		for _, name := range order {
			group := groups[name]
			breakdowns = append(breakdowns, store.NamedBreakdown{
				Name: name, Tokens: group.tokens(), APIEstimate: group.estimate.value(), Coverage: group.coverage(),
			})
		}
		// Largest first, as the store returns them.
		sort.SliceStable(breakdowns, func(i, j int) bool {
			return *breakdowns[i].Tokens.Total().Value > *breakdowns[j].Tokens.Total().Value
		})
		return breakdowns
	}
	summary.Sources, summary.Surfaces = named(sourceOrder, sources), named(surfaceOrder, surfaces)
	return summary
}

// daily spreads the month's tokens over the last thirty local dates in a
// repeatable shape: busy weekdays, quiet weekends, and one day off.
func daily(now time.Time, monthTokens uint64) []store.DailyUsage {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	days := make([]store.DailyUsage, application.DailyDays)
	weights := make([]uint64, len(days))
	var sum uint64
	for index := range days {
		days[index].Day = today.AddDate(0, 0, index-len(days)+1)
		weight := uint64((index*37+11)%23 + 6)
		if weekday := days[index].Day.Weekday(); weekday == time.Saturday || weekday == time.Sunday {
			weight /= 5
		}
		if index == 9 {
			weight = 0
		}
		weights[index], sum = weight, sum+weight
	}
	for index := range days {
		if weights[index] == 0 {
			days[index].Tokens = store.AggregateValue{State: store.AvailabilityEmpty}
			continue
		}
		tokens := monthTokens / sum * weights[index]
		days[index].Tokens = store.AggregateValue{Value: &tokens, State: store.AvailabilityPartial}
	}
	return days
}

// member is one invented account on the demo boards.
type member struct {
	login string
	// week is this week's estimate in micro-USD; the other periods and the
	// token metric are derived from it, with enough variation that ranks
	// move when the period or metric changes. The viewer has none: their
	// values come from the dashboard.
	week uint64
	// lean makes a member relatively heavier on tokens than on cost.
	lean          uint64
	pricedPercent int
	model         string
	resolution    string
	idle          bool
	viewer        bool
}

var crew = []member{
	{login: "dana", week: 1_184_000_000, lean: 96, pricedPercent: 100, model: "claude-opus-5-5", resolution: "reported"},
	{login: "kai", week: 1_092_500_000, lean: 131, pricedPercent: 100, model: "gpt-5.2-codex", resolution: "inferred"},
	{login: "rio", week: 842_250_000, lean: 88, pricedPercent: 100, model: "claude-opus-5-5", resolution: "reported"},
	{login: "lee", week: 713_400_000, lean: 152, pricedPercent: 100, model: "claude-sonnet-5-5", resolution: "reported"},
	{login: "you", model: "claude-opus-5-5", resolution: "reported", viewer: true},
	{login: "sam", week: 561_750_000, lean: 174, pricedPercent: 71, model: "gpt-5.2-codex", resolution: "inferred"},
	{login: "mika", week: 204_100_000, lean: 109, pricedPercent: 100, model: "claude-haiku-5-5", resolution: "reported"},
	{login: "zed", idle: true},
}

var strangers = []member{
	{login: "nova", week: 2_310_000_000, lean: 102, pricedPercent: 100, model: "claude-opus-5-5", resolution: "reported"},
	{login: "juno", week: 1_874_000_000, lean: 140, pricedPercent: 100, model: "gpt-5.2-codex", resolution: "inferred"},
	{login: "dana", week: 1_184_000_000, lean: 96, pricedPercent: 100, model: "claude-opus-5-5", resolution: "reported"},
	{login: "arlo", week: 968_000_000, lean: 121, pricedPercent: 100, model: "claude-sonnet-5-5", resolution: "reported"},
	{login: "you", model: "claude-opus-5-5", resolution: "reported", viewer: true},
	{login: "wren", week: 217_000_000, lean: 93, pricedPercent: 100, model: "gemini-2.5-flash", resolution: "reported"},
}

// Boards is the multiplayer half of the demo: two groups and the global
// board, held in memory. It answers for whichever period and metric is asked
// for, and lets the viewer join and leave the global board. Creating,
// joining, and inviting return multiplayer.ErrReadOnly, since nothing here
// is real.
type Boards struct {
	// Now is the moment the demo pretends to be. Nil means the real clock.
	Now func() time.Time

	mu     sync.Mutex
	listed bool
}

var groups = []multiplayer.Group{
	{ID: "demo-night-shift", Name: "night-shift", Timezone: "America/Vancouver", Role: "owner", Visibility: "private"},
	{ID: "demo-weekenders", Name: "weekenders", Timezone: "UTC", Role: "member", Visibility: "public", PublicSlug: "weekenders"},
}

// Load returns the scope list and the board for one scope, period, and metric.
func (b *Boards) Load(_ context.Context, scope string, period multiplayer.Period, metric multiplayer.Metric) (multiplayer.Snapshot, error) {
	b.mu.Lock()
	listed := b.listed
	b.mu.Unlock()
	now := time.Now().UTC()
	if b.Now != nil {
		now = b.Now().UTC()
	}
	snapshot := multiplayer.Snapshot{Groups: groups}
	board := multiplayer.Board{
		Period: period, Metric: metric, GeneratedAt: now, CostRankMinPricedBasisPoints: 9500,
		Comparison: multiplayer.Comparison{Kind: multiplayer.ComparisonPreviousPeriod},
	}
	if period == multiplayer.PeriodAllTime {
		board.Comparison.Kind = multiplayer.ComparisonDayEarlier
	}
	members, salt := crew, uint64(0)
	switch scope {
	case multiplayer.GlobalScope:
		snapshot.Global = true
		board.Scope, board.Timezone, board.MinRankedMembers = multiplayer.ScopeGlobal, "UTC", 3
		members = nil
		for _, stranger := range strangers {
			if !stranger.viewer || listed {
				members = append(members, stranger)
			}
		}
	default:
		snapshot.Selected = groups[0]
		for index, group := range groups {
			if group.ID == scope {
				snapshot.Selected = group
				// The second group is the same people on a different week.
				salt = uint64(index) * 7
			}
		}
		board.Scope, board.Timezone = multiplayer.ScopeGroup, snapshot.Selected.Timezone
		board.GroupID, board.GroupName = snapshot.Selected.ID, snapshot.Selected.Name
	}
	board.Rows, board.TotalMembers = rows(members, period, metric, salt, now), len(members)
	snapshot.Board = &board
	return snapshot, nil
}

// Create refuses: a demo has no account to own a group.
func (b *Boards) Create(context.Context, string) (multiplayer.Group, error) {
	return multiplayer.Group{}, multiplayer.ErrReadOnly
}

// Join refuses for the same reason.
func (b *Boards) Join(context.Context, string) (multiplayer.Group, error) {
	return multiplayer.Group{}, multiplayer.ErrReadOnly
}

// Invite refuses: an invented group has no invitation to give.
func (b *Boards) Invite(context.Context, string, time.Duration, int) (multiplayer.Invite, error) {
	return multiplayer.Invite{}, multiplayer.ErrReadOnly
}

// SetGlobalParticipation lists or unlists the viewer on the demo's global
// board, in memory only.
func (b *Boards) SetGlobalParticipation(_ context.Context, participating bool) error {
	b.mu.Lock()
	b.listed = participating
	b.mu.Unlock()
	return nil
}

// scale is each board period as a share of a week, per mille, and how far a
// member's standing drifts in that period.
func scale(period multiplayer.Period) (perMille, drift uint64) {
	switch period {
	case multiplayer.PeriodToday:
		return 150, 3
	case multiplayer.PeriodLast30Days:
		return 4_250, 5
	case multiplayer.PeriodAllTime:
		return 13_900, 11
	default:
		return 1000, 0
	}
}

func decimal(value uint64) multiplayer.Value {
	text := strconv.FormatUint(value, 10)
	return multiplayer.Value{Value: &text, State: multiplayer.AvailabilityComplete}
}

// rows ranks the members for one period and metric and compares each with
// the same members one period earlier.
func rows(members []member, period multiplayer.Period, metric multiplayer.Metric, salt uint64, now time.Time) []multiplayer.Row {
	perMille, drift := scale(period)
	own := viewerSummary(period)
	type standing struct {
		member            member
		current, previous uint64
	}
	amount := func(index int, who member, previous bool) uint64 {
		if who.viewer {
			// The viewer's row is their own dashboard, a little up on the
			// period before.
			value := *own.APIEstimate.Value
			if metric == multiplayer.MetricTotalTokens {
				value = *own.Tokens.Total().Value
			}
			if previous {
				value = value / 100 * 86
			}
			return value
		}
		// A small, repeatable wobble per member and period, so the order
		// differs between periods and against the earlier one.
		wobble := 100 + (uint64(index)*13+drift*7+salt)%17
		if previous {
			wobble = 84 + (uint64(index)*31+drift*5+salt+3)%37
		}
		value := who.week / 100 * wobble / 1000 * perMille
		if metric == multiplayer.MetricTotalTokens {
			// Roughly 3.4 million tokens per estimated dollar, more for a
			// member who leans on cached reads.
			value = value / 1000 * 34 * who.lean
		}
		return value
	}
	standings := make([]standing, 0, len(members))
	for index, who := range members {
		entry := standing{member: who}
		if !who.idle {
			entry.current, entry.previous = amount(index, who, false), amount(index, who, true)
		}
		standings = append(standings, entry)
	}
	ranked := func(entry standing) bool {
		who := entry.member
		priced := who.pricedPercent >= 95 || who.viewer && *own.Coverage.PricedBasisPoints >= 9500
		return !who.idle && (metric == multiplayer.MetricTotalTokens || priced)
	}
	order := func(value func(standing) uint64) map[string]int {
		sorted := append([]standing(nil), standings...)
		sort.SliceStable(sorted, func(i, j int) bool { return value(sorted[i]) > value(sorted[j]) })
		ranks, rank := map[string]int{}, 0
		for _, entry := range sorted {
			if ranked(entry) {
				rank++
				ranks[entry.member.login] = rank
			}
		}
		return ranks
	}
	currentRanks := order(func(entry standing) uint64 { return entry.current })
	previousRanks := order(func(entry standing) uint64 { return entry.previous })
	sort.SliceStable(standings, func(i, j int) bool {
		left, right := standings[i], standings[j]
		if ranked(left) != ranked(right) {
			return ranked(left)
		}
		return left.current > right.current
	})
	result := make([]multiplayer.Row, 0, len(standings))
	for index, entry := range standings {
		who := entry.member
		row := multiplayer.Row{DisplayName: who.login, CurrentUser: who.viewer}
		if who.viewer {
			row.UserID = "demo-viewer"
		}
		if who.idle {
			row.Value, row.UnrankedReason = multiplayer.Value{State: multiplayer.AvailabilityEmpty}, multiplayer.UnrankedNoUsage
			result = append(result, row)
			continue
		}
		synced := now.Add(-time.Duration(11+index*23) * time.Minute)
		share := who.pricedPercent * 100
		row.Value, row.LastSyncedAt = decimal(entry.current), &synced
		row.Trend.PreviousValue = decimal(entry.previous)
		row.MostUsedModel = &multiplayer.ModelUsage{ModelID: who.model, Resolution: who.resolution}
		if who.viewer {
			share = *own.Coverage.PricedBasisPoints
			for _, source := range own.Sources {
				row.SourceMix = append(row.SourceMix, multiplayer.SourceMix{Source: source.Name, TotalTokens: decimal(*source.Tokens.Total().Value)})
			}
			// Gemini CLI and OpenCode are read one day at a time.
			row.Limitations.DayLevelBuckets = int64(perMille/150) + 1
		} else {
			tokens := entry.current
			if metric != multiplayer.MetricTotalTokens {
				tokens = entry.current / 1000 * 34 * who.lean
			}
			row.SourceMix = []multiplayer.SourceMix{
				{Source: "claude", TotalTokens: decimal(tokens / 100 * 68)},
				{Source: "codex", TotalTokens: decimal(tokens / 100 * 27)},
			}
		}
		row.Coverage = multiplayer.Coverage{PricedBasisPoints: &share}
		if who.resolution == "inferred" {
			row.Limitations.InferredModelBuckets = int64(perMille / 9)
		}
		if rank, found := currentRanks[who.login]; found {
			row.Rank = &rank
			if previous, was := previousRanks[who.login]; was {
				change := previous - rank
				row.Trend.PreviousRank, row.Trend.RankChange = &previous, &change
			}
		} else {
			// Too little of this member's usage is priced to rank the estimate.
			row.Value.State, row.UnrankedReason = multiplayer.AvailabilityPartial, multiplayer.UnrankedCostCoverage
			row.Limitations.APIEstimateUnavailableBuckets = int64(perMille / 16)
		}
		result = append(result, row)
	}
	return result
}
