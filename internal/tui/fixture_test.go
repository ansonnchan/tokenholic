package tui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/account"
	"github.com/ansonnchan/tokenholic/internal/application"
	"github.com/ansonnchan/tokenholic/internal/modelcatalog"
	"github.com/ansonnchan/tokenholic/internal/multiplayer"
	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/store"
	"github.com/ansonnchan/tokenholic/internal/tui/theme"
	tea "github.com/charmbracelet/bubbletea"
)

// Everything in this file is synthetic: invented names and numbers shaped
// like real usage, so snapshots can be committed and shown.

type fakeApplication struct {
	dashboard  application.Dashboard
	loadError  error
	refreshErr error
	loads      int
	refreshes  int
	// scanned, when set, is what the machine holds once it has been
	// scanned: the dashboard a successful refresh switches to.
	scanned *application.Dashboard
}

func (a *fakeApplication) Load(context.Context) (application.Dashboard, error) {
	a.loads++
	return a.dashboard, a.loadError
}

func (a *fakeApplication) Refresh(context.Context) error {
	a.refreshes++
	if a.refreshErr == nil && a.scanned != nil {
		a.dashboard = *a.scanned
	}
	return a.refreshErr
}

// firstLaunch is a machine that has never been scanned and will find the
// given usage when it is.
func firstLaunch(found application.Dashboard) *fakeApplication {
	never := application.Dashboard{GeneratedAt: found.GeneratedAt, Timezone: found.Timezone}
	never.Diagnostics.IntegrityOK = true
	return &fakeApplication{dashboard: never, scanned: &found}
}

// errHeld stands for a step that has not finished: the user is still at
// GitHub, or the upload is still running. drain drops its result, leaving
// the model waiting as it would be.
var errHeld = errors.New("synthetic: not finished yet")

// fakeAccount is a device that can be claimed.
type fakeAccount struct {
	login       string
	loginErr    error
	beginErr    error
	completeErr error
	syncErr     error
	uploaded    int64
	begun       int
	completed   int
	synced      int
	cancelled   bool
}

func (f *fakeAccount) Login(context.Context) (string, error) { return f.login, f.loginErr }

func (f *fakeAccount) Begin(context.Context) (account.DeviceStart, error) {
	f.begun++
	if f.beginErr != nil {
		return account.DeviceStart{}, f.beginErr
	}
	return account.DeviceStart{
		FlowToken: "synthetic-flow-token", UserCode: "WDJB-MJHT", VerificationURI: "https://github.com/login/device",
		ExpiresIn: 15 * time.Minute, Interval: 5 * time.Second,
	}, nil
}

func (f *fakeAccount) Complete(ctx context.Context, _ account.DeviceStart) (string, error) {
	f.completed++
	if ctx.Err() != nil {
		f.cancelled = true
		return "", ctx.Err()
	}
	if f.completeErr != nil {
		return "", f.completeErr
	}
	f.login = "you"
	return f.login, nil
}

func (f *fakeAccount) Sync(context.Context) (int64, error) {
	f.synced++
	return f.uploaded, f.syncErr
}

// fakeAutosync stands in for the operating system's scheduler.
type fakeAutosync struct {
	unavailable bool
	enableErr   error
	asked       int
	enabled     int
}

func (f *fakeAutosync) Available(context.Context) bool {
	f.asked++
	return !f.unavailable
}

func (f *fakeAutosync) Enable(context.Context) error {
	f.enabled++
	return f.enableErr
}

// fakeMultiplayer serves synthetic boards by scope and records what the
// board area asked it to do.
type fakeMultiplayer struct {
	groups        []multiplayer.Group
	boards        map[string]*multiplayer.Board
	loadError     error
	actionError   error
	scope         string
	period        multiplayer.Period
	metric        multiplayer.Metric
	loads         int
	created       string
	joined        string
	invited       []string
	participating *bool
}

