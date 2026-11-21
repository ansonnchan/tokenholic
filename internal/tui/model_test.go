package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/application"
	"github.com/ansonnchan/tokenholic/internal/multiplayer"
	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/store"
	tea "github.com/charmbracelet/bubbletea"
)

type fakeApplication struct {
	dashboard  application.Dashboard
	loadError  error
	refreshErr error
	loads      int
	refreshes  int
}

type fakeMultiplayer struct {
	snapshot   multiplayer.Snapshot
	selectedID string
	period     multiplayer.Period
	metric     multiplayer.Metric
	created    string
	joined     string
}

func (f *fakeMultiplayer) Load(_ context.Context, selectedID string, period multiplayer.Period, metric multiplayer.Metric) (multiplayer.Snapshot, error) {
	f.selectedID, f.period, f.metric = selectedID, period, metric
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

func (a *fakeApplication) Load(context.Context) (application.Dashboard, error) {
	a.loads++
	return a.dashboard, a.loadError
}

func (a *fakeApplication) Refresh(context.Context) error {
	a.refreshes++
	return a.refreshErr
}

func TestModelLoadsAndNavigatesWithKeyboard(t *testing.T) {
	t.Parallel()
	app := &fakeApplication{dashboard: usageDashboard(store.AvailabilityComplete, 42)}
	app.dashboard.Diagnostics.AdapterHealth = []store.StoredAdapterHealth{{
		Health: normalize.SourceHealth{Source: normalize.SourceCodex, Surface: normalize.SurfaceCLI, Status: normalize.HealthDegraded},
	}}
	model := NewModel(t.Context(), app, Config{NoColor: true})
	message := model.Init()()
	updated, command := model.Update(message)
	model = updated.(Model)
	if command != nil || model.state != StateWarning || !strings.Contains(model.View(), "Total tokens: 42") {
		t.Fatalf("initial model = %#v view=%q", model, model.View())
	}

	updated, command = model.Update(key('2'))
	model = updated.(Model)
	if model.section != SourcesSection || model.state != StateLoading || command == nil {
		t.Fatalf("navigation model = %#v", model)
	}
	updated, _ = model.Update(command())
	model = updated.(Model)
	if model.state != StateWarning || !strings.Contains(model.View(), "[warning] 1 local adapter state(s) need attention") {
		t.Fatalf("warning model = %#v view=%q", model, model.View())
	}

	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyTab})
	if got := updated.(Model).section; got != ModelsSection {
		t.Fatalf("tab section = %v, want models", got)
	}
	updated, _ = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if got := updated.(Model).section; got != SourcesSection {
		t.Fatalf("shift-tab section = %v, want sources", got)
	}
}

func TestModelIgnoresStaleLoadsAndRefreshesThroughApplication(t *testing.T) {
	t.Parallel()
	app := &fakeApplication{dashboard: usageDashboard(store.AvailabilityComplete, 1)}
	model := NewModel(t.Context(), app, Config{NoColor: true})
	updated, _ := model.Update(key('2'))
	model = updated.(Model)
	updated, command := model.Update(loadResult{section: OverviewSection, requestID: 1, dashboard: usageDashboard(store.AvailabilityComplete, 99)})
	model = updated.(Model)
	if command != nil || strings.Contains(model.View(), "stale") || model.section != SourcesSection {
		t.Fatalf("stale result changed model = %#v", model)
	}

	updated, command = model.Update(key('r'))
	model = updated.(Model)
	if command == nil || !model.refreshing {
		t.Fatalf("refresh did not start = %#v", model)
	}
	updated, command = model.Update(command())
	model = updated.(Model)
	if app.refreshes != 1 || command == nil {
		t.Fatalf("refresh result = %#v calls=%d", model, app.refreshes)
	}
}

func TestModelStatesRemainExplicitWithoutColor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		message loadResult
		want    string
	}{
		{name: "empty", message: loadResult{section: OverviewSection, requestID: 1}, want: "[empty]"},
		{name: "error", message: loadResult{section: OverviewSection, requestID: 1, err: errors.New("private detail")}, want: "[error]"},
		{name: "warning", message: loadResult{section: OverviewSection, requestID: 1, dashboard: usageDashboard(store.AvailabilityPartial, 1)}, want: "[warning]"},
		{name: "ready", message: loadResult{section: OverviewSection, requestID: 1, dashboard: usageDashboard(store.AvailabilityComplete, 1)}, want: "[ready]"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := NewModel(t.Context(), &fakeApplication{}, Config{NoColor: true})
			updated, _ := model.Update(test.message)
			view := updated.(Model).View()
			if !strings.Contains(view, test.want) || strings.Contains(view, "\x1b[") || strings.Contains(view, "private detail") {
				t.Fatalf("View() = %q", view)
			}
		})
	}
}

