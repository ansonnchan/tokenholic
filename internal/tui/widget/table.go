package widget

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/ansonnchan/tokenholic/internal/tui/theme"
)

// Align places a cell's text within its column.
type Align int

const (
	// Left is the default, for names and labels.
	Left Align = iota
	// Right is for numbers, so digits line up.
	Right
)

// Column describes one table column. A Width of zero makes the column
// flexible: flexible columns share whatever the fixed ones leave.
type Column struct {
	Title string
	Align Align
	Width int
}

// Cell is plain text with an optional style. A nil style is ordinary text.
type Cell struct {
	Text  string
	Style *lipgloss.Style
}

// Row is one table line. Current marks the viewer's own row: it gets the
// accent edge, accent text, and a tinted background, and keeps the edge
// glyph when there is no color.
type Row struct {
	Cells   []Cell
	Current bool
}

// Table is a set of aligned rows under optional uppercase headers.
type Table struct {
	Columns []Column
	Rows    []Row
	// Gap is the space between columns; zero means two cells.
	Gap int
}

// edgeWidth is the cells reserved before the first column for the current
// row's edge marker.
const edgeWidth = 2

// Render lays the table out in width cells and returns its lines. Text that
// does not fit its column is truncated with an ellipsis.
func (table Table) Render(t theme.Theme, width int) []string {
	gap := table.Gap
	if gap <= 0 {
		gap = 2
	}
	widths := table.widths(width-edgeWidth, gap)
	separator := strings.Repeat(" ", gap)

	var lines []string
	if table.hasTitles() {
		cells := make([]string, len(table.Columns))
		for index, column := range table.Columns {
			cells[index] = t.Muted.Render(place(Truncate(t, strings.ToUpper(column.Title), widths[index]), widths[index], column.Align))
		}
		lines = append(lines, strings.Repeat(" ", edgeWidth)+strings.Join(cells, separator))
	}
	for _, row := range table.Rows {
		edge := strings.Repeat(" ", edgeWidth)
		gapText := separator
		if row.Current {
			edge = t.CurrentRow.Render(t.Glyphs.Edge + " ")
			gapText = t.CurrentRow.Render(separator)
		}
		cells := make([]string, len(table.Columns))
		for index, column := range table.Columns {
			var cell Cell
			if index < len(row.Cells) {
				cell = row.Cells[index]
			}
			text := place(Truncate(t, cell.Text, widths[index]), widths[index], column.Align)
			switch {
			case row.Current && cell.Style != nil:
				// Keep the cell's own color but sit on the row's tint.
				text = cell.Style.Background(t.CurrentRow.GetBackground()).Render(text)
			case row.Current:
				text = t.CurrentRow.Render(text)
			case cell.Style != nil:
				text = cell.Style.Render(text)
			}
			cells[index] = text
		}
		lines = append(lines, edge+strings.Join(cells, gapText))
	}
	return lines
}

func (table Table) hasTitles() bool {
	for _, column := range table.Columns {
		if column.Title != "" {
			return true
		}
	}
	return false
}

// widths resolves flexible columns against the available cells.
func (table Table) widths(available, gap int) []int {
	widths := make([]int, len(table.Columns))
	flexible := 0
	remaining := available - gap*max(len(table.Columns)-1, 0)
	for index, column := range table.Columns {
		widths[index] = column.Width
		if column.Width == 0 {
			flexible++
		}
		remaining -= column.Width
	}
	for index, column := range table.Columns {
		if column.Width != 0 {
			continue
		}
		share := max(remaining/flexible, 1)
		widths[index] = share
		remaining -= share
		flexible--
	}
	return widths
}

func place(text string, width int, align Align) string {
	if align == Right {
		return PadLeft(text, width)
	}
	return PadRight(text, width)
}