func (f *fakeMultiplayer) Load(_ context.Context, scope string, period multiplayer.Period, metric multiplayer.Metric) (multiplayer.Snapshot, error) {
	f.scope, f.period, f.metric = scope, period, metric
	f.loads++
	if f.loadError != nil {
		return multiplayer.Snapshot{}, f.loadError
	}
	snapshot := multiplayer.Snapshot{Groups: f.groups}
	key := scope
	switch {
	case scope == multiplayer.GlobalScope:
		snapshot.Global = true
	case len(f.groups) == 0:
		return snapshot, nil
	default:
		snapshot.Selected = f.groups[0]
		for _, group := range f.groups {
			if group.ID == scope {
				snapshot.Selected = group
			}
		}
		key = snapshot.Selected.ID
	}
	board := multiplayer.Board{Scope: multiplayer.ScopeGroup, Timezone: "UTC", GeneratedAt: boardTime, Rows: []multiplayer.Row{}}
	if stored := f.boards[key]; stored != nil {
		board = *stored
	}
	if snapshot.Global {
		board.Scope, board.Timezone = multiplayer.ScopeGlobal, "UTC"
	} else {
		board.Scope, board.MinRankedMembers = multiplayer.ScopeGroup, 0
		board.GroupID, board.GroupName = snapshot.Selected.ID, snapshot.Selected.Name
	}
	// A real server answers for the period and metric it was asked for.
	board.Period, board.Metric = period, metric
	board.Comparison.Kind = multiplayer.ComparisonPreviousPeriod
	if period == multiplayer.PeriodAllTime {
		board.Comparison.Kind = multiplayer.ComparisonDayEarlier
	}
	if metric == multiplayer.MetricTotalTokens {
		board.Rows = tokenRows(board.Rows)
	}
	snapshot.Board = &board
	return snapshot, nil
}

func (f *fakeMultiplayer) Create(_ context.Context, name string) (multiplayer.Group, error) {
	if f.actionError != nil {
		return multiplayer.Group{}, f.actionError
	}
	f.created = name
	group := multiplayer.Group{ID: "group-created", Name: name, Timezone: "UTC", Role: "owner", Visibility: "private"}
	f.groups = append(f.groups, group)
	return group, nil
}

func (f *fakeMultiplayer) Join(_ context.Context, code string) (multiplayer.Group, error) {
	if f.actionError != nil {
		return multiplayer.Group{}, f.actionError
	}
	f.joined = code
	group := multiplayer.Group{ID: "group-joined", Name: "Joined", Timezone: "UTC", Role: "member", Visibility: "private"}
	f.groups = append(f.groups, group)
	return group, nil
}

func (f *fakeMultiplayer) Invite(_ context.Context, group string, lifetime time.Duration, maxUses int) (multiplayer.Invite, error) {
	if f.actionError != nil {
		return multiplayer.Invite{}, f.actionError
	}
	f.invited = append(f.invited, group)
	return multiplayer.Invite{Code: "ovt_Q7M2KXsynthetic", ExpiresAt: boardTime.Add(lifetime), MaxUses: maxUses}, nil
}

func (f *fakeMultiplayer) SetGlobalParticipation(_ context.Context, participating bool) error {
	if f.actionError != nil {
		return f.actionError
	}
	f.participating = &participating
	board := f.boards[multiplayer.GlobalScope]
	if board == nil {
		return nil
	}
	// Joining adds the viewer's row and leaving removes it, as on the server.
	rows := make([]multiplayer.Row, 0, len(board.Rows)+1)
	for _, row := range board.Rows {
		if !row.CurrentUser {
			rows = append(rows, row)
		}
	}
	if participating {
		rows = append(rows, member("you", 640_000_000, 0, true))
	}
	updated := *board
	updated.Rows = rows
	f.boards[multiplayer.GlobalScope] = &updated
	return nil
}

var boardTime = time.Date(2026, 11, 21, 18, 4, 0, 0, time.UTC)

func intValue(value int) *int { return &value }

func decimal(value uint64) multiplayer.Value {
	text := strconv.FormatUint(value, 10)
	return multiplayer.Value{Value: &text, State: multiplayer.AvailabilityComplete}
}

