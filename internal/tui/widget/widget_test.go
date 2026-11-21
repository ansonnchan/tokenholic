package widget

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ansonnchan/tokenholic/internal/tui/theme"
)

var (
	plain = theme.Fixed(false, true, false)
	ascii = theme.Fixed(false, true, true)
	color = theme.Fixed(true, true, false)
)

func TestTruncateCountsCellsAndMarksTheCut(t *testing.T) {
	t.Parallel()
	cases := []struct {
		tokens theme.Theme
		text   string
		width  int
		want   string
	}{
		{plain, "claude code", 11, "claude code"},
		{plain, "claude desktop", 10, "claude de…"},
		{plain, "claude desktop", 8, "claude…"},
		{ascii, "claude desktop", 10, "claude de~"},
		{plain, "abc", 1, "…"},
		{plain, "abc", 0, ""},
		{plain, "≥$560.75", 4, "≥$5…"},
	}
	for _, test := range cases {
		if got := Truncate(test.tokens, test.text, test.width); got != test.want {
			t.Errorf("Truncate(%q, %d) = %q, want %q", test.text, test.width, got, test.want)
		}
	}
}

func TestWrapBreaksAtSpacesAndSplitsLongWords(t *testing.T) {
	t.Parallel()
	cases := []struct {
		text  string
		width int
		want  []string
	}{
		{"", 10, []string{""}},
		{"one two three", 7, []string{"one two", "three"}},
		{"abcdefghij kl", 4, []string{"abcd", "efgh", "ij", "kl"}},
		{"1–7 OF 12", 5, []string{"1–7", "OF 12"}},
	}
	for _, test := range cases {
		if got := Wrap(test.text, test.width); !reflect.DeepEqual(got, test.want) {
			t.Errorf("Wrap(%q, %d) = %q, want %q", test.text, test.width, got, test.want)
		}
	}
}

func TestSpreadMovesTheRightSideDownInsteadOfCutting(t *testing.T) {
	t.Parallel()
	if got := Spread("left", "right", 12); !reflect.DeepEqual(got, []string{"left   right"}) {
		t.Errorf("Spread fits = %q", got)
	}
	if got := Spread("left", "right", 10); !reflect.DeepEqual(got, []string{"left", "right"}) {
		t.Errorf("Spread too narrow = %q", got)
	}
}

func TestSelectionIsBracketedWithoutColorAndKeepsItsWidth(t *testing.T) {
	t.Parallel()
	if got := Chip(plain, "BOARDS", true); got != "[BOARDS]" {
		t.Errorf("plain selected chip = %q", got)
	}
	if got := Chip(plain, "BOARDS", false); got != " BOARDS " {
		t.Errorf("plain chip = %q", got)
	}
	selected := Chip(color, "BOARDS", true)
	if strings.Contains(selected, "[BOARDS]") || !strings.Contains(selected, "48;2;") {
		t.Errorf("color selected chip = %q, want a solid block", selected)
	}
	if Width(selected) != Width(Chip(plain, "BOARDS", true)) {
		t.Error("selected chip changes width with color")
	}
	if got := Action(plain, "c", "claim your spot"); got != "[ c  CLAIM YOUR SPOT ]" {
		t.Errorf("plain action = %q", got)
	}
	if Width(Action(color, "c", "claim your spot")) != Width(Action(plain, "c", "claim your spot")) {
		t.Error("action changes width with color")
	}
}

func TestChipsWrapBetweenLabels(t *testing.T) {
	t.Parallel()
	got := Chips(plain, []string{"TODAY", "THIS WEEK", "30 DAYS", "ALL-TIME"}, 1, 20)
	want := []string{" TODAY [THIS WEEK]", " 30 DAYS  ALL-TIME "}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Chips = %q, want %q", got, want)
	}
}

func TestPanelIsExactlyItsWidth(t *testing.T) {
	t.Parallel()
	for _, tokens := range []theme.Theme{plain, ascii, color} {
		for _, title := range []string{"", "your standing", "a title far too long for a panel this narrow"} {
			lines := Panel(tokens, title, 24, []string{"one", "", tokens.Accent.Render("three")})
			if len(lines) != 5 {
				t.Fatalf("Panel has %d lines", len(lines))
			}
			for _, line := range lines {
				if Width(line) != 24 {
					t.Errorf("Panel(%q) line is %d cells: %q", title, Width(line), line)
				}
			}
		}
	}
	if got := Panel(plain, "your standing", 24, nil)[0]; got != "╭─ YOUR STANDING ──────╮" {
		t.Errorf("titled edge = %q", got)
	}
	if got := Panel(ascii, "your standing", 24, nil)[0]; got != "+- YOUR STANDING ------+" {
		t.Errorf("ASCII titled edge = %q", got)
	}
}

