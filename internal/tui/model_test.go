package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/ansonnchan/tokenholic/internal/store"
	"github.com/ansonnchan/tokenholic/internal/tui/widget"
	tea "github.com/charmbracelet/bubbletea"
)

func view(model Model) string {
	return model.View()
}

func TestKeysSwitchAreasInTheDocumentedOrder(t *testing.T) {
	t.Parallel()
	app := &fakeApplication{dashboard: sampleDashboard()}
	model := start(t, app, Config{}, 88, 0)
	if app.loads != 1 || !strings.Contains(view(model), "[HOME]") || !strings.Contains(view(model), "YOUR USAGE") {
		t.Fatalf("first frame = %q after %d loads", view(model), app.loads)
	}
	for key, want := range map[string][2]string{
		"2": {"[BOARDS]", "TOKENHOLIC_API_URL"}, "3": {"[MODELS]", "MODEL DETAIL"},
		"4": {"[SOURCES]", "LOCAL SOURCES"}, "5": {"[SETTINGS]", "PRIVACY"}, "1": {"[HOME]", "BY MODEL"},
	} {
		// Start somewhere else so every key is a real switch.
		from := "5"
		if key == "5" {
			from = "4"
		}
		got := view(press(model, from, key))
		if !strings.Contains(got, want[0]) || !strings.Contains(got, want[1]) {
			t.Errorf("key %s: view = %q, want %q and %q", key, got, want[0], want[1])
		}
	}
	if got := view(press(model, "tab", "tab")); !strings.Contains(got, "[MODELS]") {
		t.Errorf("tab twice = %q", got)
	}
	if got := view(press(model, "shift+tab")); !strings.Contains(got, "[SETTINGS]") {
		t.Errorf("shift+tab = %q", got)
	}
}

func TestPeriodAndDetailKeysChangeTheLocalViews(t *testing.T) {
	t.Parallel()
	model := start(t, &fakeApplication{dashboard: sampleDashboard()}, Config{}, 88, 0)
	if got := view(model); !strings.Contains(got, "[30 DAYS]") || !strings.Contains(got, "≥4.26B") || strings.Contains(got, "CATEGORIES") {
		t.Fatalf("default view = %q", got)
	}
	// 30 days -> all-time -> today: each period shows its own totals.
	if got := view(press(model, "p")); !strings.Contains(got, "[ALL-TIME]") || !strings.Contains(got, "≥13.4B") {
		t.Errorf("all-time = %q", got)
	}
	if got := view(press(model, "p", "p")); !strings.Contains(got, "[TODAY]") || !strings.Contains(got, "≥132M") {
		t.Errorf("today = %q", got)
	}
	// The period is shared by the local areas.
	if got := view(press(model, "p", "3")); !strings.Contains(got, "[ALL-TIME]") {
		t.Errorf("models after a period change = %q", got)
	}
	detail := view(press(model, "d"))
	for _, want := range []string{"CATEGORIES", "3,800,000,000", "≥251,700,000", "reasoning"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail = %q, want %q", detail, want)
		}
	}
	if got := view(press(model, "d", "d")); strings.Contains(got, "CATEGORIES") {
		t.Errorf("detail did not toggle off: %q", got)
	}
}

func TestUnknownValuesAreNeverShownAsZero(t *testing.T) {
	t.Parallel()
	dashboard := sampleDashboard()
	dashboard.Last30Days.APIEstimate = unavailable
	dashboard.Last30Days.Coverage = store.Coverage{}
	model := start(t, &fakeApplication{dashboard: dashboard}, Config{}, 88, 0)
	home := view(model)
	if strings.Contains(home, "$0.00") || strings.Contains(home, " 0%") {
		t.Fatalf("an unavailable estimate or share rendered as zero: %q", home)
	}
	if !strings.Contains(home, "—") {
		t.Fatalf("an unavailable estimate has no marker: %q", home)
	}
	// A category the source never reports says so instead of showing 0.
	models := view(press(model, "3", "j"))
	if !strings.Contains(models, "not reported") || !strings.Contains(models, "inferred") {
		t.Fatalf("model detail = %q", models)
	}
	// A known zero is a number, not a missing value.
	dashboard = sampleDashboard()
	dashboard.Last30Days.APIEstimate = known(0)
	if got := view(start(t, &fakeApplication{dashboard: dashboard}, Config{}, 88, 0)); !strings.Contains(got, "$0.00") {
		t.Fatalf("a known zero estimate = %q", got)
	}
}

