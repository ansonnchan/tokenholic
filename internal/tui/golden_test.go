package tui

import (
	"errors"
	"fmt"
	"testing"

	"github.com/ansonnchan/tokenholic/internal/application"
	"github.com/ansonnchan/tokenholic/internal/multiplayer"
	"github.com/ansonnchan/tokenholic/internal/store"
	"github.com/ansonnchan/tokenholic/internal/tui/goldentest"
	"github.com/ansonnchan/tokenholic/internal/tui/theme"
	tea "github.com/charmbracelet/bubbletea"
)

// screen is one frame the snapshots fix: a starting dashboard, the keys that
// reach the state, and an optional terminal height.
type screen struct {
	name      string
	dashboard func() application.Dashboard
	loadError error
	keys      []string
	height    int
	// remote is the signed-in account behind the board area, when any.
	remote func() *fakeMultiplayer
	// account is the device's claim state, when an API is configured.
	account func() *fakeAccount
	// firstLaunch starts from a machine that has never been scanned.
	firstLaunch bool
	// scanning stops at the first frame of that launch, mid-scan.
	scanning bool
	// notice is the fixed header label, as the demo sets it.
	notice string
	// autosync is the scheduler behind the claim's hourly-sync offer.
	autosync func() *fakeAutosync
}

var screens = []screen{
	{name: "home", dashboard: sampleDashboard},
	{name: "home_detail", dashboard: sampleDashboard, keys: []string{"d"}},
	{name: "home_notice", dashboard: sampleDashboard, notice: "DEMO: invented data"},
	{name: "home_today", dashboard: quietToday, keys: []string{"p", "p"}},
	{name: "home_no_sources", dashboard: emptyDashboard},
	{name: "home_load_failed", dashboard: sampleDashboard, loadError: errors.New("synthetic failure")},
	{name: "home_short", dashboard: sampleDashboard, keys: []string{"j", "j"}, height: 20},
	{name: "models", dashboard: sampleDashboard, keys: []string{"3", "j"}},
	{name: "models_short", dashboard: sampleDashboard, keys: []string{"3", "j", "j", "j", "j", "j"}, height: 24},
	{name: "sources", dashboard: needsAttentionDashboard, keys: []string{"4"}},
	{name: "sources_agents", dashboard: agentsDashboard, keys: []string{"4"}, height: 40},
	{name: "sources_agents_unread", dashboard: agentsUnreadDashboard, keys: []string{"4"}, height: 40},
	{name: "settings", dashboard: sampleDashboard, keys: []string{"5"}},
	{name: "help", dashboard: sampleDashboard, keys: []string{"?"}},
	{name: "board", dashboard: sampleDashboard, remote: sampleRemote, keys: []string{"2"}},
	{name: "board_tokens", dashboard: sampleDashboard, remote: sampleRemote, keys: []string{"2", "m"}},
	{name: "board_day_level", dashboard: agentsDashboard, remote: dayLevelRemote, keys: []string{"2"}},
	{name: "board_unranked", dashboard: sampleDashboard, remote: sampleRemote, keys: []string{"2", "j", "j"}},
	{name: "board_invite", dashboard: sampleDashboard, remote: sampleRemote, keys: []string{"2", "y"}},
	{name: "board_long", dashboard: sampleDashboard, remote: sampleRemote, keys: []string{"2", "g", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k", "k"}, height: 26},
	{name: "board_global_hidden", dashboard: sampleDashboard, remote: listedRemote, keys: []string{"2", "g", "g"}},
	{name: "board_global_join", dashboard: sampleDashboard, remote: sampleRemote, keys: []string{"2", "g", "g", "o"}},
	{name: "board_no_groups", dashboard: sampleDashboard, remote: func() *fakeMultiplayer { return &fakeMultiplayer{} }, keys: []string{"2"}},
	{name: "board_signed_out", dashboard: sampleDashboard, remote: func() *fakeMultiplayer {
		return &fakeMultiplayer{loadError: multiplayer.ErrSignedOut}
	}, keys: []string{"2"}},
	{name: "board_help", dashboard: sampleDashboard, remote: sampleRemote, keys: []string{"2", "?"}},
	{name: "welcome_scanning", dashboard: sampleDashboard, account: signedOut, firstLaunch: true, scanning: true},
	{name: "welcome", dashboard: sampleDashboard, account: signedOut, firstLaunch: true},
	{name: "welcome_local_only", dashboard: sampleDashboard, firstLaunch: true},
	{name: "welcome_no_sources", dashboard: emptyDashboard, account: signedOut, firstLaunch: true},
	{name: "home_signed_out", dashboard: sampleDashboard, account: signedOut},
	{name: "claim_confirm", dashboard: sampleDashboard, account: signedOut, firstLaunch: true, keys: []string{"c"}},
	{name: "claim_code", dashboard: sampleDashboard, account: func() *fakeAccount { return &fakeAccount{completeErr: errHeld} }, keys: []string{"c", "enter", "y"}},
	{name: "claim_uploading", dashboard: sampleDashboard, account: func() *fakeAccount { return &fakeAccount{syncErr: errHeld} }, keys: []string{"c", "enter"}},
	{name: "claim_failed", dashboard: sampleDashboard, account: func() *fakeAccount {
		return &fakeAccount{completeErr: errors.New("synthetic: expired")}
	}, keys: []string{"c", "enter"}},
	{name: "claim_no_store", dashboard: sampleDashboard, account: func() *fakeAccount {
		return &fakeAccount{beginErr: ErrNoCredentialStore}
	}, keys: []string{"c", "enter"}},
	{name: "claim_start_failed", dashboard: sampleDashboard, account: func() *fakeAccount {
		return &fakeAccount{beginErr: errors.New("synthetic: unreachable")}
	}, keys: []string{"c", "enter"}},
	{name: "claim_upload_failed", dashboard: sampleDashboard, account: func() *fakeAccount {
		return &fakeAccount{syncErr: ErrUpgradeRequired}
	}, keys: []string{"c", "enter"}},
	{name: "claimed", dashboard: sampleDashboard, remote: sampleRemote, account: func() *fakeAccount { return &fakeAccount{uploaded: 4014} }, keys: []string{"c", "enter"}},
	{name: "claim_autosync", dashboard: sampleDashboard, remote: sampleRemote, account: func() *fakeAccount { return &fakeAccount{uploaded: 4014} },
		autosync: func() *fakeAutosync { return &fakeAutosync{} }, keys: []string{"c", "enter"}},
	{name: "claim_autosync_failed", dashboard: sampleDashboard, remote: sampleRemote, account: func() *fakeAccount { return &fakeAccount{uploaded: 4014} },
		autosync: func() *fakeAutosync { return &fakeAutosync{enableErr: errors.New("synthetic: no scheduler")} }, keys: []string{"c", "enter", "enter"}},
	{name: "claimed_autosync", dashboard: sampleDashboard, remote: sampleRemote, account: func() *fakeAccount { return &fakeAccount{uploaded: 4014} },
		autosync: func() *fakeAutosync { return &fakeAutosync{} }, keys: []string{"c", "enter", "enter"}},
	{name: "board_claim", dashboard: sampleDashboard, account: signedOut, remote: func() *fakeMultiplayer {
		return &fakeMultiplayer{loadError: multiplayer.ErrSignedOut}
	}, keys: []string{"2"}},
	{name: "share", dashboard: sampleDashboard, keys: []string{"s", "y"}},
	{name: "settings_signed_in", dashboard: sampleDashboard, account: func() *fakeAccount { return &fakeAccount{login: "you"} }, keys: []string{"5"}},
}

func signedOut() *fakeAccount { return &fakeAccount{} }

// listedRemote is sampleRemote already on the small global board.
func listedRemote() *fakeMultiplayer {
	remote := sampleRemote()
	remote.boards[multiplayer.GlobalScope] = smallGlobal(true)
	return remote
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
	if screen.firstLaunch {
		app = firstLaunch(screen.dashboard())
	}
	config := Config{Theme: &tokens, Clipboard: func(string) {}, OpenURL: func(string) error { return nil }, Notice: screen.notice}
	// As when it runs for real, the look can be switched.
	config.Themes = func(mode theme.Mode) theme.Theme { return theme.Fixed(tokens.Color, mode == theme.Dark, tokens.ASCII) }
	if screen.remote != nil {
		config.Multiplayer = screen.remote()
	}
	if screen.account != nil {
		config.Account = screen.account()
	}
	if screen.autosync != nil {
		config.Autosync = screen.autosync()
	}
	if screen.scanning {
		// Deliver the account and the first load, and stop before the scan
		// they start has finished.
		model := NewModel(t.Context(), app, config)
		sized, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: screen.height})
		known, _ := sized.(Model).Update(accountResult{})
		loaded, _ := known.(Model).Update(loadResult{requestID: 1, dashboard: app.dashboard})
		return loaded.(Model).frame()
	}
	model := start(t, app, config, width, screen.height)
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
