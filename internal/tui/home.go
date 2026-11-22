package tui

import (
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/ansonnchan/tokenholic/internal/application"
	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/store"
	"github.com/ansonnchan/tokenholic/internal/tui/theme"
	"github.com/ansonnchan/tokenholic/internal/tui/widget"
)

const (
	// railWidth is the breakdown column beside the usage panel on wide layouts.
	railWidth = 50
	// homeModelRows is how many models HOME lists before pointing at MODELS.
	homeModelRows = 5
)

// periods are the local calendar windows, in chip order.
var periods = []store.Period{store.PeriodToday, store.PeriodWeek, store.PeriodLast30Days, store.PeriodAllTime}

func periodTitle(period store.Period) string {
	switch period {
	case store.PeriodToday:
		return "TODAY"
	case store.PeriodWeek:
		return "THIS WEEK"
	case store.PeriodLast30Days:
		return "30 DAYS"
	default:
		return "ALL-TIME"
	}
}

// periodChips is the shared period selector of the local views.
func periodChips(t theme.Theme, selected store.Period, width int) []string {
	labels := make([]string, len(periods))
	index := 0
	for position, period := range periods {
		labels[position] = periodTitle(period)
		if period == selected {
			index = position
		}
	}
	return widget.Chips(t, labels, index, width)
}

// summaryFor picks the dashboard's summary for a period.
func summaryFor(dashboard application.Dashboard, period store.Period) store.UsageSummary {
	return dashboard.Summary(period)
}

// homeView is the personal dashboard: headline totals, the daily series, and
// the source and model breakdowns for the selected period.
func (m Model) homeView(width int) []string {
	t := m.theme
	summary := summaryFor(m.dashboard, m.period)
	if noSupportedSources(m.dashboard.AllTime, m.dashboard.Diagnostics.AdapterHealth) {
		return widget.Panel(t, "your usage", width, noSourcesLines(t, widget.InnerWidth(width)))
	}

	mainWidth, sideWidth := width, width
	if width >= wideWidth {
		sideWidth = railWidth
		mainWidth = width - railWidth - 1
	}
	usage := m.usageLines(summary, widget.InnerWidth(mainWidth))
	sources := widget.Panel(t, "by source", sideWidth, sourceTable(t, summary, widget.InnerWidth(sideWidth)))
	models := widget.Panel(t, "by model", sideWidth, modelSummaryTable(t, summary, widget.InnerWidth(sideWidth)))
	if width >= wideWidth {
		// Stretch the usage panel to the rail's height so the two columns
		// end on one line.
		for len(usage)+2 < len(sources)+len(models) {
			usage = append(usage, "")
		}
		return widget.Columns(1, widget.Panel(t, "your usage", mainWidth, usage), append(sources, models...))
	}
	return append(append(widget.Panel(t, "your usage", mainWidth, usage), sources...), models...)
}

// usageLines is the usage panel: period chips, headline tiles, the daily
// sparkline, and exact categories when detail is on.
func (m Model) usageLines(summary store.UsageSummary, width int) []string {
	t := m.theme
	lines := periodChips(t, m.period, width)
	lines = append(lines, "")
	lines = append(lines, headlineTiles(t, summary, width)...)
	if isEmpty(summary) {
		lines = append(lines, "", t.Muted.Render("No usage recorded in this period."))
	}
	lines = append(lines, "")
	lines = append(lines, dailyLines(t, m.dashboard.Daily, width)...)
	if m.detail {
		lines = append(lines, "")
		lines = append(lines, categoryLines(t, summary.Tokens, width)...)
	}
	return lines
}

// headlineTiles shows total tokens, the estimate, and how much of the usage
// the estimate covers. The estimate is always labelled as one.
func headlineTiles(t theme.Theme, summary store.UsageSummary, width int) []string {
	g := t.Glyphs
	tiles := [][2]string{
		{"tokens", tokensText(g, summary.Tokens.Total())},
		{"$ est", estimateText(g, summary.APIEstimate)},
		{"priced", pricedText(g, summary.Coverage)},
	}
	column := max(width/len(tiles), 12)
	if column*len(tiles) > width {
		// Too narrow for three: the priced share joins the estimate's label.
		tiles = tiles[:2]
		if summary.Coverage.PricedBasisPoints != nil {
			tiles[1][0] = "$ est " + g.Separator + " " + pricedText(g, summary.Coverage) + " priced"
		}
		column = width / 2
	}
	var labels, values strings.Builder
	for _, tile := range tiles {
		labels.WriteString(widget.PadRight(widget.Label(t, tile[0]), column))
		style := t.Strong
		if tile[1] == g.Unavailable {
			style = t.Muted
		}
		values.WriteString(widget.PadRight(style.Render(tile[1]), column))
	}
	return []string{strings.TrimRight(labels.String(), " "), strings.TrimRight(values.String(), " ")}
}

