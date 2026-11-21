package widget

import (
	"math"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/ansonnchan/tokenholic/internal/tui/theme"
)

// Wordmark is the one-line mark: an accent caret and the name in bold.
func Wordmark(t theme.Theme) string {
	return t.Accent.Render(">_") + " " + t.Strong.Render("TOKENHOLIC")
}

// blockCaret and blockLetters spell ">_ TOKENHOLIC" three rows high in
// half-block characters, sharing one baseline.
var (
	blockCaret = []string{
		"▀▄     ",
		" ▄▀    ",
		"▀   ▀▀▀",
	}
	blockLetters = []string{
		"▀█▀ █▀█ █ █ █▀▀ █▄ █ █ █ █▀█ █   █ █▀▀",
		" █  █ █ █▀▄ █▀▀ █ ▀█ █▀█ █ █ █   █ █  ",
		" ▀  ▀▀▀ ▀ ▀ ▀▀▀ ▀  ▀ ▀ ▀ ▀▀▀ ▀▀▀ ▀ ▀▀▀",
	}
)

// BlockWordmark is the three-line mark for wide HOME views. It reports false
// when it does not fit width or the glyph set cannot draw it; the caller
// then uses Wordmark.
func BlockWordmark(t theme.Theme, width int) ([]string, bool) {
	if t.ASCII || Width(blockCaret[0])+2+Width(blockLetters[0]) > width {
		return nil, false
	}
	lines := make([]string, len(blockLetters))
	for index, letters := range blockLetters {
		lines[index] = t.Accent.Render(blockCaret[index]) + "  " + t.Strong.Render(letters)
	}
	return lines, true
}

// Movement renders a rank change against the board's comparison window:
// a climb, a fall, no change, or a new entrant. A nil change that is not new
// has nothing to compare and renders empty, never as "no change".
func Movement(t theme.Theme, change *int, isNew bool) string {
	text, style := MovementText(t, change, isNew)
	if style == nil {
		return text
	}
	return style.Render(text)
}

// MovementText is Movement as plain text plus its style, for table cells.
func MovementText(t theme.Theme, change *int, isNew bool) (string, *lipgloss.Style) {
	g := t.Glyphs
	// The ASCII marks are signs, read as part of the number: +2, -1.
	join := " "
	if t.ASCII {
		join = ""
	}
	switch {
	case isNew:
		return g.New, &t.Accent
	case change == nil:
		return "", nil
	case *change > 0:
		return g.Up + join + strconv.Itoa(*change), &t.Up
	case *change < 0:
		return g.Down + join + strconv.Itoa(-*change), &t.Down
	default:
		return g.Same, &t.Muted
	}
}

// RankStyle is the medal tone for the top three ranks and nil below them.
// The number itself carries the meaning; the tone only decorates it.
func RankStyle(t theme.Theme, rank int) *lipgloss.Style {
	switch rank {
	case 1:
		return &t.Gold
	case 2:
		return &t.Silver
	case 3:
		return &t.Bronze
	default:
		return nil
	}
}

// Point is one sparkline sample. Known is false for a period with no
// recorded value, which draws as a gap rather than as the lowest bar.
type Point struct {
	Value uint64
	Known bool
}

// Sparkline draws the most recent width points, one cell each, scaled to the
// largest known value. Zero is the lowest bar; any usage is at least one
// level above it so a quiet day is not mistaken for an empty one.
func Sparkline(t theme.Theme, points []Point, width int) string {
	if len(points) > width {
		points = points[len(points)-width:]
	}
	var peak uint64
	for _, point := range points {
		if point.Known {
			peak = max(peak, point.Value)
		}
	}
	bars := t.Glyphs.Bars
	var line strings.Builder
	for _, point := range points {
		switch {
		case !point.Known:
			line.WriteString(t.Glyphs.Gap)
		case point.Value == 0 || peak == 0:
			line.WriteString(bars[0])
		default:
			level := int(math.Ceil(float64(point.Value) / float64(peak) * float64(len(bars)-1)))
			line.WriteString(bars[min(max(level, 1), len(bars)-1)])
		}
	}
	return line.String()
}

// Hint is one key and what it does. Keep marks hints that survive when the
// bar is too narrow for all of them.
type Hint struct {
	Key   string
	Label string
	Keep  bool
}

// KeyBar renders hints on one line. When they do not all fit, hints without
// Keep are dropped from the end first.
func KeyBar(t theme.Theme, hints []Hint, width int) string {
	hints = append([]Hint(nil), hints...)
	separator := " " + t.Glyphs.Separator + " "
	for {
		plain := make([]string, len(hints))
		for index, hint := range hints {
			plain[index] = hint.Key + " " + hint.Label
		}
		if Width(strings.Join(plain, separator)) <= width {
			break
		}
		drop := -1
		for index := len(hints) - 1; index >= 0; index-- {
			if !hints[index].Keep {
				drop = index
				break
			}
		}
		if drop < 0 {
			break
		}
		hints = append(hints[:drop], hints[drop+1:]...)
	}
	parts := make([]string, len(hints))
	for index, hint := range hints {
		parts[index] = t.Text.Render(hint.Key) + " " + t.Muted.Render(hint.Label)
	}
	return strings.Join(parts, t.Border.Render(separator))
}

// Level is the kind of a notice.
type Level int

const (
	// Info is neutral guidance.
	Info Level = iota
	// Success confirms something finished.
	Success
	// Warning is usable content with something to know about it.
	Warning
	// Failure is something that did not work.
	Failure
)

// Notice renders a message under a word that names its level, so the level
// reads without color. Long messages wrap with a hanging indent.
func Notice(t theme.Theme, level Level, message string, width int) []string {
	word, style := "NOTE", t.Muted
	switch level {
	case Success:
		word, style = "DONE", t.Up
	case Warning:
		word, style = "WARNING", t.Warning
	case Failure:
		word, style = "ERROR", t.Failure
	}
	indent := Width(word) + 2
	wrapped := Wrap(message, max(width-indent, 8))
	lines := make([]string, len(wrapped))
	for index, line := range wrapped {
		if index == 0 {
			lines[index] = style.Bold(true).Render(word) + "  " + line
			continue
		}
		lines[index] = strings.Repeat(" ", indent) + line
	}
	return lines
}

// Header renders the top of every view: the wordmark, the area tabs, and a
// right-aligned status such as freshness. Below wideAt cells the tabs move
// to their own line.
func Header(t theme.Theme, tabs []string, selected int, status string, width, wideAt int) []string {
	mark := Wordmark(t)
	status = t.Muted.Render(Truncate(t, status, max(width-Width(mark)-2, 0)))
	if width >= wideAt {
		left := mark + "   " + strings.Join(Chips(t, tabs, selected, width), "")
		if lines := Spread(left, status, width); len(lines) == 1 {
			return lines
		}
	}
	lines := Spread(mark, status, width)
	return append(lines[:1], Chips(t, tabs, selected, width)...)
}