func TestEstimatesAreAlwaysLabelledAndNeverCalledSpending(t *testing.T) {
	t.Parallel()
	model := start(t, &fakeApplication{dashboard: sampleDashboard()}, Config{}, 120, 0)
	for _, keys := range [][]string{nil, {"d"}, {"3"}, {"4"}, {"5"}, {"?"}} {
		got := view(press(model, keys...))
		lower := strings.ToLower(got)
		if strings.Contains(lower, "spent") || strings.Contains(lower, "spend") || strings.Contains(lower, " cost ") && !strings.Contains(lower, "would cost") {
			t.Errorf("keys %v: a cost is described as spending: %q", keys, got)
		}
		if strings.Contains(got, "$") && !strings.Contains(got, "EST") {
			t.Errorf("keys %v: a dollar amount has no EST label: %q", keys, got)
		}
	}
}

func TestModelsCursorMovesWithinTheList(t *testing.T) {
	t.Parallel()
	model := press(start(t, &fakeApplication{dashboard: sampleDashboard()}, Config{}, 88, 0), "3")
	selected := func(model Model) string {
		for _, line := range strings.Split(view(model), "\n") {
			if strings.Contains(line, "▸") {
				return line
			}
		}
		return ""
	}
	if got := selected(model); !strings.Contains(got, "claude-opus-5-5") {
		t.Fatalf("first selection = %q", got)
	}
	if got := selected(press(model, "j", "down")); !strings.Contains(got, "claude-sonnet-5-5") {
		t.Errorf("after two moves = %q", got)
	}
	if got := selected(press(model, "k")); !strings.Contains(got, "claude-opus-5-5") {
		t.Errorf("moving above the first row = %q", got)
	}
	moves := []string{"j", "j", "j", "j", "j", "j", "j", "j", "j", "j"}
	if got := selected(press(model, moves...)); !strings.Contains(got, "(no model recorded)") {
		t.Errorf("moving past the last row = %q", got)
	}
	// A shorter list in another period must not leave the cursor outside it.
	if got := selected(press(press(model, moves...), "p")); !strings.Contains(got, "claude-opus-5-5") {
		t.Errorf("after a period change = %q", got)
	}
}

func TestShortTerminalScrollsInsteadOfOverflowing(t *testing.T) {
	t.Parallel()
	model := start(t, &fakeApplication{dashboard: sampleDashboard()}, Config{}, 88, 14)
	frame := model.frame()
	if len(frame) > 14 || !strings.Contains(frame[len(frame)-2], "below") {
		t.Fatalf("clipped frame is %d rows: %q", len(frame), frame)
	}
	moves := make([]string, 60)
	for index := range moves {
		moves[index] = "j"
	}
	bottom := press(model, moves...)
	if frame := bottom.frame(); len(frame) > 14 || !strings.Contains(strings.Join(frame, "\n"), "0 below") {
		t.Fatalf("scrolled past the end: scroll=%d frame=%q", bottom.scroll, frame)
	}
	if top := press(bottom, append(moves, moves...)...); top.scroll != bottom.scroll {
		t.Fatalf("scroll kept growing: %d then %d", bottom.scroll, top.scroll)
	}
	// Growing the terminal pulls the offset back into range.
	resized, _ := bottom.Update(tea.WindowSizeMsg{Width: 88, Height: 60})
	if resized.(Model).scroll != 0 {
		t.Fatalf("scroll after growing = %d", resized.(Model).scroll)
	}
}

func TestRefreshGoesThroughTheApplicationAndKeepsTheLastStableData(t *testing.T) {
	t.Parallel()
	app := &fakeApplication{dashboard: sampleDashboard()}
	model := start(t, app, Config{}, 88, 0)
	model = press(model, "r")
	if app.refreshes != 1 || app.loads != 2 || model.loading || model.refreshing {
		t.Fatalf("refreshes=%d loads=%d loading=%t", app.refreshes, app.loads, model.loading)
	}
	// A failure can wrap a private path: it must not reach the screen, and
	// the data already shown must stay.
	app.refreshErr = errors.New("open /synthetic/private/path: denied")
	failed := view(press(model, "r"))
	if !strings.Contains(failed, "ERROR  Could not refresh local data") || strings.Contains(failed, "synthetic/private") {
		t.Fatalf("refresh failure = %q", failed)
	}
	if !strings.Contains(failed, "≥4.26B") || !strings.Contains(failed, "tokenholic doctor") {
		t.Fatalf("refresh failure dropped the last stable data: %q", failed)
	}
	app.refreshErr = nil
	if got := view(press(press(model, "r"), "r")); strings.Contains(got, "ERROR") {
		t.Fatalf("a later success kept the failure: %q", got)
	}
}