// dailyLines draws the last 30 local days. A day with no usage is the lowest
// bar; a day whose records carry no counts is a gap, because that total is
// unknown rather than nothing.
func dailyLines(t theme.Theme, daily []store.DailyUsage, width int) []string {
	if len(daily) == 0 {
		return []string{widget.Rule(t, "daily tokens", "", width), t.Muted.Render("No daily history yet.")}
	}
	points := make([]widget.Point, 0, len(daily))
	peak := -1
	for index, day := range daily {
		point := widget.Point{Known: day.Tokens.State != store.AvailabilityUnavailable}
		if day.Tokens.Value != nil {
			point.Value = *day.Tokens.Value
			if peak < 0 || point.Value > *daily[peak].Tokens.Value {
				peak = index
			}
		}
		points = append(points, point)
	}
	// Two cells a day when there is room, so the bars read as a chart.
	cells := 1
	if len(points)*2 <= width {
		cells = 2
		doubled := make([]widget.Point, 0, len(points)*2)
		for _, point := range points {
			doubled = append(doubled, point, point)
		}
		points = doubled
	}
	shown := min(len(daily), width/cells)
	first := daily[len(daily)-shown]
	note := ""
	if peak >= 0 && *daily[peak].Tokens.Value > 0 {
		note = "peak " + tokensText(t.Glyphs, daily[peak].Tokens) + " " + dayLabel(daily[peak].Day)
	}
	axis := widget.Spread(dayLabel(first.Day), "TODAY", shown*cells)
	return []string{
		widget.Rule(t, "daily tokens", note, width),
		widget.Sparkline(t, points, width),
		t.Muted.Render(axis[0]),
	}
}

// categoryLines lists the priced token categories in full. Reasoning is part
// of output and the two lifetimes are part of cache write, so they are
// indented under their parent and carry no share of their own.
func categoryLines(t theme.Theme, tokens store.TokenBreakdown, width int) []string {
	g := t.Glyphs
	total := tokens.Total()
	type category struct {
		label  string
		value  store.AggregateValue
		nested bool
	}
	categories := []category{
		{"Uncached input", tokens.InputUncached, false},
		{"Cache read", tokens.InputCacheRead, false},
		{"Cache write", tokens.InputCacheWrite, false},
		{"5-minute", tokens.InputCacheWrite5m, true},
		{"1-hour", tokens.InputCacheWrite1h, true},
		{"Output", tokens.Output, false},
		{"reasoning", tokens.ReasoningOutput, true},
	}
	table := widget.Table{
		Flush:   true,
		Columns: []widget.Column{{}, {Align: widget.Right, Width: 18}, {Align: widget.Right, Width: 6}},
	}
	for _, category := range categories {
		label, share := category.label, shareText(g, category.value, total)
		var valueStyle, labelStyle *lipgloss.Style
		if category.nested {
			label, share, labelStyle = "  "+label, "", &t.Muted
		}
		value := exactText(g, category.value)
		if category.value.Value == nil {
			value, share, valueStyle = "not reported", "", &t.Muted
		}
		table.Rows = append(table.Rows, widget.Row{Cells: []widget.Cell{
			{Text: label, Style: labelStyle}, {Text: value, Style: valueStyle}, {Text: share, Style: &t.Muted},
		}})
	}
	return append([]string{widget.Rule(t, "categories", "exact", width)}, table.Render(t, width)...)
}

// usageColumns are the shared columns of every usage breakdown table.
func usageColumns(title string) []widget.Column {
	return []widget.Column{
		{Title: title},
		{Title: "tokens", Align: widget.Right, Width: 7},
		{Title: "$ est", Align: widget.Right, Width: 11},
		{Title: "share", Align: widget.Right, Width: 5},
	}
}

