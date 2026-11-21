package widget

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/ansonnchan/tokenholic/internal/numfmt"
	"github.com/ansonnchan/tokenholic/internal/tui/goldentest"
	"github.com/ansonnchan/tokenholic/internal/tui/theme"
)

// gallery lays every component out with synthetic data, the way the views
// will use them. It is the design system's reference frame: the golden
// snapshots fix it at each documented width, with and without color.
func gallery(t theme.Theme, width int) []string {
	g := t.Glyphs
	var lines []string
	add := func(block ...string) { lines = append(lines, block...) }

	add(Header(t, []string{"HOME", "BOARDS", "MODELS", "SOURCES", "SETTINGS"}, 1, "synced 4m ago", width, 88)...)
	add("")
	if mark, ok := BlockWordmark(t, width); ok && width >= 88 {
		add(mark...)
		add("")
	}

	// Chips, a primary action, and field labels.
	inner := InnerWidth(width)
	periods := Chips(t, []string{"TODAY", "THIS WEEK", "30 DAYS", "ALL-TIME"}, 1, inner)
	metrics := Chips(t, []string{"$ EST", "TOKENS"}, 0, inner)
	controls := Spread(strings.Join(periods, ""), strings.Join(metrics, ""), inner)
	if len(periods) > 1 || len(controls) > 1 {
		controls = append(periods, metrics...)
	}
	controls = append(controls, "", Action(t, "c", "claim your spot"))
	controls = append(controls, Label(t, "scope")+"  NIGHT-SHIFT  "+t.Muted.Render("g next"))
	add(Panel(t, "chips and actions", width, controls)...)

	// Standings beside the standing rail; the rail stacks when narrow.
	boardWidth, railWidth := width, width
	if width >= 120 {
		railWidth = 38
		boardWidth = width - railWidth - 1
	}
	board := Panel(t, "night-shift", boardWidth, standings(t, InnerWidth(boardWidth)))
	rail := Panel(t, "your standing", railWidth, append(
		Spread(t.Accent.Render("@you"), Label(t, "rank")+" "+t.Strong.Render("#5"), InnerWidth(railWidth)),
		Spread("$640.00 "+t.Muted.Render("EST"), Movement(t, intPointer(2), false), InnerWidth(railWidth))...,
	))
	group := Panel(t, "night-shift", railWidth, []string{
		Spread(Label(t, "members"), "12", InnerWidth(railWidth))[0],
		Spread(Label(t, "invite")+"  Q7M2KX", t.Muted.Render("y copy"), InnerWidth(railWidth))[0],
		Label(t, "resets") + "  MON 00:00 LOCAL",
	})
	if width >= 120 {
		add(Columns(1, board, append(append(rail, group...), " "+Action(t, "s", "share")))...)
	} else {
		add(board...)
		add(rail...)
		add(group...)
	}

	// Headline numbers, sparkline, and a breakdown table.
	days := make([]Point, 30)
	for index := range days {
		days[index] = Point{Value: uint64((index*37 + 11) % 23 * 9_000_000), Known: index != 12 && index != 13}
	}
	days[4].Value = 0
	share := &t.Muted
	usage := Table{
		Columns: []Column{{Title: "source"}, {Title: "tokens", Align: Right, Width: 8}, {Title: "$ est", Align: Right, Width: 11}, {Title: "share", Align: Right, Width: 6}},
		Rows: []Row{
			{Cells: []Cell{{Text: "claude code"}, {Text: numfmt.Tokens(2_910_000_000)}, {Text: numfmt.USD(820_400_000)}, {Text: "68%", Style: share}}},
			{Cells: []Cell{{Text: "codex"}, {Text: numfmt.Tokens(1_350_000_000)}, {Text: g.AtLeast + numfmt.USD(359_600_000)}, {Text: "32%", Style: share}}},
			{Cells: []Cell{{Text: "claude desktop"}, {Text: numfmt.Tokens(987)}, {Text: numfmt.USD(4_200)}, {Text: "<0.1%", Style: share}}},
			{Cells: []Cell{{Text: "unpriced routing label"}, {Text: numfmt.Tokens(111_000)}, {Text: g.Unavailable, Style: share}, {Text: "<0.1%", Style: share}}},
		},
	}
	home := []string{
		t.Strong.Render(numfmt.Tokens(4_270_000_000)) + " " + t.Muted.Render("TOKENS") + "   " + t.Strong.Render(g.AtLeast+numfmt.USD(1_180_000_000)) + " " + t.Muted.Render("EST "+g.Separator+" 92% PRICED"),
		"",
		Rule(t, "daily tokens", "30 days", inner),
		Sparkline(t, days, inner),
		"",
		Rule(t, "by source", "", inner),
	}
	home = append(home, usage.Render(t, inner)...)
	add(Panel(t, "last 30 days", width, home)...)

	// Status words that read without color.
	notices := Notice(t, Warning, "Today includes partial or unavailable values", inner)
	notices = append(notices, Notice(t, Failure, "Could not read Codex sessions. Run tokenholic doctor for details.", inner)...)
	notices = append(notices, Notice(t, Success, "Invite copied", inner)...)
	notices = append(notices, Notice(t, Info, "Estimates are API-equivalent list prices, not what was paid.", inner)...)
	add(Panel(t, "notices", width, notices)...)

	add(KeyBar(t, []Hint{
		{Key: "1" + g.Range + "5", Label: "views", Keep: true},
		{Key: "g", Label: "scope"}, {Key: "p", Label: "period"}, {Key: "m", Label: "metric"},
		{Key: "j/k", Label: "move"}, {Key: "y", Label: "copy"}, {Key: "s", Label: "share"},
		{Key: "?", Label: "help", Keep: true}, {Key: "q", Label: "quit", Keep: true},
	}, width))
	return lines
}

