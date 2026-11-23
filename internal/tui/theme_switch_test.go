package tui

import (
	"errors"
	"github.com/ansonnchan/tokenholic/internal/application"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/ansonnchan/tokenholic/internal/tui/theme"
)

// switchable starts the sample dashboard in color with a look that can be
// switched, recording every mode it is asked to remember.
func switchable(t *testing.T, mode theme.Mode, saveErr error) (Model, *[]theme.Mode) {
	t.Helper()
	var saved []theme.Mode
	tokens := theme.Fixed(true, mode == theme.Dark, false)
	model := start(t, &fakeApplication{dashboard: sampleDashboard()}, Config{
		Theme:  &tokens,
		Themes: func(mode theme.Mode) theme.Theme { return theme.Fixed(true, mode == theme.Dark, false) },
		SaveMode: func(mode theme.Mode) error {
			saved = append(saved, mode)
			return saveErr
		},
	}, 88, 30)
	return model, &saved
}

func pressTheme(_ *testing.T, model Model) Model { return press(model, "t") }

func TestTSwitchesTheLookAndRemembersIt(t *testing.T) {
	model, saved := switchable(t, theme.Dark, nil)
	if model.theme.Mode != theme.Dark {
		t.Fatalf("started in %s, want dark", model.theme.Mode)
	}
	light := pressTheme(t, model)
	if light.theme.Mode != theme.Light || len(*saved) != 1 || (*saved)[0] != theme.Light {
		t.Fatalf("after t: mode %s, saved %v", light.theme.Mode, *saved)
	}
	if light.View() == model.View() {
		t.Fatal("the view did not change with the look")
	}
	dark := pressTheme(t, light)
	if dark.theme.Mode != theme.Dark || len(*saved) != 2 || (*saved)[1] != theme.Dark {
		t.Fatalf("after t twice: mode %s, saved %v", dark.theme.Mode, *saved)
	}
	if dark.View() != model.View() {
		t.Fatal("switching there and back did not restore the view")
	}
	// The switch works from every view, and SETTINGS says how.
	for _, section := range []string{"1", "2", "3", "4", "5"} {
		if switched := pressTheme(t, press(model, section)); switched.theme.Mode != theme.Light {
			t.Errorf("t in view %s left the look %s", section, switched.theme.Mode)
		}
	}
	if settings := view(press(model, "5")); !strings.Contains(settings, "dark; press t for light") {
		t.Errorf("SETTINGS does not say how to switch: %q", settings)
	}
	if settings := view(press(light, "5")); !strings.Contains(settings, "light; press t for dark") {
		t.Errorf("SETTINGS in light: %q", settings)
	}
	if help := view(press(model, "?")); !strings.Contains(help, "switch between the dark and light theme") {
		t.Errorf("help does not list t: %q", help)
	}
}

func TestAnUnsavedSwitchSaysSo(t *testing.T) {
	model, saved := switchable(t, theme.Dark, errors.New("synthetic: read-only"))
	light := pressTheme(t, model)
	// The look still changes for this run; only remembering it failed.
	if light.theme.Mode != theme.Light || len(*saved) != 1 {
		t.Fatalf("mode %s, saved %v", light.theme.Mode, *saved)
	}
	if settings := view(press(light, "5")); !strings.Contains(settings, "could not be saved") {
		t.Errorf("SETTINGS hides the failure: %q", settings)
	}
}

func TestNoColorHasNoLookToSwitch(t *testing.T) {
	saves := 0
	plain := theme.Fixed(false, true, false)
	model := start(t, &fakeApplication{dashboard: sampleDashboard()}, Config{
		Theme:    &plain,
		Themes:   func(theme.Mode) theme.Theme { t.Fatal("a theme was rebuilt without color"); return plain },
		SaveMode: func(theme.Mode) error { saves++; return nil },
	}, 88, 30)
	switched := pressTheme(t, model)
	if saves != 0 || switched.View() != model.View() {
		t.Fatalf("t changed an uncolored terminal: %d saves", saves)
	}
	if settings := view(press(model, "5")); !strings.Contains(settings, "off; this terminal is not using color") {
		t.Errorf("SETTINGS = %q", settings)
	}
	// Nothing is painted and no escape sequence is written.
	if strings.Contains(model.View(), "\x1b[") {
		t.Fatal("an uncolored view contains an escape sequence")
	}
}

