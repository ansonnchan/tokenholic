package tui

import (
	"context"
	"testing"
	"time"

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
}

func (a *fakeApplication) Load(context.Context) (application.Dashboard, error) {
	a.loads++
	return a.dashboard, a.loadError
}

func (a *fakeApplication) Refresh(context.Context) error {
	a.refreshes++
	return a.refreshErr
}

type fakeMultiplayer struct {
	snapshot   multiplayer.Snapshot
	loadError  error
	selectedID string
	period     multiplayer.Period
	metric     multiplayer.Metric
	created    string
	joined     string
}

func (f *fakeMultiplayer) Load(_ context.Context, selectedID string, period multiplayer.Period, metric multiplayer.Metric) (multiplayer.Snapshot, error) {
	f.selectedID, f.period, f.metric = selectedID, period, metric
	if f.loadError != nil {
		return multiplayer.Snapshot{}, f.loadError
	}
	snapshot := f.snapshot
	if selectedID != "" {
		for _, group := range snapshot.Groups {
			if group.ID == selectedID {
				snapshot.Selected = group
				break
			}
		}
	}
	if snapshot.Board != nil {
		board := *snapshot.Board
		board.GroupID, board.GroupName = snapshot.Selected.ID, snapshot.Selected.Name
		board.Period, board.Metric = period, metric
		snapshot.Board = &board
	}
	return snapshot, nil
}

func (f *fakeMultiplayer) Create(_ context.Context, name string) (multiplayer.Group, error) {
	f.created = name
	group := multiplayer.Group{ID: "group-created", Name: name, Timezone: "UTC", Role: "owner"}
	f.snapshot.Groups = append(f.snapshot.Groups, group)
	return group, nil
}

func (f *fakeMultiplayer) Join(_ context.Context, code string) (multiplayer.Group, error) {
	f.joined = code
	group := multiplayer.Group{ID: "group-joined", Name: "Joined", Timezone: "UTC", Role: "member"}
	f.snapshot.Groups = append(f.snapshot.Groups, group)
	return group, nil
}

// start builds a model at a terminal size and runs its first load.
func start(t *testing.T, app Application, config Config, width, height int) Model {
	t.Helper()
	if config.Theme == nil {
		plain := theme.Fixed(false, true, config.ASCII)
		config.Theme = &plain
	}
	model := NewModel(t.Context(), app, config)
	model.tickEvery = time.Nanosecond
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

// drain runs a command and everything it leads to. Spinner ticks are
// dropped: the spinner's frame is not what these tests drive.
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
		case spinnerTick:
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