// member is a ranked row with a full estimate, in micro-USD.
func member(login string, microUSD uint64, rank int, viewer bool) multiplayer.Row {
	synced := boardTime.Add(-time.Duration(len(login)) * 17 * time.Minute)
	row := multiplayer.Row{
		DisplayName: login, CurrentUser: viewer, Value: decimal(microUSD),
		Coverage:     multiplayer.Coverage{PricedBasisPoints: intValue(10_000)},
		LastSyncedAt: &synced,
		SourceMix: []multiplayer.SourceMix{
			{Source: "claude", TotalTokens: decimal(microUSD * 3)}, {Source: "codex", TotalTokens: decimal(microUSD / 4)},
		},
		MostUsedModel: &multiplayer.ModelUsage{ModelID: "claude-opus-5-5", Resolution: "resolved"},
	}
	if rank > 0 {
		row.Rank = &rank
	}
	if viewer {
		row.UserID = "viewer"
	}
	return row
}

// tokenRows turns estimate rows into the same board ranked by tokens: every
// member with a known total is ranked, including lower bounds.
func tokenRows(rows []multiplayer.Row) []multiplayer.Row {
	converted := make([]multiplayer.Row, len(rows))
	rank := 0
	for index, row := range rows {
		converted[index] = row
		if row.UnrankedReason == multiplayer.UnrankedNoUsage || row.UnrankedReason == multiplayer.UnrankedScopeTooSmall {
			continue
		}
		rank++
		tokens := decimal(uint64(4_300_000_000 / rank))
		if row.Rank == nil {
			tokens.State = multiplayer.AvailabilityPartial
		}
		converted[index].Value, converted[index].Rank, converted[index].UnrankedReason = tokens, intValue(rank), ""
		previous := decimal(uint64(3_900_000_000 / rank))
		converted[index].Trend.PreviousValue = previous
	}
	return converted
}

// nightShift is a group board with every row state the design names.
func nightShift() *multiplayer.Board {
	dana, kai, rio, lee, you := member("dana", 1_180_000_000, 1, false), member("kai", 1_090_000_000, 2, false),
		member("rio", 840_500_000, 3, false), member("lee", 712_250_000, 4, false), member("you", 640_000_000, 5, true)
	dana.Trend = multiplayer.Trend{PreviousValue: decimal(1_010_300_000), PreviousRank: intValue(2), RankChange: intValue(1)}
	kai.Trend = multiplayer.Trend{PreviousValue: decimal(1_204_750_000), PreviousRank: intValue(1), RankChange: intValue(-1)}
	rio.Trend = multiplayer.Trend{PreviousValue: decimal(802_100_000), PreviousRank: intValue(3), RankChange: intValue(0)}
	lee.Trend = multiplayer.Trend{PreviousValue: multiplayer.Value{State: multiplayer.AvailabilityEmpty}, New: true}
	you.Trend = multiplayer.Trend{PreviousValue: decimal(512_100_000), PreviousRank: intValue(7), RankChange: intValue(2)}

	// Too little of this member's usage is priced for the estimate to rank.
	sam := member("sam", 560_750_000, 0, false)
	sam.Value.State, sam.UnrankedReason = multiplayer.AvailabilityPartial, multiplayer.UnrankedCostCoverage
	sam.Coverage = multiplayer.Coverage{PricedBasisPoints: intValue(7100)}
	sam.MostUsedModel = &multiplayer.ModelUsage{ModelID: "gpt-5.2-codex", Resolution: "inferred"}
	sam.Limitations = multiplayer.Limitations{UnknownModelBuckets: 4, InferredModelBuckets: 212, APIEstimateUnavailableBuckets: 61}
	sam.Trend = multiplayer.Trend{PreviousValue: decimal(498_200_000)}

	// None of this member's usage has a price, and a day looks implausible.
	max := member("a-very-long-github-login-that-must-truncate", 0, 0, false)
	max.Value, max.UnrankedReason = multiplayer.Value{State: multiplayer.AvailabilityUnavailable}, multiplayer.UnrankedValueUnavailable
	max.Coverage = multiplayer.Coverage{PricedBasisPoints: intValue(0)}
	max.MostUsedModel = &multiplayer.ModelUsage{ModelID: "codex-auto-review", Resolution: "unknown"}
	max.Plausibility.DailyTokensAboveLimit = true

	zed := multiplayer.Row{DisplayName: "zed", Value: multiplayer.Value{State: multiplayer.AvailabilityEmpty}, UnrankedReason: multiplayer.UnrankedNoUsage}

	return &multiplayer.Board{
		Scope: multiplayer.ScopeGroup, Timezone: "America/Vancouver", GeneratedAt: boardTime,
		CostRankMinPricedBasisPoints: 9500, TotalMembers: 8,
		Rows: []multiplayer.Row{dana, kai, rio, lee, you, sam, max, zed},
	}
}