// With color the view covers the whole terminal, so no cell shows the
// terminal's own background.
func TestViewPaintsTheWholeTerminal(t *testing.T) {
	for _, mode := range []theme.Mode{theme.Dark, theme.Light} {
		model, _ := switchable(t, mode, nil)
		for _, keys := range [][]string{nil, {"2"}, {"5"}, {"?"}} {
			rendered := press(model, keys...).View()
			if strings.HasSuffix(rendered, "\n") {
				t.Errorf("%s %v: a full-height view ends with a line break, which scrolls the header away", mode, keys)
			}
			lines := strings.Split(rendered, "\n")
			if len(lines) != 30 {
				t.Errorf("%s %v: %d lines, want the terminal's 30", mode, keys, len(lines))
			}
			for index, line := range lines {
				if width := lipgloss.Width(line); width != 88 {
					t.Errorf("%s %v: line %d is %d cells wide, want 88", mode, keys, index, width)
					break
				}
			}
		}
	}
}

// TestWritePaintedPreviews writes painted frames for review as images. It
// runs only when TOKENHOLIC_TUI_PREVIEW names a directory.
func TestWritePaintedPreviews(t *testing.T) {
	directory := os.Getenv("TOKENHOLIC_TUI_PREVIEW")
	if directory == "" {
		t.Skip("set TOKENHOLIC_TUI_PREVIEW to a directory to write previews")
	}
	previews := []struct {
		name      string
		dashboard func() application.Dashboard
		remote    func() *fakeMultiplayer
		keys      []string
		// height is tall enough to show the whole view.
		height int
		// claimed is a device whose claim offers hourly sync.
		claimed bool
		// account is a signed-out device whose claim goes as it says.
		account func() *fakeAccount
	}{
		{name: "home", dashboard: sampleDashboard, height: 26},
		{name: "board", dashboard: sampleDashboard, remote: sampleRemote, keys: []string{"2"}, height: 24},
		{name: "settings", dashboard: sampleDashboard, keys: []string{"5"}, height: 44},
		{name: "sources_agents", dashboard: agentsDashboard, keys: []string{"4"}, height: 40},
		{name: "sources_agents_unread", dashboard: agentsUnreadDashboard, keys: []string{"4"}, height: 40},
		{name: "board_day_level", dashboard: agentsDashboard, remote: dayLevelRemote, keys: []string{"2"}, height: 36},
		{name: "claim_autosync", dashboard: sampleDashboard, remote: sampleRemote, keys: []string{"c", "enter"}, height: 22, claimed: true},
		{name: "claimed_autosync", dashboard: sampleDashboard, remote: sampleRemote, keys: []string{"c", "enter", "enter"}, height: 30, claimed: true},
		{name: "claim_uploading_progress", dashboard: sampleDashboard, keys: []string{"c", "enter"}, height: 22, account: func() *fakeAccount {
			return &fakeAccount{syncErr: errHeld, progress: longUpload}
		}},
		{name: "claim_upload_stopped", dashboard: sampleDashboard, keys: []string{"c", "enter"}, height: 22, account: func() *fakeAccount {
			return &fakeAccount{syncErr: errors.New("synthetic: connection lost"), progress: longUpload}
		}},
	}
	for _, mode := range []theme.Mode{theme.Dark, theme.Light} {
		for _, width := range []int{120, 88, 48} {
			for _, preview := range previews {
				tokens := theme.Fixed(true, mode == theme.Dark, false)
				config := Config{
					Theme:  &tokens,
					Themes: func(mode theme.Mode) theme.Theme { return theme.Fixed(true, mode == theme.Dark, false) },
				}
				if preview.remote != nil {
					config.Multiplayer = preview.remote()
				}
				if preview.claimed {
					config.Account, config.Autosync = &fakeAccount{uploaded: 4014}, &fakeAutosync{}
				}
				if preview.account != nil {
					config.Account = preview.account()
				}
				model := start(t, &fakeApplication{dashboard: preview.dashboard()}, config, width, preview.height)
				path := filepath.Join(directory, preview.name+"_"+mode.String()+"_"+"w"+strconv.Itoa(width)+".ansi")
				if err := os.WriteFile(path, []byte(press(model, preview.keys...).View()+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}
