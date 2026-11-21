package theme

// Glyphs is every character the TUI draws that carries meaning or structure.
// Each has an ASCII form of the same cell width, so a layout measured with
// one set fits the other, except where the comment says otherwise.
type Glyphs struct {
	// Panel frame corners and edges.
	TopLeft, TopRight, BottomLeft, BottomRight string
	Horizontal, Vertical                       string
	// Dash is one segment of a dashed rule; segments are joined by spaces.
	Dash string

	// Up, Down, and Same mark rank movement; Up and Down precede a count.
	Up, Down, Same string
	// New marks a member with no usage in the comparison window.
	New string
	// Edge and Star mark the current user's row.
	Edge, Star string
	// Pointer marks the row the cursor is on.
	Pointer string

	// AtLeast prefixes a lower bound. The ASCII form is two cells wide.
	AtLeast string
	// Unavailable stands for a value that is not known. It is never "0".
	Unavailable string
	// Separator joins short phrases on one line.
	Separator string
	// Range joins the ends of a numeric range.
	Range string
	// Ellipsis ends truncated text.
	Ellipsis string
	// Check and Cross mark a finished or failed step. The ASCII check is two
	// cells wide.
	Check, Cross string
	// Bars are sparkline levels from lowest to highest.
	Bars []string
	// Gap is a sparkline cell with no known value, distinct from the lowest bar.
	Gap string
	// Spinner frames, shown only while work is running.
	Spinner []string
}

var unicodeGlyphs = Glyphs{
	TopLeft: "╭", TopRight: "╮", BottomLeft: "╰", BottomRight: "╯",
	Horizontal: "─", Vertical: "│", Dash: "-",
	Up: "▲", Down: "▼", Same: "–", New: "NEW",
	Edge: "▌", Star: "★", Pointer: "▸",
	AtLeast: "≥", Unavailable: "—", Separator: "·", Range: "–", Ellipsis: "…",
	Check: "✓", Cross: "✗",
	Bars:    []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"},
	Gap:     " ",
	Spinner: []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
}

var asciiGlyphs = Glyphs{
	TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+",
	Horizontal: "-", Vertical: "|", Dash: "-",
	Up: "+", Down: "-", Same: "=", New: "new",
	Edge: "|", Star: "*", Pointer: ">",
	AtLeast: ">=", Unavailable: "-", Separator: "|", Range: "-", Ellipsis: "~",
	Check: "ok", Cross: "x",
	Bars:    []string{"_", ".", "-", "=", "#"},
	Gap:     " ",
	Spinner: []string{"|", "/", "-", "\\"},
}