// longBoard has more members than a terminal shows, with the viewer far
// down it, as a busy global board does.
func longBoard(members, viewerRank, total int) *multiplayer.Board {
	board := &multiplayer.Board{
		Scope: multiplayer.ScopeGlobal, Timezone: "UTC", GeneratedAt: boardTime,
		CostRankMinPricedBasisPoints: 9500, MinRankedMembers: 3, TotalMembers: total,
	}
	for rank := 1; rank <= members; rank++ {
		row := member(fmt.Sprintf("member-%03d", rank), uint64(2_000_000_000/rank), rank, rank == viewerRank)
		if rank == viewerRank {
			row.DisplayName = "you"
		}
		row.Trend = multiplayer.Trend{PreviousRank: intValue(rank + rank%3 - 1), RankChange: intValue(rank%3 - 1)}
		board.Rows = append(board.Rows, row)
	}
	return board
}

// smallGlobal is a global board with too few rankable members: the server
// hides every rank and says so.
func smallGlobal(listed bool) *multiplayer.Board {
	other := member("dana", 1_180_000_000, 0, false)
	other.UnrankedReason, other.LastSyncedAt = multiplayer.UnrankedScopeTooSmall, nil
	other.Trend = multiplayer.Trend{New: true}
	board := &multiplayer.Board{
		Scope: multiplayer.ScopeGlobal, Timezone: "UTC", GeneratedAt: boardTime,
		CostRankMinPricedBasisPoints: 9500, MinRankedMembers: 3, RanksHidden: true, TotalMembers: 1,
		Rows: []multiplayer.Row{other},
	}
	if listed {
		you := member("you", 640_000_000, 0, true)
		you.UnrankedReason, you.LastSyncedAt = multiplayer.UnrankedScopeTooSmall, nil
		you.Trend = multiplayer.Trend{PreviousValue: decimal(512_100_000)}
		board.Rows, board.TotalMembers = append(board.Rows, you), 2
	}
	return board
}

// sampleRemote is a signed-in account that owns one group, is a member of
// another, and can read a small global board it is not listed on.
func sampleRemote() *fakeMultiplayer {
	return &fakeMultiplayer{
		groups: []multiplayer.Group{
			{ID: "group-night", Name: "night-shift", Timezone: "America/Vancouver", Role: "owner", Visibility: "private"},
			{ID: "group-day", Name: "day-crew", Timezone: "UTC", Role: "member", Visibility: "public"},
		},
		boards: map[string]*multiplayer.Board{
			"group-night":           nightShift(),
			"group-day":             longBoard(40, 31, 40),
			multiplayer.GlobalScope: smallGlobal(false),
		},
	}
}

// start builds a model at a terminal size and runs its first load.
func start(t *testing.T, app Application, config Config, width, height int) Model {
	t.Helper()
	if config.Theme == nil {
		plain := theme.Fixed(false, true, config.ASCII)
		config.Theme = &plain
	}
	model := NewModel(t.Context(), app, config)
	model.tickEvery, model.flashFor = time.Nanosecond, time.Nanosecond
	updated, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return drain(updated.(Model), model.Init())
}