func TestModelExplainsWhenNoSupportedSourceIsAvailable(t *testing.T) {
	t.Parallel()
	dashboard := application.Dashboard{Diagnostics: store.DiagnosticSnapshot{
		AdapterHealth: []store.StoredAdapterHealth{
			{Health: normalize.SourceHealth{Source: normalize.SourceClaude, Surface: normalize.SurfaceCLI, Status: normalize.HealthNotFound}},
			{Health: normalize.SourceHealth{Source: normalize.SourceCodex, Surface: normalize.SurfaceSharedLocal, Status: normalize.HealthNotFound}},
		},
	}}
	model := NewModel(t.Context(), &fakeApplication{dashboard: dashboard}, Config{NoColor: true})
	updated, _ := model.Update(model.Init()())
	updated, _ = updated.(Model).Update(tea.WindowSizeMsg{Width: MinimumWidth, Height: 20})
	view := updated.(Model).View()
	for _, want := range []string{"[empty] No supported coding-agent usage", "Claude Code/Desktop", "Codex CLI/Desktop", "press r", "tokenholic doctor"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() = %q, want %q", view, want)
		}
	}
	if strings.Contains(view, "[warning]") || strings.Contains(view, "\x1b[") {
		t.Fatalf("missing optional sources rendered as a warning or used color: %q", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if len([]rune(line)) > MinimumWidth {
			t.Fatalf("empty-state line is %d columns: %q", len([]rune(line)), line)
		}
	}
}

