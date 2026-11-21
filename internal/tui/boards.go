package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/ansonnchan/tokenholic/internal/multiplayer"
	"github.com/ansonnchan/tokenholic/internal/numfmt"
	"github.com/ansonnchan/tokenholic/internal/tui/theme"
	"github.com/ansonnchan/tokenholic/internal/tui/widget"
)

// The board area renders what the server ranked. It never orders rows,
// assigns a rank, or decides whether an estimate is comparable: rank,
// unranked reason, priced share, hidden ranks, and the comparison all arrive
// in the board and are shown as they are.

// boardRailWidth is the standing rail beside the standings on wide layouts.
const boardRailWidth = 38

// boardGate is why the area cannot show a board at all.
type boardGate int

const (
	gateOpen boardGate = iota
	gateNotConfigured
	gateSignedOut
)

// boardPrompt is an inline question: a group name, an invite code, or a
// yes-or-no confirmation of a visibility change.
type boardPrompt struct {
	action string
	value  string
}

const (
	promptCreate      = "create"
	promptJoin        = "join"
	promptGlobalJoin  = "global-join"
	promptGlobalLeave = "global-leave"
)

// boardState is everything the board area holds between loads.
type boardState struct {
	loading  bool
	loaded   bool
	gate     boardGate
	failure  string
	snapshot multiplayer.Snapshot
	// scope is a group ID or multiplayer.GlobalScope; empty selects the
	// first group.
	scope  string
	period multiplayer.Period
	metric multiplayer.Metric
	// cursor rests on the viewer's own row until they move it.
	cursor int
	moved  bool
	prompt boardPrompt
	// invites holds codes created in this session, by group. The server
	// keeps only their hashes, so a code cannot be read back later.
	invites map[string]multiplayer.Invite
	flash   string
	flashID uint64
	request uint64
}

var (
	boardPeriods = []multiplayer.Period{
		multiplayer.PeriodToday, multiplayer.PeriodWeek, multiplayer.PeriodLast30Days, multiplayer.PeriodAllTime,
	}
	boardMetrics = []multiplayer.Metric{multiplayer.MetricAPIEstimate, multiplayer.MetricTotalTokens}
)

// boardsView renders the board area for the terminal's width and the rows
// available to it.
func (m Model) boardsView(width, height int) []string {
	t := m.theme
	board := m.board.snapshot.Board
	inner := widget.InnerWidth(width)
	switch {
	case m.board.gate == gateNotConfigured:
		return widget.Panel(t, "boards", width, wrapAll(inner,
			"Boards need the tokenholic API.",
			"Set TOKENHOLIC_API_URL, run `tokenholic claim`, then reopen tokenholic.",
		))
	case m.board.gate == gateSignedOut:
		return widget.Panel(t, "boards", width, wrapAll(inner,
			"Boards rank you with friends, by tokens or by estimate.",
			"Sign in to join one: run `tokenholic claim`, then press r.",
		))
	case !m.board.loaded && m.board.failure == "":
		return widget.Panel(t, "boards", width, []string{t.Muted.Render(m.spin() + " Loading boards")})
	case board == nil:
		lines := m.boardPromptLines(inner)
		if m.board.failure != "" {
			lines = append(lines, widget.Notice(t, widget.Failure, m.board.failure+". Press r to retry.", inner)...)
		} else {
			lines = append(lines, wrapAll(inner,
				"You are not in a group yet.",
				"Press c to create one, i to join with an invite code, or g to see the global board.",
			)...)
		}
		return widget.Panel(t, "boards", width, lines)
	}

	rows := board.Rows
	cursor := max(0, min(m.board.cursor, len(rows)-1))
	viewer := viewerRow(board)

	wide, stacked := width >= wideWidth, width >= stackWidth
	mainWidth := width
	if wide {
		mainWidth = width - boardRailWidth - 1
	}
	mainInner := widget.InnerWidth(mainWidth)

	// Everything in the main panel except the rows, so the rows can take
	// what the terminal has left.
	head := m.boardPromptLines(mainInner)
	if m.board.failure != "" {
		head = append(head, widget.Notice(t, widget.Failure, m.board.failure+". Press r to retry.", mainInner)...)
	}
	head = append(head, boardChips(t, board, mainInner)...)
	head = append(head, "", widget.Rule(t, "standings", t.Glyphs.Up+t.Glyphs.Down+" "+comparisonLegend(board), mainInner))

	var foot []string
	if len(rows) > 0 {
		foot = append(foot, "")
		foot = append(foot, selectedLines(t, board, rows[cursor], mainInner)...)
	}

	var below []string
	switch {
	case wide:
	case stacked:
		right := (width - 1) / 2
		left := width - 1 - right
		below = widget.Columns(1,
			widget.Panel(t, "your standing", left, standingLines(t, board, viewer, widget.InnerWidth(left))),
			widget.Panel(t, scopeTitle(board), right, m.scopeLines(board, viewer, widget.InnerWidth(right))),
		)
	default:
		below = m.summaryLines(board, viewer, width)
	}

	capacity := len(rows)
	if height > 0 {
		// Frame, position line and its blank, and whatever sits below.
		capacity = height - len(head) - len(foot) - len(below) - 4
		capacity = max(capacity, 3)
	}
	body := append(head, standingRows(t, board, cursor, viewer, capacity, mainInner)...)
	body = append(body, foot...)
	main := widget.Panel(t, boardTitle(board), mainWidth, body)

	if wide {
		rail := widget.Panel(t, "your standing", boardRailWidth, standingLines(t, board, viewer, widget.InnerWidth(boardRailWidth)))
		rail = append(rail, widget.Panel(t, scopeTitle(board), boardRailWidth, m.scopeLines(board, viewer, widget.InnerWidth(boardRailWidth)))...)
		return widget.Columns(1, main, rail)
	}
	return append(main, below...)
}