// press sends keys one at a time and runs the commands they start.
func press(model Model, keys ...string) Model {
	for _, name := range keys {
		message := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(name)}
		switch name {
		case "tab":
			message = tea.KeyMsg{Type: tea.KeyTab}
		case "shift+tab":
			message = tea.KeyMsg{Type: tea.KeyShiftTab}
		case "enter":
			message = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			message = tea.KeyMsg{Type: tea.KeyEsc}
		case "down":
			message = tea.KeyMsg{Type: tea.KeyDown}
		}
		updated, command := model.Update(message)
		model = drain(updated.(Model), command)
	}
	return model
}

// drain runs a command and everything it leads to. Timers are dropped: the
// spinner's frame and the moment a success message fades are not what these
// tests drive.
func drain(model Model, command tea.Cmd) Model {
	for queue := []tea.Cmd{command}; len(queue) > 0; {
		next := queue[0]
		queue = queue[1:]
		if next == nil {
			continue
		}
		switch message := next().(type) {
		case tea.BatchMsg:
			queue = append(queue, message...)
		case spinnerTick, flashExpired:
		case claimCompleted:
			if !errors.Is(message.err, errHeld) {
				updated, following := model.Update(message)
				model = updated.(Model)
				queue = append(queue, following)
			}
		case claimSynced:
			if !errors.Is(message.err, errHeld) {
				updated, following := model.Update(message)
				model = updated.(Model)
				queue = append(queue, following)
			}
		default:
			updated, following := model.Update(message)
			model = updated.(Model)
			queue = append(queue, following)
		}
	}
	return model
}

func known(value uint64) store.AggregateValue {
	return store.AggregateValue{Value: &value, State: store.AvailabilityComplete}
}

func partial(value uint64) store.AggregateValue {
	return store.AggregateValue{Value: &value, State: store.AvailabilityPartial}
}

var unavailable = store.AggregateValue{State: store.AvailabilityUnavailable}

func coverage(basisPoints int) store.Coverage {
	return store.Coverage{PricedTokens: uint64(basisPoints), UnpricedTokens: uint64(10_000 - basisPoints), PricedBasisPoints: &basisPoints}
}

// claudeTokens reports every category; codexTokens has no cache writes,
// which makes any total that includes it a lower bound.
func claudeTokens(input, read, write5m, write1h, output uint64) store.TokenBreakdown {
	return store.TokenBreakdown{
		InputUncached: known(input), InputCacheRead: known(read), InputCacheWrite: known(write5m + write1h),
		InputCacheWrite5m: known(write5m), InputCacheWrite1h: known(write1h),
		Output: known(output), ReasoningOutput: unavailable,
	}
}

func codexTokens(input, read, output, reasoning uint64) store.TokenBreakdown {
	return store.TokenBreakdown{
		InputUncached: known(input), InputCacheRead: known(read), InputCacheWrite: unavailable,
		InputCacheWrite5m: unavailable, InputCacheWrite1h: unavailable,
		Output: known(output), ReasoningOutput: known(reasoning),
	}
}

