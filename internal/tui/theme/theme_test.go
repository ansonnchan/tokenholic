package theme

import (
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestNoColorEmitsNoEscapeSequences(t *testing.T) {
	t.Parallel()
	for name, tokens := range map[string]Theme{
		"fixed plain":   Fixed(false, true, false),
		"NO_COLOR":      New(&bytes.Buffer{}, Options{NoColor: true}),
		"not a console": New(&bytes.Buffer{}, Options{}),
	} {
		if tokens.Color {
			t.Errorf("%s: Color = true", name)
		}
		for _, style := range []lipgloss.Style{tokens.Strong, tokens.Accent, tokens.Chip, tokens.CurrentRow, tokens.Failure} {
			if got := style.Render("x"); got != "x" {
				t.Errorf("%s: styled text = %q, want plain", name, got)
			}
		}
	}
}

func TestModeChoosesThePaletteWithoutAskingTheTerminal(t *testing.T) {
	t.Parallel()
	// Nothing here is a terminal, so nothing could be detected: the mode
	// alone decides. The zero value is light.
	for mode, dark := range map[Mode]bool{Dark: true, Light: false} {
		renderer := lipgloss.NewRenderer(io.Discard)
		renderer.SetColorProfile(termenv.TrueColor)
		renderer.SetHasDarkBackground(mode == Dark)
		built := build(renderer, false)
		if got, want := built.Accent.Render("x"), Fixed(true, dark, false).Accent.Render("x"); got != want {
			t.Errorf("%s: accent = %q, want %q", mode, got, want)
		}
		if built.Mode != mode {
			t.Errorf("%s: theme reports mode %s", mode, built.Mode)
		}
	}
	if (Options{}).Mode != Light {
		t.Error("the default mode is not light")
	}
}

func TestModeNamesRoundTrip(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		mode  Mode
		valid bool
	}{
		{"dark", Dark, true}, {"light", Light, true}, {" Light ", Light, true}, {"DARK", Dark, true},
		{"", Light, false}, {"auto", Light, false}, {"terminal", Light, false},
	} {
		if mode, ok := ParseMode(test.name); mode != test.mode || ok != test.valid {
			t.Errorf("ParseMode(%q) = %s, %t", test.name, mode, ok)
		}
	}
	for _, mode := range []Mode{Dark, Light} {
		if parsed, ok := ParseMode(mode.String()); !ok || parsed != mode {
			t.Errorf("%s does not parse back", mode)
		}
		if mode.Other() == mode || mode.Other().Other() != mode {
			t.Errorf("%s does not switch and switch back", mode)
		}
	}
}

func TestPaintCoversEveryCellOfTheCanvas(t *testing.T) {
	t.Parallel()
	for _, dark := range []bool{true, false} {
		tokens := Fixed(true, dark, false)
		background := "48;2;11;17;32"
		if !dark {
			background = "48;2;250;247;242"
		}
		lines := []string{
			"plain " + tokens.Accent.Render("accent") + " after",
			tokens.Chip.Render(" chip ") + tokens.Muted.Render("muted"),
			"",
		}
		painted := tokens.Paint(lines, 30, 5)
		if len(painted) != 5 {
			t.Fatalf("painted %d lines, want the full height of 5", len(painted))
		}
		for index, line := range painted {
			if lipgloss.Width(line) != 30 {
				t.Errorf("line %d is %d cells wide, want 30", index, lipgloss.Width(line))
			}
			if !strings.HasPrefix(line, tokens.canvas) || !strings.HasSuffix(line, reset) {
				t.Errorf("line %d does not start on the canvas and end reset: %q", index, line)
			}
			// Wherever a styled run ended, the canvas is selected again, so no
			// cell falls back to the terminal's own colors.
			for _, piece := range strings.Split(strings.TrimSuffix(line, reset), reset)[1:] {
				if !strings.HasPrefix(piece, tokens.canvas) {
					t.Errorf("line %d leaves the canvas after a reset: %q", index, line)
				}
			}
		}
		if !strings.Contains(tokens.canvas, background) {
			t.Errorf("canvas sequence %q does not select %s", tokens.canvas, background)
		}
		// The text is untouched.
		if !strings.Contains(painted[0], "plain ") || !strings.Contains(painted[0], "accent") || !strings.Contains(painted[0], " after") {
			t.Errorf("painting changed the text: %q", painted[0])
		}
	}
}