// usageRow is one breakdown row: a name, its tokens, estimate, and its share
// of the period's tokens.
func usageRow(t theme.Theme, name string, tokens store.TokenBreakdown, estimate store.AggregateValue, total store.AggregateValue) widget.Row {
	g := t.Glyphs
	var estimateStyle *lipgloss.Style
	if estimate.Value == nil {
		estimateStyle = &t.Muted
	}
	return widget.Row{Cells: []widget.Cell{
		{Text: name},
		{Text: tokensText(g, tokens.Total())},
		{Text: estimateText(g, estimate), Style: estimateStyle},
		{Text: shareText(g, tokens.Total(), total), Style: &t.Muted},
	}}
}

func sourceTable(t theme.Theme, summary store.UsageSummary, width int) []string {
	if len(summary.Sources) == 0 {
		return []string{t.Muted.Render("No sources in this period.")}
	}
	total := summary.Tokens.Total()
	table := widget.Table{Flush: true, Columns: usageColumns("source")}
	for _, source := range sortedByTokens(summary.Sources, func(source store.NamedBreakdown) store.TokenBreakdown { return source.Tokens }) {
		table.Rows = append(table.Rows, usageRow(t, sourceName(source.Name), source.Tokens, source.APIEstimate, total))
	}
	return table.Render(t, width)
}

// modelSummaryTable is HOME's short model list; MODELS has all of them.
func modelSummaryTable(t theme.Theme, summary store.UsageSummary, width int) []string {
	if len(summary.Models) == 0 {
		return []string{t.Muted.Render("No models in this period.")}
	}
	total := summary.Tokens.Total()
	models := sortedModels(summary.Models)
	table := widget.Table{Flush: true, Columns: usageColumns("model")}
	for _, model := range models[:min(len(models), homeModelRows)] {
		table.Rows = append(table.Rows, usageRow(t, modelName(model), model.Tokens, model.APIEstimate, total))
	}
	lines := table.Render(t, width)
	if hidden := len(models) - homeModelRows; hidden > 0 {
		lines = append(lines, t.Muted.Render(widget.Truncate(t, plural(hidden, "more model", "more models")+" "+t.Glyphs.Separator+" press 3", width)))
	}
	return lines
}

// sortedModels orders models by known tokens, largest first. Ties keep the
// order storage returned, which is by name, so rows do not swap between
// refreshes.
func sortedModels(models []store.ModelBreakdown) []store.ModelBreakdown {
	return sortedByTokens(models, func(model store.ModelBreakdown) store.TokenBreakdown { return model.Tokens })
}

func sortedByTokens[T any](items []T, tokens func(T) store.TokenBreakdown) []T {
	sorted := append([]T(nil), items...)
	known := func(item T) uint64 {
		if total := tokens(item).Total(); total.Value != nil {
			return *total.Value
		}
		return 0
	}
	sort.SliceStable(sorted, func(i, j int) bool { return known(sorted[i]) > known(sorted[j]) })
	return sorted
}

// noSourcesLines tells a user with no supported agent what tokenholic reads.
func noSourcesLines(t theme.Theme, width int) []string {
	lines := []string{t.Strong.Render("No supported coding-agent usage found."), ""}
	for _, paragraph := range []string{
		"Supported now: Claude Code, Claude Desktop coding sessions, and Codex CLI and Desktop.",
		"Use one of them, then press r to scan again. Run `tokenholic doctor` to check source detection.",
	} {
		lines = append(lines, widget.Wrap(paragraph, width)...)
	}
	return lines
}

func plural(count int, one, many string) string {
	if count == 1 {
		return "1 " + one
	}
	return strconv.Itoa(count) + " " + many
}

// healthStyle is the tone for an adapter state; the word beside it says
// which state it is.
func healthStyle(t theme.Theme, status normalize.HealthStatus) *lipgloss.Style {
	switch {
	case status == normalize.HealthDetected:
		return &t.Up
	case status == normalize.HealthNotFound, status == normalize.HealthUnavailable:
		return &t.Muted
	case status == normalize.HealthError || status == normalize.HealthPermissionDenied:
		return &t.Failure
	default:
		return &t.Warning
	}
}