// boardTitle names the scope: the group, or the global board.
func boardTitle(board *multiplayer.Board) string {
	if board.Scope == multiplayer.ScopeGlobal {
		return "global"
	}
	return cleanLine(board.GroupName)
}

func scopeTitle(board *multiplayer.Board) string {
	if board.Scope == multiplayer.ScopeGlobal {
		return "global board"
	}
	return "group"
}

// boardChips is the period selector with the metric selector at the right,
// or on its own line when they do not fit together.
func boardChips(t theme.Theme, board *multiplayer.Board, width int) []string {
	periodLabels := []string{"TODAY", "THIS WEEK", "30 DAYS", "ALL-TIME"}
	periodChips := widget.Chips(t, periodLabels, indexOf(boardPeriods, board.Period), width)
	metricChips := widget.Chips(t, []string{"$ EST", "TOKENS"}, indexOf(boardMetrics, board.Metric), width)
	if len(periodChips) == 1 && len(metricChips) == 1 {
		if joined := widget.Spread(periodChips[0], metricChips[0], width); len(joined) == 1 {
			return joined
		}
	}
	return append(periodChips, metricChips...)
}

// standingRows is the visible window of the standings around the cursor,
// with the viewer's own row pinned after it when it is out of view, and the
// line that says which rows are showing and when the period resets.
func standingRows(t theme.Theme, board *multiplayer.Board, cursor, viewer, capacity, width int) []string {
	g := t.Glyphs
	rows := board.Rows
	if len(rows) == 0 {
		return []string{t.Muted.Render("No members on this board yet."), "", t.Muted.Render(resetText(board))}
	}
	start, end := window(cursor, len(rows), min(capacity, len(rows)))
	pinned := viewer >= 0 && (viewer < start || viewer >= end)
	if pinned && end-start > 1 && end-start == capacity {
		// Give one row of the window to the viewer's own row.
		start, end = window(cursor, len(rows), capacity-1)
		pinned = viewer < start || viewer >= end
	}

	// Columns are as wide as their widest text, so a large value or a deep
	// rank never truncates.
	rankWidth, valueWidth := 2, 7
	texts := make([][4]string, len(rows))
	styles := make([][4]*lipgloss.Style, len(rows))
	tags := make([]string, len(rows))
	for index, row := range rows {
		texts[index], styles[index], tags[index] = boardCells(t, board, row)
		rankWidth = max(rankWidth, widget.Width(texts[index][0]))
		valueWidth = max(valueWidth, widget.Width(texts[index][2]))
	}
	table := widget.Table{Columns: []widget.Column{
		{Width: rankWidth, Align: widget.Right}, {}, {Width: valueWidth, Align: widget.Right}, {Width: 12},
	}}
	add := func(index int) {
		cells := make([]widget.Cell, 4)
		for column := range cells {
			cells[column] = widget.Cell{Text: texts[index][column], Style: styles[index][column]}
		}
		cells[1].Keep = tags[index]
		table.Rows = append(table.Rows, widget.Row{Cells: cells, Current: rows[index].CurrentUser, Cursor: index == cursor})
	}
	for index := start; index < end; index++ {
		add(index)
	}
	if pinned {
		add(viewer)
	}
	lines := table.Render(t, width)

	position := positionText(t, start, end, max(board.TotalMembers, len(rows)))
	notes := []string{position + " " + g.Separator + " " + resetText(board)}
	if board.RanksHidden {
		notes = append(notes, "Ranks are hidden until "+plural(board.MinRankedMembers, "member", "members")+" can be ranked.")
	}
	lines = append(lines, "")
	for _, note := range notes {
		for _, line := range widget.Wrap(note, width) {
			lines = append(lines, t.Muted.Render(line))
		}
	}
	return lines
}