// standings is a board with every row state the design names: medal ranks,
// each kind of movement, the current user, a lower bound without a rank, and
// an unavailable value.
func standings(t theme.Theme, width int) []string {
	g := t.Glyphs
	type member struct {
		rank     int
		name     string
		value    string
		note     string
		change   *int
		entrant  bool
		viewer   bool
		unranked bool
	}
	members := []member{
		{rank: 1, name: "@dana", value: numfmt.USD(1_180_000_000), change: intPointer(1)},
		{rank: 2, name: "@kai", value: numfmt.USD(1_090_000_000), change: intPointer(-1)},
		{rank: 3, name: "@rio", value: numfmt.USD(840_500_000), change: intPointer(0)},
		{rank: 4, name: "@lee", value: numfmt.USD(712_250_000), entrant: true},
		{rank: 5, name: "@you", value: numfmt.USD(640_000_000), change: intPointer(2), viewer: true},
		{name: "@sam", value: g.AtLeast + numfmt.USD(560_750_000), note: "71% priced", unranked: true},
		{name: "@a-very-long-display-name-that-must-truncate", value: g.Unavailable, note: "no estimate", unranked: true},
	}
	narrow := width < 60
	columns := []Column{{Width: 2, Align: Right}, {}, {Width: 10, Align: Right}, {Width: 11}}
	if narrow {
		columns[3].Width = 10
	}
	table := Table{Columns: columns}
	for _, member := range members {
		rank, rankStyle := g.Unavailable, &t.Muted
		if !member.unranked {
			rank, rankStyle = fmt.Sprint(member.rank), RankStyle(t, member.rank)
		}
		name := member.name
		if member.viewer {
			name += "  " + g.Star + " YOU"
		}
		trend, trendStyle := MovementText(t, member.change, member.entrant)
		if member.note != "" {
			trend, trendStyle = member.note, &t.Muted
		}
		var valueStyle *lipgloss.Style
		if member.value == g.Unavailable {
			valueStyle = &t.Muted
		}
		table.Rows = append(table.Rows, Row{Current: member.viewer, Cells: []Cell{
			{Text: rank, Style: rankStyle}, {Text: name}, {Text: member.value, Style: valueStyle}, {Text: trend, Style: trendStyle},
		}})
	}
	comparison := g.Up + g.Down + " vs last week"
	if narrow {
		comparison = "vs last wk"
	}
	lines := []string{Rule(t, "standings", comparison, width)}
	lines = append(lines, table.Render(t, width)...)
	lines = append(lines, "")
	for _, line := range Wrap("1"+g.Range+"7 OF 12 "+g.Separator+" RESETS MON 00:00 America/Vancouver", width) {
		lines = append(lines, t.Muted.Render(line))
	}
	return lines
}

func intPointer(value int) *int { return &value }

func TestGalleryGoldenSnapshots(t *testing.T) {
	t.Parallel()
	variants := []struct {
		name               string
		color, dark, ascii bool
		widths             []int
	}{
		{name: "color", color: true, dark: true, widths: []int{48, 88, 120}},
		{name: "plain", dark: true, widths: []int{48, 88, 120}},
		{name: "light", color: true, widths: []int{120}},
		{name: "ascii", dark: true, ascii: true, widths: []int{48, 120}},
	}
	for _, variant := range variants {
		for _, width := range variant.widths {
			name := fmt.Sprintf("gallery_%d_%s", width, variant.name)
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				lines := gallery(theme.Fixed(variant.color, variant.dark, variant.ascii), width)
				goldentest.Fits(t, lines, width, variant.ascii)
				goldentest.Compare(t, name, lines)
			})
		}
	}
}