func TestSpinnerShowsOnlyWhileWorkRuns(t *testing.T) {
	t.Parallel()
	app := &fakeApplication{dashboard: sampleDashboard()}
	model := NewModel(t.Context(), app, Config{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 88, Height: 0})
	model = updated.(Model)
	if got := view(model); !strings.Contains(got, "Reading local usage") {
		t.Fatalf("first frame = %q", got)
	}
	// A tick while busy advances the spinner and asks for another.
	ticked, next := model.Update(spinnerTick{})
	if ticked.(Model).spinner != 1 || next == nil {
		t.Fatalf("busy tick: spinner=%d next=%v", ticked.(Model).spinner, next)
	}
	model = drain(model, model.Init())
	idle, next := model.Update(spinnerTick{})
	if next != nil || idle.(Model).ticking || strings.Contains(view(model), "scanning") {
		t.Fatalf("the spinner kept running while idle: %q", view(model))
	}
	// A refresh shows it in the header until the reload lands.
	scanning, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	if got := view(scanning.(Model)); !strings.Contains(got, "scanning") || !strings.Contains(got, "≥4.26B") {
		t.Fatalf("frame during a refresh = %q", got)
	}
}

func TestStaleLoadsAreIgnored(t *testing.T) {
	t.Parallel()
	app := &fakeApplication{dashboard: sampleDashboard()}
	model := start(t, app, Config{}, 88, 0)
	stale := emptyDashboard()
	updated, _ := model.Update(loadResult{requestID: model.localRequest - 1, dashboard: stale})
	if got := view(updated.(Model)); !strings.Contains(got, "≥4.26B") {
		t.Fatalf("a stale load replaced the dashboard: %q", got)
	}
}

func TestFirstLoadFailureIsActionable(t *testing.T) {
	t.Parallel()
	app := &fakeApplication{loadError: errors.New("open /synthetic/private/state.db")}
	got := view(start(t, app, Config{}, 88, 0))
	if !strings.Contains(got, "ERROR  Could not load local data") || !strings.Contains(got, "tokenholic doctor") || strings.Contains(got, "synthetic/private") {
		t.Fatalf("first load failure = %q", got)
	}
}

func TestNoSupportedSourcesGetsInstructions(t *testing.T) {
	t.Parallel()
	got := view(start(t, &fakeApplication{dashboard: emptyDashboard()}, Config{}, MinimumWidth, 0))
	for _, want := range []string{"No supported coding-agent usage found.", "Claude Code", "Codex", "tokenholic doctor", "press r to scan again"} {
		if !strings.Contains(got, want) {
			t.Errorf("empty machine = %q, want %q", got, want)
		}
	}
}

func TestSourcesThatNeedAttentionAreFlaggedEverywhere(t *testing.T) {
	t.Parallel()
	model := start(t, &fakeApplication{dashboard: needsAttentionDashboard()}, Config{}, 88, 0)
	if got := view(model); !strings.Contains(got, "WARNING  1 source needs attention; see 4 SOURCES.") {
		t.Errorf("home = %q", got)
	}
	sources := view(press(model, "4"))
	if !strings.Contains(sources, "no permission") || strings.Contains(sources, "WARNING") {
		t.Errorf("sources = %q", sources)
	}
}

func TestTooNarrowTerminalAsksForRoom(t *testing.T) {
	t.Parallel()
	model := start(t, &fakeApplication{dashboard: sampleDashboard()}, Config{}, MinimumWidth-1, 20)
	got := view(model)
	if !strings.Contains(got, "Terminal is too narrow") || !strings.Contains(got, "48 columns") || !strings.Contains(got, "q quit") {
		t.Fatalf("too-narrow view = %q", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if widget.Width(line) > MinimumWidth-1 {
			t.Errorf("line is %d cells: %q", widget.Width(line), line)
		}
	}
	if got := view(press(model, "?")); !strings.Contains(got, "1–5 views") {
		t.Fatalf("too-narrow help = %q", got)
	}
}

func TestHelpExplainsKeysAndHowToReadTheNumbers(t *testing.T) {
	t.Parallel()
	model := start(t, &fakeApplication{dashboard: sampleDashboard()}, Config{}, MinimumWidth, 0)
	help := view(press(model, "?"))
	for _, want := range []string{"KEYS", "READING THE NUMBERS", "lower bound", "never means zero", "It is an estimate"} {
		if !strings.Contains(help, want) {
			t.Errorf("help = %q, want %q", help, want)
		}
	}
	if got := view(press(model, "?", "?")); strings.Contains(got, "READING THE NUMBERS") {
		t.Errorf("help did not close: %q", got)
	}
	if got := view(press(model, "?", "3")); strings.Contains(got, "READING THE NUMBERS") || !strings.Contains(got, "[MODELS]") {
		t.Errorf("switching areas kept help open: %q", got)
	}
}

func TestStoredTextCannotControlTheTerminal(t *testing.T) {
	t.Parallel()
	if got := cleanLine("Private\x1b[31m\nGroup\x7f\u009b"); strings.ContainsAny(got, "\x1b\n\x7f\u009b") {
		t.Fatalf("cleanLine() = %q", got)
	}
}