// sampleSummary is one period of synthetic usage, scaled by parts per
// thousand so the four periods differ without four hand-written fixtures.
func sampleSummary(period store.Period, perMille uint64) store.UsageSummary {
	scale := func(value uint64) uint64 { return value * perMille / 1000 }
	opus, sonnet, haiku := "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"
	codex, mini := "gpt-5.2-codex", "gpt-5.2-mini"
	claudeAll := claudeTokens(scale(31_400_000), scale(2_610_000_000), scale(203_000_000), scale(48_700_000), scale(21_900_000))
	codexAll := codexTokens(scale(96_200_000), scale(1_190_000_000), scale(58_400_000), scale(22_100_000))
	return store.UsageSummary{
		Period: period,
		Tokens: store.TokenBreakdown{
			InputUncached: known(scale(127_600_000)), InputCacheRead: known(scale(3_800_000_000)),
			InputCacheWrite: partial(scale(251_700_000)), InputCacheWrite5m: partial(scale(203_000_000)),
			InputCacheWrite1h: partial(scale(48_700_000)), Output: known(scale(80_300_000)),
			ReasoningOutput: partial(scale(22_100_000)),
		},
		APIEstimate: partial(scale(1_180_420_000)),
		Coverage:    coverage(9230),
		Sources: []store.NamedBreakdown{
			{Name: "claude", Tokens: claudeAll, APIEstimate: known(scale(820_400_000)), Coverage: coverage(10_000)},
			{Name: "codex", Tokens: codexAll, APIEstimate: partial(scale(360_020_000)), Coverage: coverage(7610)},
		},
		Surfaces: []store.NamedBreakdown{
			{Name: "cli", Tokens: claudeTokens(scale(27_000_000), scale(2_240_000_000), scale(171_000_000), scale(48_700_000), scale(18_800_000)), APIEstimate: known(scale(701_150_000)), Coverage: coverage(10_000)},
			{Name: "desktop_code", Tokens: claudeTokens(scale(4_400_000), scale(370_000_000), scale(32_000_000), 0, scale(3_100_000)), APIEstimate: known(scale(119_250_000)), Coverage: coverage(10_000)},
			{Name: "shared_local", Tokens: codexAll, APIEstimate: partial(scale(360_020_000)), Coverage: coverage(7610)},
		},
		Models: []store.ModelBreakdown{
			{ObservedModelID: "claude-opus-5-5-20261101", CanonicalModelID: &opus, Resolution: normalize.ModelResolved,
				Tokens:      claudeTokens(scale(22_100_000), scale(1_870_000_000), scale(160_000_000), scale(48_700_000), scale(15_200_000)),
				APIEstimate: known(scale(688_300_000)), Coverage: coverage(10_000)},
			{ObservedModelID: codex, CanonicalModelID: &codex, Resolution: normalize.ModelInferred,
				UnpricedReason: modelcatalog.ReasonPromptLengthUnproven,
				Tokens:         codexTokens(scale(71_000_000), scale(905_000_000), scale(44_000_000), scale(17_300_000)),
				APIEstimate:    partial(scale(318_900_000)), Coverage: coverage(8840)},
			{ObservedModelID: sonnet, CanonicalModelID: &sonnet, Resolution: normalize.ModelReported,
				Tokens:      claudeTokens(scale(8_100_000), scale(655_000_000), scale(38_000_000), 0, scale(5_900_000)),
				APIEstimate: known(scale(121_750_000)), Coverage: coverage(10_000)},
			{ObservedModelID: "codex-auto-review", Resolution: normalize.ModelUnknown,
				UnpricedReason: modelcatalog.ReasonRoutingLabel,
				Tokens:         codexTokens(scale(19_800_000), scale(212_000_000), scale(10_100_000), scale(3_400_000)),
				APIEstimate:    unavailable, Coverage: coverage(0)},
			{ObservedModelID: haiku, CanonicalModelID: &haiku, Resolution: normalize.ModelReported,
				Tokens:      claudeTokens(scale(1_200_000), scale(85_000_000), scale(5_000_000), 0, scale(800_000)),
				APIEstimate: known(scale(10_350_000)), Coverage: coverage(10_000)},
			{ObservedModelID: mini, CanonicalModelID: &mini, Resolution: normalize.ModelReported,
				Tokens:      codexTokens(scale(5_400_000), scale(73_000_000), scale(4_300_000), scale(1_400_000)),
				APIEstimate: known(scale(41_120_000)), Coverage: coverage(10_000)},
			{ObservedModelID: "", Resolution: normalize.ModelUnknown, UnpricedReason: modelcatalog.ReasonModelUnavailable,
				Tokens:      codexTokens(scale(12_000), 0, scale(987), 0),
				APIEstimate: unavailable, Coverage: coverage(0)},
		},
	}
}

