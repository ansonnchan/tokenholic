package widget

import (
	"strings"

	"github.com/ansonnchan/tokenholic/internal/tui/theme"
)

// Chip renders one label of a chip row. The selected chip is a solid accent
// block; without color it is bracketed instead, in the same cells, so
// selection never depends on color and the row keeps its width.
func Chip(t theme.Theme, label string, selected bool) string {
	switch {
	case selected && t.Color:
		return t.Chip.Render(" " + label + " ")
	case selected:
		return "[" + label + "]"
	default:
		return t.Muted.Render(" " + label + " ")
	}
}

// Chips renders a row of labels with one selected. A negative selected index
// selects none. When the row is wider than width it wraps between chips.
func Chips(t theme.Theme, labels []string, selected, width int) []string {
	lines := []string{""}
	for index, label := range labels {
		chip := Chip(t, label, index == selected)
		last := len(lines) - 1
		if lines[last] != "" && Width(lines[last])+Width(chip) > width {
			lines = append(lines, chip)
			continue
		}
		lines[last] += chip
	}
	return lines
}

// Action renders a primary action with its key, such as [ c  CLAIM YOUR SPOT ].
// Show at most one per panel.
func Action(t theme.Theme, key, label string) string {
	text := key + "  " + strings.ToUpper(label)
	if t.Color {
		return t.Chip.Render("  " + text + "  ")
	}
	return "[ " + text + " ]"
}

// Label renders an uppercase field label in the muted tone.
func Label(t theme.Theme, text string) string {
	return t.Muted.Render(strings.ToUpper(text))
}
