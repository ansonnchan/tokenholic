package theme

import "github.com/charmbracelet/lipgloss"

// The palette. Each role has a value for dark and for light terminal
// backgrounds, and each of those degrades from 24-bit to 256 and 16 colors.
// The TUI never paints the viewport, so every foreground here must stay
// readable on a background it does not control. docs/design.md records the
// same values for the website and share cards.
var (
	accent     = tone("#B4502F", "166", "1", "#D9805F", "209", "9")
	onAccent   = tone("#FFFFFF", "231", "15", "#1B1210", "233", "0")
	muted      = tone("#6A6F78", "242", "8", "#8D929B", "245", "8")
	border     = tone("#B4B8C0", "250", "7", "#50555E", "240", "8")
	currentRow = tone("#F8E6DE", "224", "7", "#33231D", "236", "0")

	gold   = tone("#8A6510", "136", "3", "#D9B45B", "179", "11")
	silver = tone("#646B76", "243", "8", "#BCC2CB", "250", "7")
	bronze = tone("#93532A", "130", "3", "#C08253", "173", "3")

	up      = tone("#1F7A68", "30", "6", "#62B8A5", "73", "14")
	down    = tone("#B3362E", "124", "1", "#E36A62", "167", "9")
	warning = tone("#8A5F00", "94", "3", "#D9A64E", "179", "11")
	failure = tone("#A82A22", "124", "1", "#D95C53", "167", "9")
)

func tone(light, light256, light16, dark, dark256, dark16 string) lipgloss.CompleteAdaptiveColor {
	return lipgloss.CompleteAdaptiveColor{
		Light: lipgloss.CompleteColor{TrueColor: light, ANSI256: light256, ANSI: light16},
		Dark:  lipgloss.CompleteColor{TrueColor: dark, ANSI256: dark256, ANSI: dark16},
	}
}