// sampleDashboard is a signed-out machine with a month of synthetic usage.
func sampleDashboard() application.Dashboard {
	location := time.FixedZone("UTC-8", -8*60*60)
	now := time.Date(2026, 11, 21, 10, 4, 0, 0, location)
	scanned := now.Add(-4 * time.Minute)
	daily := make([]store.DailyUsage, application.DailyDays)
	today := time.Date(2026, 11, 21, 0, 0, 0, 0, location)
	for index := range daily {
		daily[index].Day = today.AddDate(0, 0, index-len(daily)+1)
		// A repeatable sawtooth with quiet weekends, one day with no usage
		// and one whose records carry no counts.
		tokens := uint64((index*37+11)%23+3) * 14_100_000
		if weekday := daily[index].Day.Weekday(); weekday == time.Saturday || weekday == time.Sunday {
			tokens /= 6
		}
		daily[index].Tokens = partial(tokens)
	}
	daily[8].Tokens = store.AggregateValue{State: store.AvailabilityEmpty}
	daily[17].Tokens = unavailable
	records := func(count uint64) *uint64 { return &count }
	return application.Dashboard{
		GeneratedAt: now,
		Timezone:    "America/Vancouver",
		Today:       sampleSummary(store.PeriodToday, 31),
		Week:        sampleSummary(store.PeriodWeek, 212),
		Last30Days:  sampleSummary(store.PeriodLast30Days, 1000),
		AllTime:     sampleSummary(store.PeriodAllTime, 3140),
		Daily:       daily,
		Diagnostics: store.DiagnosticSnapshot{
			IntegrityOK: true, SchemaVersion: 7, SupportedSchemaVersion: 7, CheckpointCount: 3,
			LastSuccessfulCollection: &scanned, PendingOutbox: 3,
			AdapterHealth: []store.StoredAdapterHealth{
				{Health: normalize.SourceHealth{Source: normalize.SourceClaude, Surface: normalize.SurfaceCLI, Status: normalize.HealthDetected, RecordCount: records(13_916)}, LastSuccessAt: &scanned},
				{Health: normalize.SourceHealth{Source: normalize.SourceClaude, Surface: normalize.SurfaceDesktopCode, Status: normalize.HealthDetected, RecordCount: records(212)}, LastSuccessAt: &scanned},
				{Health: normalize.SourceHealth{Source: normalize.SourceClaude, Surface: normalize.SurfaceDesktopCowork, Status: normalize.HealthNotFound}},
				{Health: normalize.SourceHealth{Source: normalize.SourceCodex, Surface: normalize.SurfaceSharedLocal, Status: normalize.HealthDetected, RecordCount: records(4_014)}, LastSuccessAt: &scanned},
			},
		},
	}
}

// emptyDashboard is a machine where every adapter looked and found nothing.
func emptyDashboard() application.Dashboard {
	dashboard := sampleDashboard()
	empty := store.UsageSummary{}
	dashboard.Today, dashboard.Week, dashboard.Last30Days, dashboard.AllTime = empty, empty, empty, empty
	for index := range dashboard.Daily {
		dashboard.Daily[index].Tokens = store.AggregateValue{State: store.AvailabilityEmpty}
	}
	for index := range dashboard.Diagnostics.AdapterHealth {
		dashboard.Diagnostics.AdapterHealth[index].Health.Status = normalize.HealthNotFound
		dashboard.Diagnostics.AdapterHealth[index].Health.RecordCount = nil
		dashboard.Diagnostics.AdapterHealth[index].LastSuccessAt = nil
	}
	dashboard.Diagnostics.PendingOutbox = 0
	return dashboard
}