func TestRuleFillsItsWidthAndKeepsTheWordsWhenNarrow(t *testing.T) {
	t.Parallel()
	cases := []struct {
		label, note string
		width       int
		want        string
	}{
		{"standings", "vs last week", 34, "STANDINGS - - - - -   VS LAST WEEK"},
		{"by source", "", 19, "BY SOURCE - - - - -"},
		{"by source", "", 20, "BY SOURCE - - - - - "},
		{"standings", "vs last week", 23, "STANDINGS  VS LAST WEEK"},
		{"standings", "vs last week", 22, "STANDINGS"},
		{"standings", "", 6, "STAND…"},
	}
	for _, test := range cases {
		if got := Rule(plain, test.label, test.note, test.width); got != test.want {
			t.Errorf("Rule(%q, %q, %d) = %q, want %q", test.label, test.note, test.width, got, test.want)
		}
	}
}

func TestTableAlignsTruncatesAndMarksTheCurrentRow(t *testing.T) {
	t.Parallel()
	table := Table{
		Columns: []Column{{Title: "#", Width: 2, Align: Right}, {Title: "member"}, {Title: "$ est", Width: 9, Align: Right}},
		Rows: []Row{
			{Cells: []Cell{{Text: "1"}, {Text: "@dana"}, {Text: "$1,180.00"}}},
			{Cells: []Cell{{Text: "12"}, {Text: "@a-long-display-name"}, {Text: "$640.00"}}, Current: true},
			{Cells: []Cell{{Text: "—"}}},
		},
	}
	want := []string{
		"   #  MEMBER          $ EST",
		"   1  @dana       $1,180.00",
		"▌ 12  @a-long-d…    $640.00",
		"   —                       ",
	}
	if got := table.Render(plain, 27); !reflect.DeepEqual(got, want) {
		t.Errorf("table =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got := table.Render(ascii, 27)[2]; got != "| 12  @a-long-d~    $640.00" {
		t.Errorf("ASCII current row = %q", got)
	}
	// The cursor is a pointer in the edge cells: on its own it does not tint
	// the row, and on the viewer's row it replaces the edge.
	table.Rows[0].Cursor = true
	if got := table.Render(plain, 27)[1]; got != "▸  1  @dana       $1,180.00" {
		t.Errorf("cursor row = %q", got)
	}
	if got := table.Render(ascii, 27)[1]; got != ">  1  @dana       $1,180.00" {
		t.Errorf("ASCII cursor row = %q", got)
	}
	if cursor := table.Render(color, 27)[1]; strings.Contains(cursor, "48;2;") || !strings.Contains(cursor, "\x1b[1m") {
		t.Errorf("cursor row should be bold and untinted: %q", cursor)
	}
	table.Rows[0].Cursor, table.Rows[1].Cursor = false, true
	if got := table.Render(plain, 27)[2]; got != "▸ 12  @a-long-d…    $640.00" {
		t.Errorf("cursor on the current row = %q", got)
	}
	table.Rows[1].Cursor = false
	// A tag in Keep outlives the name it follows.
	table.Rows[1].Cells[1] = Cell{Text: "@a-long-display-name", Keep: " ★ YOU"}
	if got := table.Render(plain, 27)[2]; got != "▌ 12  @a-… ★ YOU    $640.00" {
		t.Errorf("row with a kept tag = %q", got)
	}
	for _, line := range table.Render(color, 27) {
		if Width(line) != 27 {
			t.Errorf("color row is %d cells: %q", Width(line), line)
		}
	}
	if current := table.Render(color, 27)[2]; strings.Count(current, "48;2;") < 5 {
		t.Errorf("current row is not tinted across its cells and gaps: %q", current)
	}
}

func TestSparklineKeepsZeroUnknownAndQuietDaysApart(t *testing.T) {
	t.Parallel()
	points := []Point{
		{Value: 0, Known: true}, {Value: 1, Known: true}, {Known: false},
		{Value: 500, Known: true}, {Value: 1000, Known: true},
	}
	if got := Sparkline(plain, points, 10); got != "▁▂ ▅█" {
		t.Errorf("sparkline = %q", got)
	}
	if got := Sparkline(ascii, points, 10); got != "_. -#" {
		t.Errorf("ASCII sparkline = %q", got)
	}
	if got := Sparkline(plain, points, 2); got != "▅█" {
		t.Errorf("clipped sparkline = %q, want the most recent points", got)
	}
	if got := Sparkline(plain, []Point{{Known: true}, {Known: true}}, 5); got != "▁▁" {
		t.Errorf("all-zero sparkline = %q", got)
	}
}

func TestMovementReadsWithoutColorOrUnicode(t *testing.T) {
	t.Parallel()
	up, down, same := 2, -1, 0
	cases := []struct {
		change      *int
		entrant     bool
		want, plain string
	}{
		{&up, false, "▲ 2", "+2"},
		{&down, false, "▼ 1", "-1"},
		{&same, false, "–", "="},
		{nil, true, "NEW", "new"},
		{&up, true, "NEW", "new"},
		{nil, false, "", ""},
	}
	for _, test := range cases {
		if got := Movement(plain, test.change, test.entrant); got != test.want {
			t.Errorf("Movement = %q, want %q", got, test.want)
		}
		if got := Movement(ascii, test.change, test.entrant); got != test.plain {
			t.Errorf("ASCII Movement = %q, want %q", got, test.plain)
		}
	}
}

func TestKeyBarDropsOptionalHintsFromTheEnd(t *testing.T) {
	t.Parallel()
	hints := []Hint{
		{Key: "1–5", Label: "views", Keep: true}, {Key: "p", Label: "period"}, {Key: "m", Label: "metric"},
		{Key: "?", Label: "help", Keep: true}, {Key: "q", Label: "quit", Keep: true},
	}
	if got := KeyBar(plain, hints, 80); got != "1–5 views · p period · m metric · ? help · q quit" {
		t.Errorf("KeyBar = %q", got)
	}
	if got := KeyBar(plain, hints, 40); got != "1–5 views · p period · ? help · q quit" {
		t.Errorf("narrow KeyBar = %q", got)
	}
	if got := KeyBar(ascii, hints, 10); got != "1–5 views | ? help | q quit" {
		t.Errorf("minimum KeyBar = %q", got)
	}
}

func TestNoticeNamesItsLevelInWords(t *testing.T) {
	t.Parallel()
	got := Notice(plain, Warning, "Today includes partial or unavailable values", 30)
	want := []string{"WARNING  Today includes", "         partial or", "         unavailable values"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Notice = %q, want %q", got, want)
	}
	if got := Notice(plain, Failure, "Could not read Codex sessions", 80)[0]; got != "ERROR  Could not read Codex sessions" {
		t.Errorf("failure notice = %q", got)
	}
}

func TestHeaderStacksTabsBelowTheWideWidth(t *testing.T) {
	t.Parallel()
	tabs := []string{"HOME", "BOARDS", "MODELS", "SOURCES", "SETTINGS"}
	wide := Header(plain, tabs, 0, "synced 4m ago", 88, 88)
	if len(wide) != 1 || Width(wide[0]) != 88 || !strings.HasPrefix(wide[0], ">_ TOKENHOLIC   [HOME] BOARDS ") {
		t.Errorf("wide header = %q", wide)
	}
	narrow := Header(plain, tabs, 4, "synced 4m ago", 48, 88)
	want := []string{">_ TOKENHOLIC                      synced 4m ago", " HOME  BOARDS  MODELS  SOURCES [SETTINGS]"}
	if !reflect.DeepEqual(narrow, want) {
		t.Errorf("narrow header = %q, want %q", narrow, want)
	}
}

func TestBlockWordmarkFallsBack(t *testing.T) {
	t.Parallel()
	if lines, ok := BlockWordmark(plain, 88); !ok || len(lines) != 3 {
		t.Errorf("BlockWordmark = %q, %t", lines, ok)
	}
	if _, ok := BlockWordmark(plain, 40); ok {
		t.Error("BlockWordmark fits 40 cells")
	}
	if _, ok := BlockWordmark(ascii, 120); ok {
		t.Error("BlockWordmark drawn without Unicode")
	}
	if got := Wordmark(plain); got != ">_ TOKENHOLIC" {
		t.Errorf("Wordmark = %q", got)
	}
}