// boardCells is one member's row: rank, name, value, and the movement or
// the reason there is no rank. The tags that follow the name are returned
// apart from it, so a long name is cut before they are.
func boardCells(t theme.Theme, board *multiplayer.Board, row multiplayer.Row) ([4]string, [4]*lipgloss.Style, string) {
	g := t.Glyphs
	var texts [4]string
	var styles [4]*lipgloss.Style
	tags := ""

	texts[0], styles[0] = g.Unavailable, &t.Muted
	if row.Rank != nil {
		texts[0], styles[0] = strconv.Itoa(*row.Rank), widget.RankStyle(t, *row.Rank)
	}
	texts[1] = memberName(row)
	if row.CurrentUser {
		tags += "  " + g.Star + " YOU"
	}
	if row.Plausibility.DailyTokensAboveLimit {
		tags += " !"
	}
	texts[2] = boardValue(g, board.Metric, row.Value)
	if row.Value.Value == nil {
		styles[2] = &t.Muted
	}
	texts[3], styles[3] = standingNote(t, board, row)
	return texts, styles, tags
}

// standingNote is the last column: rank movement for a ranked row, and
// otherwise the server's reason for leaving the row unranked.
func standingNote(t theme.Theme, board *multiplayer.Board, row multiplayer.Row) (string, *lipgloss.Style) {
	if row.Rank != nil {
		return widget.MovementText(t, row.Trend.RankChange, row.Trend.New)
	}
	switch row.UnrankedReason {
	case multiplayer.UnrankedCostCoverage:
		if row.Coverage.PricedBasisPoints == nil {
			return "partly priced", &t.Muted
		}
		return numfmt.Percent(*row.Coverage.PricedBasisPoints) + " priced", &t.Muted
	case multiplayer.UnrankedValueUnavailable:
		if board.Metric == multiplayer.MetricAPIEstimate {
			return "no estimate", &t.Muted
		}
		return "no total", &t.Muted
	case multiplayer.UnrankedNoUsage:
		return "no usage", &t.Muted
	case multiplayer.UnrankedScopeTooSmall, "":
		// No rank to move, but a first appearance is still worth showing.
		return widget.MovementText(t, nil, row.Trend.New)
	default:
		// A reason from a newer server is shown as sent rather than hidden.
		return cleanLine(string(row.UnrankedReason)), &t.Muted
	}
}

// memberName is a member's GitHub login as the board shows it.
func memberName(row multiplayer.Row) string {
	name := strings.TrimSpace(cleanLine(row.DisplayName))
	if name == "" {
		return "(unnamed)"
	}
	return "@" + strings.TrimPrefix(name, "@")
}

// boardValue renders a ranked value: an estimate in dollars or a token
// count, as a lower bound when partial and never as zero when unknown.
func boardValue(g theme.Glyphs, metric multiplayer.Metric, value multiplayer.Value) string {
	if value.Value == nil {
		return g.Unavailable
	}
	text, ok := numfmt.TokensDecimal(*value.Value)
	if metric == multiplayer.MetricAPIEstimate {
		text, ok = numfmt.USDDecimal(*value.Value)
	}
	if !ok {
		return g.Unavailable
	}
	if value.State == multiplayer.AvailabilityPartial {
		return g.AtLeast + text
	}
	return text
}

