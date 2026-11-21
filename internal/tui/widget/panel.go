package widget

import (
	"strings"

	"github.com/ansonnchan/tokenholic/internal/tui/theme"
)

// panelChrome is the cells a panel adds to each side of its content: one
// border and one space.
const panelChrome = 2

// InnerWidth is the content width of a panel that is width cells wide.
func InnerWidth(width int) int {
	return max(width-2*panelChrome, 0)
}

// Panel frames lines in a rounded border with an uppercase title set into
// the top edge. width is the full outer width; lines should already fit
// InnerWidth(width) and are padded, never cut, so a caller's overflow shows
// up in a snapshot instead of silently losing text.
func Panel(t theme.Theme, title string, width int, lines []string) []string {
	g := t.Glyphs
	inner := InnerWidth(width)
	span := inner + 2 // cells between the two corners

	top := t.Border.Render(g.TopLeft + strings.Repeat(g.Horizontal, span) + g.TopRight)
	if title = Truncate(t, strings.ToUpper(title), span-4); title != "" {
		fill := strings.Repeat(g.Horizontal, max(span-3-Width(title), 0))
		top = t.Border.Render(g.TopLeft+g.Horizontal+" ") + t.Strong.Render(title) + t.Border.Render(" "+fill+g.TopRight)
	}

	side := t.Border.Render(g.Vertical)
	framed := make([]string, 0, len(lines)+2)
	framed = append(framed, top)
	for _, line := range lines {
		framed = append(framed, side+" "+PadRight(line, inner)+" "+side)
	}
	return append(framed, t.Border.Render(g.BottomLeft+strings.Repeat(g.Horizontal, span)+g.BottomRight))
}

// Rule renders a section label followed by a dashed rule that fills width,
// with an optional right-hand note such as the trend comparison.
func Rule(t theme.Theme, label, note string, width int) string {
	label = strings.ToUpper(label)
	note = strings.ToUpper(note)
	fill := width - Width(label)
	if label != "" {
		fill--
	}
	if note != "" {
		fill -= Width(note) + 2
	}
	if fill < 3 {
		// No room for a rule worth drawing: keep the words.
		if note == "" || Width(label)+2+Width(note) > width {
			return t.Strong.Render(Truncate(t, label, width))
		}
		return t.Strong.Render(label) + strings.Repeat(" ", width-Width(label)-Width(note)) + t.Muted.Render(note)
	}
	var line strings.Builder
	if label != "" {
		line.WriteString(t.Strong.Render(label) + " ")
	}
	line.WriteString(t.Border.Render(dashes(t, fill)))
	if note != "" {
		line.WriteString("  " + t.Muted.Render(note))
	}
	return line.String()
}

// dashes fills width cells with dash segments one space apart.
func dashes(t theme.Theme, width int) string {
	var rule strings.Builder
	for used := 0; used+Width(t.Glyphs.Dash) <= width; used += Width(t.Glyphs.Dash) + 1 {
		if used > 0 {
			rule.WriteByte(' ')
		}
		rule.WriteString(t.Glyphs.Dash)
	}
	return PadRight(rule.String(), width)
}
