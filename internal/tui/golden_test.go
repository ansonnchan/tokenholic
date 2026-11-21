package tui

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ansonnchan/tokenholic/internal/application"
	"github.com/ansonnchan/tokenholic/internal/store"
	"github.com/ansonnchan/tokenholic/internal/tui/goldentest"
	"github.com/ansonnchan/tokenholic/internal/tui/theme"
)

// screen is one frame the snapshots fix: a starting dashboard, the keys that
// reach the state, and an optional terminal height.
type screen struct {
	name      string
	dashboard func() application.Dashboard
	loadError error
	keys      []string
	height    int
}

var screens = []screen{
	{name: "home", dashboard: sampleDashboard},
	{name: "home_detail", dashboard: sampleDashboard, keys: []string{"d"}},
	{name: "home_today", dashboard: quietToday, keys: []string{"p", "p"}},
	{name: "home_no_sources", dashboard: emptyDashboard},
	{name: "home_load_failed", dashboard: sampleDashboard, loadError: errors.New("synthetic failure")},
	{name: "home_short", dashboard: sampleDashboard, keys: []string{"j", "j"}, height: 20},
	{name: "models", dashboard: sampleDashboard, keys: []string{"3", "j"}},
	{name: "models_short", dashboard: sampleDashboard, keys: []string{"3", "j", "j", "j", "j", "j"}, height: 24},
	{name: "sources", dashboard: needsAttentionDashboard, keys: []string{"4"}},
	{name: "settings", dashboard: sampleDashboard, keys: []string{"5"}},
	{name: "help", dashboard: sampleDashboard, keys: []string{"?"}},
}

// quietToday is the sample machine before any usage today.
func quietToday() application.Dashboard {
	dashboard := sampleDashboard()
	dashboard.Today = store.UsageSummary{Period: store.PeriodToday}
	return dashboard
}

// needsAttentionDashboard has one source that cannot be read.
func needsAttentionDashboard() application.Dashboard {
	dashboard := sampleDashboard()
	dashboard.Diagnostics.AdapterHealth[1].Health.Status = "permission_denied"
	dashboard.Diagnostics.AdapterHealth[1].Health.RecordCount = nil
	return dashboard
}

func renderScreen(t *testing.T, screen screen, tokens theme.Theme, width int) []string {
	t.Helper()
	app := &fakeApplication{dashboard: screen.dashboard(), loadError: screen.loadError}
	model := start(t, app, Config{Theme: &tokens}, width, screen.height)
	return press(model, screen.keys...).frame()
}

func TestScreenGoldenSnapshots(t *testing.T) {
	t.Parallel()
	variants := []struct {
		name   string
		tokens theme.Theme
		widths []int
	}{
		{"color", theme.Fixed(true, true, false), []int{48, 88, 120}},
		{"plain", theme.Fixed(false, true, false), []int{48, 88, 120}},
		{"light", theme.Fixed(true, false, false), []int{120}},
		{"ascii", theme.Fixed(false, true, true), []int{48}},
	}
	for _, screen := range screens {
		for _, variant := range variants {
			for _, width := range variant.widths {
				name := fmt.Sprintf("%s_%d_%s", screen.name, width, variant.name)
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					lines := renderScreen(t, screen, variant.tokens, width)
					goldentest.Fits(t, lines, width, variant.tokens.ASCII)
					if screen.height > 0 && len(lines) > screen.height {
						t.Errorf("frame is %d rows in a terminal of %d", len(lines), screen.height)
					}
					goldentest.Compare(t, name, lines)
				})
			}
		}
	}
}