func TestPaintLeavesAnUncoloredTerminalAlone(t *testing.T) {
	t.Parallel()
	lines := []string{"one", "two"}
	for name, tokens := range map[string]Theme{
		"plain": Fixed(false, true, false), "NO_COLOR": New(&bytes.Buffer{}, Options{NoColor: true}),
	} {
		painted := tokens.Paint(lines, 40, 10)
		if len(painted) != 2 || painted[0] != "one" || painted[1] != "two" {
			t.Errorf("%s: Paint changed an uncolored frame: %q", name, painted)
		}
	}
}

// Text sits on a canvas the TUI paints, so its contrast is something this
// package controls and can promise.
func TestTextMeetsAAContrastOnItsCanvas(t *testing.T) {
	t.Parallel()
	const minimum = 4.5
	roles := map[string]lipgloss.CompleteAdaptiveColor{
		"text": onCanvas, "accent": accent, "muted": muted, "gold": gold, "silver": silver, "bronze": bronze,
		"up": up, "down": down, "warning": warning, "failure": failure,
	}
	for name, role := range roles {
		if ratio := contrast(role.Dark.TrueColor, canvas.Dark.TrueColor); ratio < minimum {
			t.Errorf("dark %s is %.2f:1 on the canvas, below %.1f:1", name, ratio, minimum)
		}
		if ratio := contrast(role.Light.TrueColor, canvas.Light.TrueColor); ratio < minimum {
			t.Errorf("light %s is %.2f:1 on the canvas, below %.1f:1", name, ratio, minimum)
		}
	}
	// Text on a solid chip.
	if ratio := contrast(onAccent.Dark.TrueColor, accent.Dark.TrueColor); ratio < minimum {
		t.Errorf("dark chip text is %.2f:1", ratio)
	}
	if ratio := contrast(onAccent.Light.TrueColor, accent.Light.TrueColor); ratio < minimum {
		t.Errorf("light chip text is %.2f:1", ratio)
	}
}

func TestColorFollowsTheBackground(t *testing.T) {
	t.Parallel()
	dark, light := Fixed(true, true, false), Fixed(true, false, false)
	if !dark.Color || !light.Color {
		t.Fatal("fixed color theme reports no color")
	}
	if got := dark.Accent.Render("x"); got != "\x1b[1;38;2;217;128;95mx\x1b[0m" {
		t.Errorf("dark accent = %q", got)
	}
	if dark.Accent.Render("x") == light.Accent.Render("x") {
		t.Error("accent is the same on light and dark backgrounds")
	}
	if !strings.Contains(dark.Chip.Render("x"), "48;2;") {
		t.Errorf("chip has no background: %q", dark.Chip.Render("x"))
	}
}

// Every glyph needs an ASCII fallback, and a fallback that is missing or
// still outside ASCII would only show up on a terminal that cannot draw it.
func TestEveryGlyphHasAnASCIIFallback(t *testing.T) {
	t.Parallel()
	unicode, ascii := reflect.ValueOf(Fixed(false, true, false).Glyphs), reflect.ValueOf(Fixed(false, true, true).Glyphs)
	for index := range unicode.NumField() {
		name := unicode.Type().Field(index).Name
		for _, glyph := range glyphStrings(ascii.Field(index)) {
			if name != "Gap" && strings.TrimSpace(glyph) == "" {
				t.Errorf("ASCII %s is blank", name)
			}
			for _, character := range glyph {
				if character < 0x20 || character > 0x7e {
					t.Errorf("ASCII %s contains %q", name, character)
				}
			}
		}
		if len(glyphStrings(unicode.Field(index))) == 0 || len(glyphStrings(ascii.Field(index))) == 0 {
			t.Errorf("%s is empty in one glyph set", name)
		}
	}
	if !Fixed(false, true, true).ASCII || Fixed(false, true, false).ASCII {
		t.Error("ASCII flag does not match the glyph set")
	}
}

func glyphStrings(value reflect.Value) []string {
	if value.Kind() == reflect.String {
		return []string{value.String()}
	}
	glyphs := make([]string, value.Len())
	for index := range glyphs {
		glyphs[index] = value.Index(index).String()
	}
	return glyphs
}
