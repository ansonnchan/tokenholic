// Package theme holds the TUI's design tokens: the palette, the text styles
// built from it, and the glyph set with its ASCII fallbacks. It decides how
// things look and nothing about what is shown.
package theme

import (
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Mode names the look a theme has: its palette column and the canvas it
// paints behind everything. The TUI looks the same in every terminal, so
// the mode is the user's choice and is never read from the terminal.
type Mode int

const (
	// Light is the default.
	Light Mode = iota
	Dark
)

// String is the name a mode is saved and shown under.
func (m Mode) String() string {
	if m == Dark {
		return "dark"
	}
	return "light"
}

// Other returns the mode a switch moves to.
func (m Mode) Other() Mode {
	if m == Dark {
		return Light
	}
	return Dark
}

// ParseMode reads a saved or configured mode name. Anything but "light" or
// "dark", in any letter case, is not a mode.
func ParseMode(name string) (Mode, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "dark":
		return Dark, true
	case "light":
		return Light, true
	}
	return Light, false
}

// Options selects the environment-dependent parts of a theme.
type Options struct {
	// NoColor disables every escape sequence, as NO_COLOR requires. Selection
	// and status then rely on brackets and words.
	NoColor bool
	// ASCII replaces every glyph outside ASCII with its fallback.
	ASCII bool
	// Mode is the look to use.
	Mode Mode
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
	// Mode is the look this theme was built for.
	Mode Mode
	// canvas is the escape sequence that selects the canvas background and
	// foreground. It is empty when the output takes no styling.
	canvas string

	// Text is the canvas foreground; Strong is the same in bold.
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

// New builds a theme for output. Color depth is detected from output; the
// mode is whatever the caller chose.
func New(output io.Writer, options Options) Theme {
	renderer := lipgloss.NewRenderer(output)
	if options.NoColor {
		renderer.SetColorProfile(termenv.Ascii)
	}
	renderer.SetHasDarkBackground(options.Mode == Dark)
	return build(renderer, options.ASCII)
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
	mode := Light
	if renderer.HasDarkBackground() {
		mode = Dark
	}
	color := renderer.ColorProfile() != termenv.Ascii
	return Theme{
		Color:      color,
		ASCII:      ascii,
		Glyphs:     glyphs,
		Mode:       mode,
		canvas:     canvasSequence(renderer, color),
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

// reset is how Lip Gloss ends every styled run.
const reset = "\x1b[0m"

// canvasSequence is the escape sequence that selects the canvas colors, cut
// from a rendered sample so it follows the output's color depth.
func canvasSequence(renderer *lipgloss.Renderer, color bool) string {
	if !color {
		return ""
	}
	const sample = "\x00"
	open, _, _ := strings.Cut(renderer.NewStyle().Foreground(onCanvas).Background(canvas).Render(sample), sample)
	return open
}

// Paint lays a frame on the theme's canvas, so the TUI looks the same
// whatever colors the terminal itself has. Every cell of a width by height
// area gets the canvas background, and text with no color of its own gets
// the canvas foreground. Lines keep their text; shorter ones are padded and
// missing ones are added. A theme with no color paints nothing and returns
// the lines as they are, leaving the terminal's own colors alone.
func (t Theme) Paint(lines []string, width, height int) []string {
	if t.canvas == "" {
		return lines
	}
	painted := make([]string, 0, max(len(lines), height))
	for _, line := range lines {
		// A styled run ends by resetting every attribute, which would drop
		// the canvas for the rest of the line. Select it again each time.
		padding := strings.Repeat(" ", max(width-lipgloss.Width(line), 0))
		painted = append(painted, t.canvas+strings.ReplaceAll(line, reset, reset+t.canvas)+padding+reset)
	}
	blank := t.canvas + strings.Repeat(" ", max(width, 0)) + reset
	for len(painted) < height {
		painted = append(painted, blank)
	}
	return painted
}