// addSource adds one more source to a summary and to its totals, so the
// shares still add up.
func addSource(summary *store.UsageSummary, name string, input, read, output, perMille uint64) {
	scale := func(value uint64) uint64 { return value * perMille / 1000 }
	tokens := store.TokenBreakdown{
		InputUncached: known(scale(input)), InputCacheRead: known(scale(read)),
		// A day-level source that records no cache writes.
		InputCacheWrite: unavailable, InputCacheWrite5m: unavailable, InputCacheWrite1h: unavailable,
		Output: known(scale(output)), ReasoningOutput: unavailable,
	}
	grow := func(total *store.AggregateValue, by uint64) {
		value := *total.Value + by
		total.Value = &value
	}
	grow(&summary.Tokens.InputUncached, scale(input))
	grow(&summary.Tokens.InputCacheRead, scale(read))
	grow(&summary.Tokens.Output, scale(output))
	summary.Sources = append(summary.Sources, store.NamedBreakdown{
		Name: name, Tokens: tokens, APIEstimate: partial(scale(input) / 4), Coverage: coverage(6400),
	})
}

// agentsDashboard is the sample machine with three of the agents read
// through ccusage in use: two read by day, one whose report was refused, and
// two that are not installed.
func agentsDashboard() application.Dashboard {
	dashboard := sampleDashboard()
	scanned := *dashboard.Diagnostics.LastSuccessfulCollection
	days := func(count uint64) *uint64 { return &count }
	agent := func(source normalize.Source, status normalize.HealthStatus, count *uint64, reason string) store.StoredAdapterHealth {
		record := store.StoredAdapterHealth{Health: normalize.SourceHealth{Source: source, Surface: normalize.SurfaceCLI, Status: status, RecordCount: count}}
		if count != nil {
			record.LastSuccessAt = &scanned
		}
		if reason != "" {
			record.Health.Warnings = []normalize.Warning{{Code: reason}}
		}
		return record
	}
	dashboard.Diagnostics.AdapterHealth = append(dashboard.Diagnostics.AdapterHealth,
		agent(normalize.SourceCopilot, normalize.HealthNotFound, nil, ""),
		agent(normalize.SourceGemini, normalize.HealthDetected, days(41), ""),
		agent(normalize.SourceGrok, normalize.HealthNotFound, nil, ""),
		agent(normalize.SourceOpenCode, normalize.HealthError, nil, "ccusage_malformed_output"),
		agent(normalize.SourceQwen, normalize.HealthDetected, days(6), ""),
	)
	for _, period := range []struct {
		summary  *store.UsageSummary
		perMille uint64
	}{{&dashboard.Today, 31}, {&dashboard.Week, 212}, {&dashboard.Last30Days, 1000}, {&dashboard.AllTime, 3140}} {
		addSource(period.summary, "gemini", 61_000_000, 148_000_000, 9_400_000, period.perMille)
		addSource(period.summary, "qwen", 8_200_000, 3_100_000, 1_300_000, period.perMille)
	}
	return dashboard
}

// agentsUnreadDashboard is the same machine on an install without ccusage:
// the agents are in use, and nothing can read them.
func agentsUnreadDashboard() application.Dashboard {
	dashboard := sampleDashboard()
	for _, source := range normalize.DayLevelSources() {
		record := store.StoredAdapterHealth{Health: normalize.SourceHealth{Source: source, Surface: normalize.SurfaceCLI, Status: normalize.HealthNotFound}}
		if source == normalize.SourceGemini || source == normalize.SourceQwen {
			record.Health.Status = normalize.HealthUnavailable
			record.Health.Warnings = []normalize.Warning{{Code: "ccusage_not_installed"}}
		}
		dashboard.Diagnostics.AdapterHealth = append(dashboard.Diagnostics.AdapterHealth, record)
	}
	return dashboard
}

// dayLevelRemote is sampleRemote where the viewer's week includes usage a
// day-level agent reported.
func dayLevelRemote() *fakeMultiplayer {
	remote := sampleRemote()
	board := remote.boards["group-night"]
	for index := range board.Rows {
		if !board.Rows[index].CurrentUser {
			continue
		}
		board.Rows[index].SourceMix = append(board.Rows[index].SourceMix, multiplayer.SourceMix{Source: "gemini", TotalTokens: decimal(46_300_000)})
		board.Rows[index].Limitations.DayLevelBuckets = 7
	}
	return remote
}
