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

func TestBackgroundOverrideSkipsDetection(t *testing.T) {
	t.Parallel()
	// Nothing here is a terminal, so only the override can choose light.
	for background, dark := range map[Background]bool{DarkBackground: true, LightBackground: false, DetectBackground: true} {
		renderer := lipgloss.NewRenderer(io.Discard)
		renderer.SetColorProfile(termenv.TrueColor)
		configure(renderer, Options{Background: background})
		got, want := build(renderer, false).Accent.Render("x"), Fixed(true, dark, false).Accent.Render("x")
		if got != want {
			t.Errorf("background %d: accent = %q, want %q", background, got, want)
		}
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