// boardUnit labels a value's metric. An estimate is always labelled EST.
func boardUnit(metric multiplayer.Metric) string {
	if metric == multiplayer.MetricAPIEstimate {
		return "EST"
	}
	return "TOKENS"
}

// comparisonLegend names what movement is measured against, as the board
// reported it.
func comparisonLegend(board *multiplayer.Board) string {
	return "VS " + strings.ToUpper(comparisonWords(board))
}

func comparisonWords(board *multiplayer.Board) string {
	if board.Comparison.Kind == multiplayer.ComparisonDayEarlier {
		return "24h ago"
	}
	switch board.Period {
	case multiplayer.PeriodToday:
		return "yesterday"
	case multiplayer.PeriodWeek:
		return "last week"
	case multiplayer.PeriodLast30Days:
		return "prior 30 days"
	default:
		return "previous period"
	}
}

// resetText says when the period's window starts over, in the board's own
// timezone.
func resetText(board *multiplayer.Board) string {
	zone := cleanLine(board.Timezone)
	switch board.Period {
	case multiplayer.PeriodToday:
		return "RESETS 00:00 " + zone
	case multiplayer.PeriodWeek:
		return "RESETS MON 00:00 " + zone
	case multiplayer.PeriodLast30Days:
		return "ROLLING 30 DAYS " + zone
	default:
		return "ALL SYNCED HISTORY"
	}
}

// viewerRow is the index of the viewer's own row, or -1 when they are not
// on the board.
func viewerRow(board *multiplayer.Board) int {
	for index, row := range board.Rows {
		if row.CurrentUser {
			return index
		}
	}
	return -1
}

// standingLines is the viewer's own result: rank, value, movement, and the
// value in the comparison window.
func standingLines(t theme.Theme, board *multiplayer.Board, viewer, width int) []string {
	g := t.Glyphs
	if viewer < 0 {
		if board.Scope == multiplayer.ScopeGlobal {
			return wrapAll(width, "You are not on the global board.", "Press o to join it.")
		}
		return []string{t.Muted.Render("You are not on this board.")}
	}
	row := board.Rows[viewer]
	standing := t.Muted.Render("UNRANKED")
	switch {
	case row.Rank != nil:
		standing = widget.Label(t, "rank") + " " + t.Strong.Render("#"+strconv.Itoa(*row.Rank))
	case board.RanksHidden:
		standing = t.Muted.Render("RANKS HIDDEN")
	}
	note, noteStyle := standingNote(t, board, row)
	if noteStyle != nil {
		note = noteStyle.Render(note)
	}
	lines := widget.Spread(t.Accent.Render(widget.Truncate(t, memberName(row), max(width-14, 8))), standing, width)
	lines = append(lines, widget.Spread(t.Strong.Render(boardValue(g, board.Metric, row.Value))+" "+t.Muted.Render(boardUnit(board.Metric)), note, width)...)
	if previous := row.Trend.PreviousValue; previous.Value != nil {
		lines = append(lines, widget.Spread(widget.Label(t, comparisonWords(board)), t.Muted.Render(boardValue(g, board.Metric, previous)), width)...)
	}
	return lines
}

