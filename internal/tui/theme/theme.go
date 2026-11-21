// Package theme holds the TUI's design tokens: the palette, the text styles
// built from it, and the glyph set with its ASCII fallbacks. It decides how
// things look and nothing about what is shown.
package theme

import (
	"io"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Background says which palette column a theme uses.
type Background int

const (
	// DetectBackground asks the terminal, and assumes dark when it does not
	// answer. This is the default.
	DetectBackground Background = iota
	// DarkBackground and LightBackground skip detection, for terminals that
	// answer wrongly or not at all.
	DarkBackground
	LightBackground
)

// Options selects the environment-dependent parts of a theme.
type Options struct {
	// NoColor disables every escape sequence, as NO_COLOR requires. Selection
	// and status then rely on brackets and words.
	NoColor bool
	// ASCII replaces every glyph outside ASCII with its fallback.
	ASCII bool
	// Background overrides detection of a light or dark terminal.
	Background Background
}

// Theme is an immutable set of styles and glyphs for one output.
type Theme struct {
	// Color is false when the output takes no styling at all. Components use
	// it to add the brackets that stand in for a solid chip.
	Color bool
	// ASCII is true when Glyphs holds the fallback set, so block-drawn
	// decoration that has no fallback is left out.
	ASCII  bool
	Glyphs Glyphs

	// Text is the terminal's own foreground; Strong is the same in bold.
	Text   lipgloss.Style
	Strong lipgloss.Style
	// Muted is for labels, hints, and secondary values.
	Muted lipgloss.Style
	// Border draws panel frames and dashed rules.
	Border lipgloss.Style
	// Accent is the one coral used for selection, primary actions, the
	// current user, and the wordmark caret.
	Accent lipgloss.Style
	// Chip is a solid accent block with dark text.
	Chip lipgloss.Style
	// CurrentRow tints the current user's row behind accent text.
	CurrentRow lipgloss.Style

	Gold   lipgloss.Style
	Silver lipgloss.Style
	Bronze lipgloss.Style

	Up      lipgloss.Style
	Down    lipgloss.Style
	Warning lipgloss.Style
	Failure lipgloss.Style
}

// New builds a theme for output. Color depth and the light or dark background
// are detected from the output once, here, before any program reads input.
func New(output io.Writer, options Options) Theme {
	renderer := lipgloss.NewRenderer(output)
	configure(renderer, options)
	return build(renderer, options.ASCII)
}

func configure(renderer *lipgloss.Renderer, options Options) {
	switch {
	case options.NoColor:
		renderer.SetColorProfile(termenv.Ascii)
	case options.Background != DetectBackground:
		renderer.SetHasDarkBackground(options.Background == DarkBackground)
	default:
		// Query the background now: asking later would race the key reader.
		renderer.HasDarkBackground()
	}
}

// Fixed builds a theme with nothing detected, for snapshots and previews:
// 24-bit color on the named background, or no styling when color is false.
func Fixed(color, darkBackground, ascii bool) Theme {
	renderer := lipgloss.NewRenderer(io.Discard)
	renderer.SetColorProfile(termenv.Ascii)
	if color {
		renderer.SetColorProfile(termenv.TrueColor)
	}
	renderer.SetHasDarkBackground(darkBackground)
	return build(renderer, ascii)
}

func build(renderer *lipgloss.Renderer, ascii bool) Theme {
	style := renderer.NewStyle
	glyphs := unicodeGlyphs
	if ascii {
		glyphs = asciiGlyphs
	}
	return Theme{
		Color:      renderer.ColorProfile() != termenv.Ascii,
		ASCII:      ascii,
		Glyphs:     glyphs,
		Text:       style(),
		Strong:     style().Bold(true),
		Muted:      style().Foreground(muted),
		Border:     style().Foreground(border),
		Accent:     style().Foreground(accent).Bold(true),
		Chip:       style().Foreground(onAccent).Background(accent).Bold(true),
		CurrentRow: style().Foreground(accent).Background(currentRow).Bold(true),
		Gold:       style().Foreground(gold).Bold(true),
		Silver:     style().Foreground(silver).Bold(true),
		Bronze:     style().Foreground(bronze).Bold(true),
		Up:         style().Foreground(up),
		Down:       style().Foreground(down),
		Warning:    style().Foreground(warning),
		Failure:    style().Foreground(failure),
	}
}
