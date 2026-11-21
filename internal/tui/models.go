package tui

import (
	"strconv"

	"github.com/charmbracelet/lipgloss"

	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/store"
	"github.com/ansonnchan/tokenholic/internal/tui/theme"
	"github.com/ansonnchan/tokenholic/internal/tui/widget"
)

// modelsView lists every model in the period with a cursor, beside the
// selected model's attribution, estimate coverage, and exact categories.
func (m Model) modelsView(width, height int) []string {
	t := m.theme
	summary := summaryFor(m.dashboard, m.period)
	models := sortedModels(summary.Models)
	cursor := min(m.modelCursor, max(len(models)-1, 0))

	wide := width >= wideWidth
	listWidth, detailWidth := width, width
	if wide {
		detailWidth = railWidth
		listWidth = width - railWidth - 1
	}
	inner := widget.InnerWidth(listWidth)
	lines := append(periodChips(t, m.period, inner), "")
	if len(models) == 0 {
		lines = append(lines, t.Muted.Render("No model usage recorded in this period."))
		return widget.Panel(t, "models", listWidth, lines)
	}

	// Rows that fit: the list scrolls around the cursor instead of the page.
	// Stacked in a short terminal, the detail gives up its categories before
	// the list gives up rows.
	const listChrome = 4 // frame, header row, position line
	detailInner := widget.InnerWidth(detailWidth)
	detail := modelDetail(t, models[cursor], detailInner, true)
	capacity := len(models)
	if height > 0 {
		capacity = height - len(lines) - listChrome
		if !wide {
			if capacity-len(detail)-2 < min(len(models), 5) {
				detail = modelDetail(t, models[cursor], detailInner, false)
			}
			capacity -= len(detail) + 2
		}
		capacity = min(max(capacity, 3), len(models))
	}
	start, end := window(cursor, len(models), capacity)

	columns := usageColumns("model")
	attribution := inner >= 60
	if attribution {
		columns = append(columns[:1], append([]widget.Column{{Title: "attribution", Width: 11}}, columns[1:]...)...)
	}
	table := widget.Table{Columns: columns}
	total := summary.Tokens.Total()
	for index := start; index < end; index++ {
		model := models[index]
		row := usageRow(t, modelName(model), model.Tokens, model.APIEstimate, total)
		if attribution {
			cell := widget.Cell{Text: attributionText(model.Resolution), Style: attributionStyle(t, model.Resolution)}
			row.Cells = append(row.Cells[:1], append([]widget.Cell{cell}, row.Cells[1:]...)...)
		}
		row.Cursor = index == cursor
		table.Rows = append(table.Rows, row)
	}
	lines = append(lines, table.Render(t, inner)...)
	if capacity < len(models) {
		lines = append(lines, t.Muted.Render(positionText(t, start, end, len(models))))
	}

	list := widget.Panel(t, "models", listWidth, lines)
	if wide {
		return widget.Columns(1, list, widget.Panel(t, "model detail", detailWidth, detail))
	}
	return append(list, widget.Panel(t, "model detail", detailWidth, detail)...)
}

// modelDetail describes one model: what the source called it, how it was
// attributed, how much of it is priced, and, with categories, its exact
// token counts.
func modelDetail(t theme.Theme, model store.ModelBreakdown, width int, categories bool) []string {
	g := t.Glyphs
	lines := []string{t.Strong.Render(widget.Truncate(t, modelName(model), width))}
	field := func(label, value string, style *lipgloss.Style) {
		const labelWidth = 13
		for index, line := range widget.Wrap(value, max(width-labelWidth, 8)) {
			if style != nil {
				line = style.Render(line)
			}
			if index == 0 {
				lines = append(lines, widget.PadRight(widget.Label(t, label), labelWidth)+line)
				continue
			}
			lines = append(lines, widget.PadRight("", labelWidth)+line)
		}
	}
	if observed := model.ObservedModelID; observed != "" && observed != modelName(model) {
		field("observed", observed, nil)
	}
	field("attribution", attributionText(model.Resolution), attributionStyle(t, model.Resolution))
	field("tokens", tokensText(g, model.Tokens.Total()), nil)
	estimate := estimateText(g, model.APIEstimate)
	if model.APIEstimate.Value != nil && model.Coverage.PricedBasisPoints != nil {
		estimate += " " + g.Separator + " " + pricedText(g, model.Coverage) + " priced"
	}
	field("$ est", estimate, nil)
	if reason := unpricedReasonText(model.UnpricedReason); reason != "" {
		label := "no estimate"
		if model.APIEstimate.Value != nil {
			// Some of the model's usage priced; the reason covers the rest.
			label = "unpriced"
		}
		field(label, reason, &t.Muted)
	}
	if !categories {
		return lines
	}
	lines = append(lines, "")
	return append(lines, categoryLines(t, model.Tokens, width)...)
}

// attributionStyle mutes a clean attribution and flags an inferred or
// unknown one; the word itself always says which it is.
func attributionStyle(t theme.Theme, resolution normalize.ModelResolution) *lipgloss.Style {
	switch resolution {
	case normalize.ModelReported, normalize.ModelResolved:
		return &t.Muted
	default:
		return &t.Warning
	}
}

// window returns the rows to show so the cursor stays visible, keeping it
// away from the edge while there is more to see.
func window(cursor, total, capacity int) (int, int) {
	if capacity >= total {
		return 0, total
	}
	start := cursor - capacity/2
	start = max(0, min(start, total-capacity))
	return start, start + capacity
}

// positionText is the "1–8 OF 23" line under a windowed list.
func positionText(t theme.Theme, start, end, total int) string {
	return strconv.Itoa(start+1) + t.Glyphs.Range + strconv.Itoa(end) + " OF " + strconv.Itoa(total)
}