// scopeLines describes the board's scope and what the viewer can do in it:
// copy an invite to a group they own, or join or leave the global board.
func (m Model) scopeLines(board *multiplayer.Board, viewer, width int) []string {
	t := m.theme
	g := t.Glyphs
	field := func(label, value, hint string) string {
		left := widget.PadRight(widget.Label(t, label), 9) + value
		return widget.Spread(left, t.Muted.Render(hint), width)[0]
	}
	scopes := m.scopes()
	lines := []string{field("scope", strconv.Itoa(indexOf(scopes, m.currentScope())+1)+" of "+strconv.Itoa(len(scopes)), "g next")}
	lines = append(lines, field("members", strconv.Itoa(max(board.TotalMembers, len(board.Rows))), ""))
	if board.Scope == multiplayer.ScopeGlobal {
		if viewer >= 0 {
			lines = append(lines, field("listed", "yes", "o leave"))
		} else {
			lines = append(lines, field("listed", "no", "o join"))
		}
		if board.MinRankedMembers > 0 {
			lines = append(lines, field("ranks", "from "+strconv.Itoa(board.MinRankedMembers)+" ranked", ""))
		}
		return lines
	}
	group := m.board.snapshot.Selected
	lines = append(lines, field("role", cleanLine(group.Role), ""))
	if group.Visibility != "" {
		lines = append(lines, field("board", cleanLine(group.Visibility), ""))
	}
	switch invite, created := m.board.invites[group.ID]; {
	case created:
		// The code is shown whole, on its own lines: a cut code is useless.
		lines = append(lines, field("invite", "", "y copy"))
		lines = append(lines, widget.Wrap(cleanLine(invite.Code), width)...)
		lines = append(lines, t.Muted.Render(widget.Truncate(t, "expires "+until(invite.ExpiresAt.Sub(board.GeneratedAt))+" "+g.Separator+" "+plural(invite.MaxUses, "use", "uses"), width)))
	case group.Role == "owner":
		lines = append(lines, field("invite", "", "y create and copy"))
	}
	return lines
}

// summaryLines is the rail collapsed for narrow terminals: the viewer's
// standing on one line, and an invite created in this session on another.
func (m Model) summaryLines(board *multiplayer.Board, viewer, width int) []string {
	t := m.theme
	g := t.Glyphs
	separator := " " + g.Separator + " "
	var lines []string
	if viewer < 0 {
		lines = append(lines, t.Muted.Render("You are not on this board."))
	} else {
		row := board.Rows[viewer]
		parts := []string{memberName(row)}
		switch {
		case row.Rank != nil:
			parts = append(parts, "RANK #"+strconv.Itoa(*row.Rank))
		case board.RanksHidden:
			parts = append(parts, "RANKS HIDDEN")
		default:
			parts = append(parts, "UNRANKED")
		}
		parts = append(parts, boardValue(g, board.Metric, row.Value)+" "+boardUnit(board.Metric))
		if note, _ := standingNote(t, board, row); note != "" {
			parts = append(parts, note)
		}
		lines = append(lines, t.Accent.Render(widget.Truncate(t, strings.Join(parts, separator), width)))
	}
	if invite, created := m.board.invites[m.board.snapshot.Selected.ID]; created && board.Scope == multiplayer.ScopeGroup {
		lines = append(lines, widget.Wrap("INVITE "+cleanLine(invite.Code)+separator+"y copy", width)...)
	}
	return lines
}

// selectedLines describes the row under the cursor: why it stands where it
// does, what it was in the comparison window, and what its usage is made of.
func selectedLines(t theme.Theme, board *multiplayer.Board, row multiplayer.Row, width int) []string {
	g := t.Glyphs
	separator := " " + g.Separator + " "
	lines := []string{widget.Rule(t, memberName(row), "", width)}
	say := func(style lipgloss.Style, text string) {
		for _, line := range widget.Wrap(text, width) {
			lines = append(lines, style.Render(line))
		}
	}

	value := boardValue(g, board.Metric, row.Value) + " " + boardUnit(board.Metric)
	if previous := row.Trend.PreviousValue; previous.Value != nil {
		value += separator + comparisonWords(board) + " " + boardValue(g, board.Metric, previous)
	}
	switch {
	case row.Rank != nil:
		standing := "Rank #" + strconv.Itoa(*row.Rank)
		if row.Trend.PreviousRank != nil {
			standing += ", was #" + strconv.Itoa(*row.Trend.PreviousRank) + " " + comparisonWords(board)
		} else if row.Trend.New {
			standing += ", new since " + comparisonWords(board)
		}
		say(t.Text, standing+separator+value)
	default:
		say(t.Text, value)
		// Hidden ranks are a fact about the board, already stated under it.
		if row.UnrankedReason != multiplayer.UnrankedScopeTooSmall {
			say(t.Muted, unrankedSentence(board, row))
		}
	}

	var context []string
	if model := row.MostUsedModel; model != nil && model.ModelID != "" {
		context = append(context, "mostly "+cleanLine(model.ModelID)+" ("+cleanLine(model.Resolution)+")")
	}
	sources := make([]string, 0, len(row.SourceMix))
	for _, source := range row.SourceMix {
		sources = append(sources, sourceName(cleanLine(source.Source))+" "+boardValue(g, multiplayer.MetricTotalTokens, source.TotalTokens))
	}
	if len(sources) > 0 {
		context = append(context, strings.Join(sources, ", "))
	}
	if row.LastSyncedAt != nil {
		context = append(context, "synced "+age(row.LastSyncedAt, board.GeneratedAt))
	}
	if len(context) > 0 {
		say(t.Muted, strings.Join(context, separator))
	}

	var limits []string
	if count := row.Limitations.UnknownModelBuckets; count > 0 {
		limits = append(limits, strconv.FormatInt(count, 10)+" with an unknown model")
	}
	if count := row.Limitations.InferredModelBuckets; count > 0 {
		limits = append(limits, strconv.FormatInt(count, 10)+" with an inferred model")
	}
	if count := row.Limitations.APIEstimateUnavailableBuckets; count > 0 {
		limits = append(limits, strconv.FormatInt(count, 10)+" without an estimate")
	}
	if len(limits) > 0 {
		say(t.Muted, "Usage buckets: "+strings.Join(limits, ", ")+".")
	}
	if row.Plausibility.DailyTokensAboveLimit {
		say(t.Warning, "! A daily total is above the plausibility limit. Usage is self-reported.")
	}
	return lines
}

