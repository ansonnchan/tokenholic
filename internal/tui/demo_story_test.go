package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/demo"
	"github.com/ansonnchan/tokenholic/internal/tui/theme"
)

// demoStory is the walk through `tokenholic demo` that the README's
// animation shows, kept to about ten seconds. Each step presses keys on the
// running interface and holds the frame that results; the caption says what
// was pressed.
var demoStory = []struct {
	keys    []string
	caption string
	// hold is how long the frame stays up, in hundredths of a second.
	hold int
}{
	{caption: "tokenholic: your usage, read locally", hold: 120},
	{keys: []string{"p"}, caption: "p: change the period", hold: 80},
	{keys: []string{"2"}, caption: "2: boards", hold: 120},
	{keys: []string{"m"}, caption: "m: rank by tokens instead of estimate", hold: 100},
	{keys: []string{"g", "g"}, caption: "g: the opt-in global board", hold: 100},
	{keys: []string{"o", "y"}, caption: "o: join it, after confirming", hold: 110},
	{keys: []string{"3"}, caption: "3: models", hold: 100},
	{keys: []string{"4"}, caption: "4: sources, and how each is read", hold: 100},
	{keys: []string{"t"}, caption: "t: dark look", hold: 90},
	{keys: []string{"2", "g", "m"}, caption: "2: back to your group", hold: 130},
}

// demoMoment is the fixed time the story is told at, so the frames are the
// same on every run.
var demoMoment = time.Date(2026, 11, 20, 10, 4, 0, 0, time.FixedZone("UTC-8", -8*60*60))

func demoModel(t *testing.T, width, height int) Model {
	t.Helper()
	tokens := theme.Fixed(true, false, false)
	now := func() time.Time { return demoMoment }
	return start(t, demo.App{Now: now, Location: demoMoment.Location()}, Config{
		Theme:       &tokens,
		Themes:      func(mode theme.Mode) theme.Theme { return theme.Fixed(true, mode == theme.Dark, false) },
		Multiplayer: &demo.Boards{Now: now},
		Notice:      demo.Notice,
	}, width, height)
}

// demoStep presses one step's keys. A confirmation line from an earlier step
// has faded by then, as it has after the seconds the story holds a frame.
func demoStep(model Model, keys []string) Model {
	faded, _ := model.Update(flashExpired{id: model.board.flashID})
	return press(faded.(Model), keys...)
}

// The demo's data must drive every view without an error state, at the
// documented widths, and carry its label in the header throughout.
func TestDemoStoryRendersEveryStep(t *testing.T) {
	t.Parallel()
	for _, width := range []int{48, 88, 120} {
		model := demoModel(t, width, 34)
		for step, scene := range demoStory {
			model = demoStep(model, scene.keys)
			frame := model.View()
			for _, unwanted := range []string{"could not", "Could not", "failed", "Sign in", "not scanned"} {
				if containsText(frame, unwanted) {
					t.Fatalf("width %d step %d (%s) shows %q:\n%s", width, step, scene.caption, unwanted, frame)
				}
			}
			if !containsText(frame, demo.Notice) {
				t.Fatalf("width %d step %d (%s) does not carry the demo label", width, step, scene.caption)
			}
		}
	}
}

// An action the demo cannot perform is declined in a line, not reported as
// a failure with a retry.
func TestDemoDeclinesWhatNeedsAnAccount(t *testing.T) {
	t.Parallel()
	for name, keys := range map[string][]string{
		"create": {"2", "c", "crew", "enter"},
		"join":   {"2", "i", "CODE", "enter"},
		"invite": {"2", "y"},
	} {
		frame := press(demoModel(t, 120, 30), keys...).View()
		if !containsText(frame, "read-only") || containsText(frame, "Could not") || containsText(frame, "retry") {
			t.Errorf("%s:\n%s", name, frame)
		}
	}
}

func containsText(frame, text string) bool {
	for index := 0; index+len(text) <= len(frame); index++ {
		if frame[index:index+len(text)] == text {
			return true
		}
	}
	return false
}

// TestWriteDemoStory writes the story's painted frames, with each frame's
// caption and hold time beside it, for rendering into the README's
// animation. It runs only when TOKENHOLIC_DEMO_STORY names a directory.
func TestWriteDemoStory(t *testing.T) {
	directory := os.Getenv("TOKENHOLIC_DEMO_STORY")
	if directory == "" {
		t.Skip("set TOKENHOLIC_DEMO_STORY to a directory to write the demo's frames")
	}
	model := demoModel(t, 120, 26)
	for step, scene := range demoStory {
		model = demoStep(model, scene.keys)
		name := filepath.Join(directory, fmt.Sprintf("%02d", step))
		if err := os.WriteFile(name+".ansi", []byte(model.View()+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name+".txt", []byte(fmt.Sprintf("%d\n%s\n", scene.hold, scene.caption)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
