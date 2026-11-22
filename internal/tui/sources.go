package tui

import (
	"strings"

	"github.com/ansonnchan/tokenholic/internal/collect"
	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/numfmt"
	"github.com/ansonnchan/tokenholic/internal/store"
	"github.com/ansonnchan/tokenholic/internal/tui/widget"
)

// sourcesView shows what each adapter found on this machine and how the
// period's usage divides across sources and surfaces. It names states,
// never paths.
func (m Model) sourcesView(width int) []string {
	t := m.theme
	if width >= wideWidth {
		right := (width - 1) / 2
		left := width - 1 - right
		return widget.Columns(1,
			widget.Panel(t, "local sources", left, m.sourceHealthLines(widget.InnerWidth(left))),
			widget.Panel(t, "usage by source", right, m.sourceUsageLines(widget.InnerWidth(right))),
		)
	}
	inner := widget.InnerWidth(width)
	return append(
		widget.Panel(t, "local sources", width, m.sourceHealthLines(inner)),
		widget.Panel(t, "usage by source", width, m.sourceUsageLines(inner))...,
	)
}

// sourceHealthLines is the adapter table. Record counts and read times are
// dropped before a source name is cut.
func (m Model) sourceHealthLines(width int) []string {
	t := m.theme
	g := t.Glyphs
	health := m.dashboard.Diagnostics.AdapterHealth
	if len(health) == 0 {
		return []string{t.Muted.Render("No source has been scanned yet. Press r to scan.")}
	}
	columns := []widget.Column{{Title: "source"}, {Title: "status", Width: 13}}
	full := width >= 56
	if full {
		columns = append(columns,
			widget.Column{Title: "records", Align: widget.Right, Width: 9},
			widget.Column{Title: "last read", Align: widget.Right, Width: 9},
		)
	}
	table := widget.Table{Flush: true, Columns: columns}
	for _, record := range health {
		surface := surfaceName(string(record.Health.Surface))
		if record.Health.Source.DayLevel() {
			// Every day-level agent is a terminal program, so its surface
			// says nothing; how coarsely it is read does.
			surface = byDay
		}
		name := sourceName(string(record.Health.Source)) + " " + g.Separator + " " + surface
		cells := []widget.Cell{{Text: name}, {Text: healthText(record.Health.Status), Style: healthStyle(t, record.Health.Status)}}
		if full {
			records := widget.Cell{Text: g.Unavailable, Style: &t.Muted}
			if record.Health.RecordCount != nil {
				records = widget.Cell{Text: numfmt.Exact(*record.Health.RecordCount)}
				if record.Health.Source.DayLevel() {
					// What was read is days of usage, not record files.
					records.Text = plural(int(*record.Health.RecordCount), "day", "days")
				}
			}
			cells = append(cells, records, widget.Cell{Text: age(record.LastSuccessAt, m.dashboard.GeneratedAt), Style: &t.Muted})
		}
		table.Rows = append(table.Rows, widget.Row{Cells: cells})
	}
	lines := table.Render(t, width)
	for _, note := range append(dayLevelNotes(health), "Run `tokenholic doctor` for path-redacted detail on any source.") {
		lines = append(lines, "")
		for _, line := range widget.Wrap(note, width) {
			lines = append(lines, t.Muted.Render(line))
		}
	}
	return lines
}

// dayLevelNotes explains the day-level rows: what "by day" covers, and for
// each agent that is here but could not be read, why. An agent that is not
// found needs no note, and one that cannot be read is never shown as zero.
func dayLevelNotes(health []store.StoredAdapterHealth) []string {
	var read bool
	reasons := make(map[string][]string)
	var order []string
	for _, record := range health {
		if !record.Health.Source.DayLevel() {
			continue
		}
		switch record.Health.Status {
		case normalize.HealthDetected:
			read = true
		case normalize.HealthUnavailable, normalize.HealthError:
			reason := "ccusage could not read it."
			if len(record.Health.Warnings) > 0 {
				reason = dayLevelReason(record.Health.Warnings[0].Code)
			}
			if _, seen := reasons[reason]; !seen {
				order = append(order, reason)
			}
			reasons[reason] = append(reasons[reason], sourceName(string(record.Health.Source)))
		}
	}
	var notes []string
	if read {
		notes = append(notes, "By day: one total per UTC day, read through ccusage. Day boundaries are approximate, and prices that depend on a request's size are not estimated.")
	}
	for _, reason := range order {
		notes = append(notes, strings.Join(reasons[reason], ", ")+" not counted: "+reason)
	}
	return notes
}

// dayLevelReason says in words why ccusage did not read an agent.
func dayLevelReason(code string) string {
	switch code {
	case collect.WarningCCUsageOff:
		return "ccusage is turned off. Run `tokenholic ccusage on`."
	case collect.WarningCCUsageNotInstalled:
		return "this install has no ccusage. It comes with the npm package."
	case collect.WarningCCUsageUnpinned:
		return "the ccusage here is not the pinned build, so it was not run."
	case collect.WarningCCUsageMalformed:
		return "ccusage answered in a form tokenholic does not accept."
	case collect.WarningCCUsageTimeout:
		return "ccusage did not finish in time."
	default:
		return "ccusage could not read it."
	}
}

// sourceUsageLines is the period's usage by source and then by surface.
func (m Model) sourceUsageLines(width int) []string {
	t := m.theme
	summary := summaryFor(m.dashboard, m.period)
	lines := append(periodChips(t, m.period, width), "")
	if len(summary.Sources) == 0 && len(summary.Surfaces) == 0 {
		return append(lines, t.Muted.Render("No usage recorded in this period."))
	}
	total := summary.Tokens.Total()
	sources := widget.Table{Flush: true, Columns: usageColumns("source")}
	for _, source := range summary.Sources {
		sources.Rows = append(sources.Rows, usageRow(t, sourceName(source.Name), source.Tokens, source.APIEstimate, total))
	}
	surfaces := widget.Table{Flush: true, Columns: usageColumns("surface")}
	for _, surface := range summary.Surfaces {
		surfaces.Rows = append(surfaces.Rows, usageRow(t, surfaceName(surface.Name), surface.Tokens, surface.APIEstimate, total))
	}
	lines = append(lines, sources.Render(t, width)...)
	lines = append(lines, "")
	return append(lines, surfaces.Render(t, width)...)
}