func TestNarrowLayoutAndHelp(t *testing.T) {
	t.Parallel()
	model := NewModel(t.Context(), nil, Config{NoColor: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: MinimumWidth - 1, Height: 20})
	view := updated.(Model).View()
	if !strings.Contains(view, "Terminal is too narrow") || !strings.Contains(view, "48 columns") {
		t.Fatalf("too-narrow View() = %q", view)
	}
	updated, _ = updated.(Model).Update(key('?'))
	if view = updated.(Model).View(); !strings.Contains(view, "1–5 views") {
		t.Fatalf("too-narrow help View() = %q", view)
	}

	updated, _ = updated.(Model).Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	view = updated.(Model).View()
	if !strings.Contains(view, "tab/arrows navigate") || strings.Contains(view, "move between views") {
		t.Fatalf("narrow help View() = %q", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if len([]rune(line)) > 60 {
			t.Fatalf("narrow line is %d columns: %q", len([]rune(line)), line)
		}
	}
}

func TestRefreshFailureIsActionable(t *testing.T) {
	t.Parallel()
	app := &fakeApplication{refreshErr: errors.New("private path")}
	model := NewModel(t.Context(), app, Config{NoColor: true})
	updated, command := model.Update(key('r'))
	updated, command = updated.(Model).Update(command())
	if command != nil {
		t.Fatal("failed refresh returned another command")
	}
	view := updated.(Model).View()
	if !strings.Contains(view, "Could not refresh local data") || strings.Contains(view, "private path") {
		t.Fatalf("refresh failure View() = %q", view)
	}
}

func TestNarrowSettingsContentWrapsWithoutLosingPrivacyMeaning(t *testing.T) {
	t.Parallel()
	app := &fakeApplication{dashboard: usageDashboard(store.AvailabilityComplete, 1)}
	model := NewModel(t.Context(), app, Config{NoColor: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: MinimumWidth, Height: 30})
	updated, command := updated.(Model).Update(key('4'))
	updated, _ = updated.(Model).Update(command())
	view := updated.(Model).View()
	if !strings.Contains(view, "Never stored: prompts") || !strings.Contains(view, "repository") || !strings.Contains(view, "branch names") {
		t.Fatalf("settings View() lost privacy explanation: %q", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if len([]rune(line)) > MinimumWidth {
			t.Fatalf("settings line is %d columns: %q", len([]rune(line)), line)
		}
	}
}

func TestGroupsViewLoadsSwitchesPeriodMetricAndSelection(t *testing.T) {
	t.Parallel()
	value := "20"
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	remote := &fakeMultiplayer{snapshot: multiplayer.Snapshot{
		Groups: []multiplayer.Group{
			{ID: "group-one", Name: "One", Timezone: "UTC", Role: "owner"},
			{ID: "group-two", Name: "Two", Timezone: "UTC", Role: "member"},
		},
		Selected: multiplayer.Group{ID: "group-one", Name: "One", Timezone: "UTC", Role: "owner"},
		Board: &multiplayer.Board{
			GroupID: "group-one", GroupName: "One", Timezone: "UTC",
			Period: multiplayer.PeriodToday, Metric: multiplayer.MetricTotalTokens,
			GeneratedAt: now, Rows: []multiplayer.Row{{
				Rank: new(int), DisplayName: "Alpha", CurrentUser: true,
				Value:       multiplayer.Value{Value: &value, State: multiplayer.AvailabilityComplete},
				Limitations: multiplayer.Limitations{UnknownModelBuckets: 1},
			}},
		},
	}}
	*remote.snapshot.Board.Rows[0].Rank = 1
	model := NewModel(t.Context(), &fakeApplication{}, Config{NoColor: true, Multiplayer: remote})
	updated, command := model.Update(key('5'))
	if command == nil {
		t.Fatal("groups navigation did not load")
	}
	updated, _ = updated.(Model).Update(command())
	model = updated.(Model)
	view := model.View()
	for _, want := range []string{"Group 1/2 · One", "Your standing: #1 of 1", "Alpha (you)", "unknown model 1"} {
		if !strings.Contains(view, want) {
			t.Fatalf("group View() = %q, want %q", view, want)
		}
	}
	updated, command = model.Update(key('p'))
	updated, _ = updated.(Model).Update(command())
	if remote.period != multiplayer.PeriodWeek {
		t.Fatalf("period = %s", remote.period)
	}
	updated, command = updated.(Model).Update(key('m'))
	updated, _ = updated.(Model).Update(command())
	if remote.metric != multiplayer.MetricAPIEstimate {
		t.Fatalf("metric = %s", remote.metric)
	}
	updated, command = updated.(Model).Update(key('j'))
	updated, _ = updated.(Model).Update(command())
	if remote.selectedID != "group-two" || !strings.Contains(updated.(Model).View(), "Group 2/2 · Two") {
		t.Fatalf("selected group/view = %q/%q", remote.selectedID, updated.(Model).View())
	}
}

func TestGroupsViewOffersClaimWhenMultiplayerIsNotConfigured(t *testing.T) {
	t.Parallel()
	model := NewModel(t.Context(), &fakeApplication{}, Config{NoColor: true})
	updated, command := model.Update(key('5'))
	if command == nil {
		t.Fatal("groups navigation did not load")
	}
	updated, _ = updated.(Model).Update(command())
	view := updated.(Model).View()
	if !strings.Contains(view, "tokenholic claim") || strings.Contains(view, "tokenholic login") {
		t.Fatalf("groups claim guidance = %q", view)
	}
}

func TestGroupsViewCreatesAndMasksInviteInput(t *testing.T) {
	t.Parallel()
	remote := &fakeMultiplayer{snapshot: multiplayer.Snapshot{Groups: []multiplayer.Group{
		{ID: "group-one", Name: "One", Timezone: "UTC", Role: "owner"},
	}, Selected: multiplayer.Group{ID: "group-one", Name: "One", Timezone: "UTC", Role: "owner"}}}
	model := NewModel(t.Context(), &fakeApplication{}, Config{NoColor: true, Multiplayer: remote})
	updated, command := model.Update(key('5'))
	updated, _ = updated.(Model).Update(command())
	updated, _ = updated.(Model).Update(key('c'))
	updated, _ = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("New group")})
	if !strings.Contains(updated.(Model).View(), "New group_") {
		t.Fatalf("create input View() = %q", updated.(Model).View())
	}
	updated, command = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	updated, command = updated.(Model).Update(command())
	updated, _ = updated.(Model).Update(command())
	if remote.created != "New group" {
		t.Fatalf("created = %q", remote.created)
	}
	updated, _ = updated.(Model).Update(key('i'))
	updated, _ = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ovt_secret")})
	view := updated.(Model).View()
	if strings.Contains(view, "ovt_secret") || !strings.Contains(view, "**********_") {
		t.Fatalf("invite input was not masked: %q", view)
	}
	updated, command = updated.(Model).Update(tea.KeyMsg{Type: tea.KeyEnter})
	_, _ = updated.(Model).Update(command())
	if remote.joined != "ovt_secret" {
		t.Fatalf("joined with %q", remote.joined)
	}
}

func key(value rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{value}}
}

func usageDashboard(state store.Availability, input uint64) application.Dashboard {
	zero := uint64(0)
	breakdown := store.TokenBreakdown{
		InputUncached:   store.AggregateValue{Value: &input, State: state},
		InputCacheRead:  store.AggregateValue{Value: &zero, State: store.AvailabilityComplete},
		InputCacheWrite: store.AggregateValue{Value: &zero, State: store.AvailabilityComplete},
		Output:          store.AggregateValue{Value: &zero, State: store.AvailabilityComplete},
		ReasoningOutput: store.AggregateValue{Value: &zero, State: store.AvailabilityComplete},
	}
	estimate := store.AggregateValue{Value: &zero, State: store.AvailabilityComplete}
	summary := store.UsageSummary{Tokens: breakdown, APIEstimate: estimate}
	return application.Dashboard{
		GeneratedAt: time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC),
		Timezone:    "UTC", Today: summary, Week: summary, Last30Days: summary, AllTime: summary,
		Diagnostics: store.DiagnosticSnapshot{IntegrityOK: true, SchemaVersion: 3, SupportedSchemaVersion: 3},
	}
}
