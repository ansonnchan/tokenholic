// Package widget draws the TUI's building blocks from theme tokens: chips,
// tabs, titled panels, dashed rules, tables, sparklines, movement markers,
// and the key bar. Widgets take plain text and a width and return styled
// lines; they hold no state and know nothing about usage or ranking.
package widget

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/ansonnchan/tokenholic/internal/tui/theme"
)

// Width is the number of terminal cells text occupies, ignoring styling.
func Width(text string) int {
	return lipgloss.Width(text)
}

// Truncate cuts plain text to at most width cells, ending it with the
// theme's ellipsis when anything was removed. Pass text before styling it.
func Truncate(t theme.Theme, text string, width int) string {
	if width <= 0 {
		return ""
	}
	if Width(text) <= width {
		return text
	}
	budget := width - Width(t.Glyphs.Ellipsis)
	if budget <= 0 {
		return t.Glyphs.Ellipsis
	}
	var kept strings.Builder
	used := 0
	for _, character := range text {
		cells := Width(string(character))
		if used+cells > budget {
			break
		}
		kept.WriteRune(character)
		used += cells
	}
	return strings.TrimRight(kept.String(), " ") + t.Glyphs.Ellipsis
}

// PadRight extends text, styled or not, with spaces to width cells.
func PadRight(text string, width int) string {
	if gap := width - Width(text); gap > 0 {
		return text + strings.Repeat(" ", gap)
	}
	return text
}

// PadLeft right-aligns text, styled or not, in width cells.
func PadLeft(text string, width int) string {
	if gap := width - Width(text); gap > 0 {
		return strings.Repeat(" ", gap) + text
	}
	return text
}

// Spread puts left and right at the two ends of a line width cells wide.
// When both do not fit with a gap between them, right moves to its own line,
// so nothing is cut.
func Spread(left, right string, width int) []string {
	if right == "" {
		return []string{left}
	}
	gap := width - Width(left) - Width(right)
	if gap < 2 && left != "" {
		return []string{left, right}
	}
	return []string{left + strings.Repeat(" ", max(gap, 0)) + right}
}

// Wrap breaks plain text at spaces into lines of at most width cells. A word
// longer than a line is split rather than overflowing.
func Wrap(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	var lines []string
	current := ""
	for _, word := range strings.Fields(text) {
		for Width(word) > width {
			if current != "" {
				lines = append(lines, current)
				current = ""
			}
			head := splitAt(word, width)
			lines = append(lines, head)
			word = word[len(head):]
		}
		switch {
		case current == "":
			current = word
		case Width(current)+1+Width(word) <= width:
			current += " " + word
		default:
			lines = append(lines, current)
			current = word
		}
	}
	if current != "" || len(lines) == 0 {
		lines = append(lines, current)
	}
	return lines
}

// splitAt returns the longest prefix of word that fits in width cells.
func splitAt(word string, width int) string {
	used, end := 0, 0
	for index, character := range word {
		cells := Width(string(character))
		if used+cells > width {
			break
		}
		used += cells
		end = index + len(string(character))
	}
	if end == 0 {
		// Always make progress, even for a cell wider than the line.
		for _, character := range word {
			return string(character)
		}
	}
	return word[:end]
}

// Columns sets blocks of lines side by side with gap cells between them.
// Each block is padded to its own widest line so the next one stays aligned.
func Columns(gap int, blocks ...[]string) []string {
	height := 0
	for _, block := range blocks {
		height = max(height, len(block))
	}
	lines := make([]string, height)
	for index, block := range blocks {
		width := 0
		for _, line := range block {
			width = max(width, Width(line))
		}
		last := index == len(blocks)-1
		for row := range lines {
			cell := ""
			if row < len(block) {
				cell = block[row]
			}
			if !last {
				cell = PadRight(cell, width) + strings.Repeat(" ", gap)
			}
			lines[row] += cell
		}
	}
	for row := range lines {
		lines[row] = strings.TrimRight(lines[row], " ")
	}
	return lines
}
