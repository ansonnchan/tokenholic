package tui

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/ansonnchan/tokenholic/internal/tui/theme"
)

// styleSet is the subset of theme tokens the current text views use.
type styleSet struct {
	title    lipgloss.Style
	selected lipgloss.Style
	muted    lipgloss.Style
	success  lipgloss.Style
	warning  lipgloss.Style
	failure  lipgloss.Style
}

func newStyles(t theme.Theme) styleSet {
	return styleSet{
		title:    t.Strong,
		selected: t.Accent,
		muted:    t.Muted,
		success:  t.Up,
		warning:  t.Warning,
		failure:  t.Failure,
	}
}