// unrankedSentence states the server's reason for a missing rank in words.
func unrankedSentence(board *multiplayer.Board, row multiplayer.Row) string {
	switch row.UnrankedReason {
	case multiplayer.UnrankedCostCoverage:
		share := "Part"
		if row.Coverage.PricedBasisPoints != nil {
			share = numfmt.Percent(*row.Coverage.PricedBasisPoints)
		}
		return "Unranked: " + share + " of these tokens are priced, and an estimate is ranked from " + numfmt.Percent(board.CostRankMinPricedBasisPoints) + "."
	case multiplayer.UnrankedValueUnavailable:
		if board.Metric == multiplayer.MetricAPIEstimate {
			return "Unranked: none of this usage has a price."
		}
		return "Unranked: the token total is unavailable."
	case multiplayer.UnrankedNoUsage:
		return "No usage in this period."
	case "":
		return "Unranked."
	default:
		return "Unranked: " + cleanLine(string(row.UnrankedReason)) + "."
	}
}

// boardPromptLines is the inline prompt, when one is open.
func (m Model) boardPromptLines(width int) []string {
	t := m.theme
	separator := " " + t.Glyphs.Separator + " "
	var question, hint string
	switch m.board.prompt.action {
	case promptCreate:
		question, hint = "Create group name: "+m.board.prompt.value+"_", "enter create"+separator+"esc cancel"
	case promptJoin:
		// An invite code is a secret: never echo it.
		question = "Join with invite code: " + strings.Repeat("*", len([]rune(m.board.prompt.value))) + "_"
		hint = "enter join" + separator + "esc cancel" + separator + "code hidden"
	case promptGlobalJoin:
		question = "Join the global board? Your GitHub login and totals become visible to every signed-in user."
		hint = "y join" + separator + "esc cancel"
	case promptGlobalLeave:
		question, hint = "Leave the global board? Your row is removed from it.", "y leave"+separator+"esc cancel"
	default:
		return nil
	}
	var lines []string
	for _, line := range widget.Wrap(question, width) {
		lines = append(lines, t.Accent.Render(line))
	}
	return append(lines, t.Muted.Render(hint), "")
}

// scopes lists what g cycles through: each group, then the global board.
func (m Model) scopes() []string {
	scopes := make([]string, 0, len(m.board.snapshot.Groups)+1)
	for _, group := range m.board.snapshot.Groups {
		scopes = append(scopes, group.ID)
	}
	return append(scopes, multiplayer.GlobalScope)
}

// currentScope is the scope the loaded snapshot shows.
func (m Model) currentScope() string {
	if m.board.snapshot.Global {
		return multiplayer.GlobalScope
	}
	return m.board.snapshot.Selected.ID
}

func wrapAll(width int, paragraphs ...string) []string {
	var lines []string
	for _, paragraph := range paragraphs {
		lines = append(lines, widget.Wrap(paragraph, width)...)
	}
	return lines
}
