package tui

import (
	"strconv"
	"strings"

	"github.com/ansonnchan/tokenholic/internal/multiplayer"
	"github.com/ansonnchan/tokenholic/internal/tui/widget"
)

// tabs are the areas in key order: 1 through 5.
var tabs = []string{"HOME", "BOARDS", "MODELS", "SOURCES", "SETTINGS"}

// View renders the current frame. It reads only the model.
//
// A theme with color paints the frame onto its own canvas, covering the
// whole terminal, so the TUI looks the same whatever colors the terminal
// has. The painted frame is exactly as tall as the terminal and so ends
// without a line break, which would push its first line off the top.
func (m Model) View() string {
	lines := m.frame()
	if !m.theme.Color || m.width <= 0 || m.height <= 0 {
		return strings.Join(lines, "\n") + "\n"
	}
	return strings.Join(m.theme.Paint(lines, m.width, m.height), "\n")
}

// frame lays the view out as lines no wider than the terminal: the header,
// the active area, notices, and the key bar. When the area is taller than
// the terminal it is clipped to a scrolled window.
func (m Model) frame() []string {
	width := m.viewWidth()
	t := m.theme
	if width < MinimumWidth {
		lines := []string{widget.Wordmark(t), ""}
		lines = append(lines, widget.Notice(t, widget.Warning, "Terminal is too narrow. Resize to at least "+strconv.Itoa(MinimumWidth)+" columns.", width)...)
		lines = append(lines, "", widget.KeyBar(t, []widget.Hint{{Key: "?", Label: "help", Keep: true}, {Key: "q", Label: "quit", Keep: true}}, width))
		if m.help {
			lines = append(lines, "", t.Muted.Render("1"+t.Glyphs.Range+"5 views"), t.Muted.Render("r refresh"))
		}
		return lines
	}

	header, notices, available := m.layout(width)
	footer := widget.KeyBar(t, m.hints(), width)
	body := m.body(width, available)
	if available > 0 && len(body) > available {
		// Keep one line to say how much is out of view.
		offset := min(m.scroll, len(body)-available+1)
		shown := body[offset : offset+available-1]
		below := len(body) - offset - len(shown)
		position := plural(below, "more line", "more lines") + " below"
		if offset > 0 {
			position = strconv.Itoa(offset) + " above " + t.Glyphs.Separator + " " + strconv.Itoa(below) + " below"
		}
		if m.help || m.section != ModelsSection && m.section != BoardsSection {
			// In the other areas j and k move a selection instead.
			position += " " + t.Glyphs.Separator + " j/k scroll"
		}
		body = append(append([]string(nil), shown...), t.Muted.Render(position))
	}
	lines := append(header, body...)
	lines = append(lines, notices...)
	return append(lines, footer)
}

// layout returns the frame's fixed parts and the rows left for the area
// between them, which is zero while the terminal's height is unknown.
func (m Model) layout(width int) (header, notices []string, available int) {
	header = append(widget.Header(m.theme, tabs, int(m.section), m.status(width), width, stackWidth), "")
	if m.welcome {
		// The first launch has no tabs. Its own view draws the wordmark;
		// an overlay opened from it gets the plain one.
		header = nil
		if m.overlay() {
			header = []string{widget.Wordmark(m.theme), ""}
		}
	}
	notices = m.notices(width)
	if m.height > 0 {
		// One row is the key bar; never squeeze the area below three rows.
		available = max(m.height-len(header)-len(notices)-1, 3)
	}
	return header, notices, available
}

// maxScroll is how far the active area can scroll at the current size.
func (m Model) maxScroll() int {
	width := m.viewWidth()
	if m.height <= 0 || width < MinimumWidth {
		return 0
	}
	_, _, available := m.layout(width)
	if rows := len(m.body(width, available)); rows > available {
		return rows - available + 1
	}
	return 0
}

// viewWidth is the terminal width, or a default before the first size
// message arrives.
func (m Model) viewWidth() int {
	if m.width <= 0 {
		return stackWidth
	}
	return m.width
}

// body renders the active area. height is the rows available, or zero when
// unknown; only views that window their own lists use it.
func (m Model) body(width, height int) []string {
	t := m.theme
	switch {
	case m.claim.stage != claimClosed:
		return m.claimView(width)
	case m.share.open:
		return m.shareView(width)
	case m.help:
		return m.helpView(width)
	case m.welcome:
		return m.welcomeView(width)
	}
	if m.section == BoardsSection {
		return m.boardsView(width, height)
	}
	switch {
	case m.app == nil:
		return widget.Panel(t, tabs[m.section], width, []string{t.Muted.Render("No local data to show.")})
	case !m.loaded && m.localError != "":
		return widget.Panel(t, tabs[m.section], width, []string{t.Muted.Render("Local usage could not be read.")})
	case !m.loaded:
		return widget.Panel(t, tabs[m.section], width, []string{t.Muted.Render(m.spin() + " Reading local usage")})
	}
	switch m.section {
	case ModelsSection:
		return m.modelsView(width, height)
	case SourcesSection:
		return m.sourcesView(width)
	case SettingsSection:
		return m.settingsView(width)
	default:
		return m.homeView(width)
	}
}

// status is the header's right-hand text: a spinner while work runs, and
// otherwise how fresh the local scan is.
func (m Model) status(width int) string {
	g := m.theme.Glyphs
	switch {
	case m.section == BoardsSection && m.board.loading:
		return m.spin() + " loading"
	case m.refreshing:
		return m.spin() + " scanning"
	case !m.loaded:
		return ""
	case m.notice != "":
		return m.notice
	}
	diagnostics := m.dashboard.Diagnostics
	if diagnostics.LastSuccessfulCollection == nil {
		return "not scanned yet"
	}
	status := "scanned " + age(diagnostics.LastSuccessfulCollection, m.dashboard.GeneratedAt)
	if diagnostics.PendingOutbox > 0 && width >= wideWidth {
		status += " " + g.Separator + " " + strconv.Itoa(diagnostics.PendingOutbox) + " to upload"
	}
	return status
}

func (m Model) spin() string {
	frames := m.theme.Glyphs.Spinner
	return frames[m.spinner%len(frames)]
}

// notices are the lines between the area and the key bar: a failed load or
// refresh, and sources that need the user's attention.
func (m Model) notices(width int) []string {
	t := m.theme
	var lines []string
	if m.overlay() || m.welcome {
		return nil
	}
	if m.section == BoardsSection {
		if m.board.flash != "" {
			return widget.Notice(t, widget.Success, m.board.flash, width)
		}
		return nil
	}
	if m.localError != "" {
		lines = append(lines, widget.Notice(t, widget.Failure, m.localError+". Press r to retry, or run `tokenholic doctor`.", width)...)
	}
	if count := unhealthyAdapters(m.dashboard.Diagnostics.AdapterHealth); m.loaded && count > 0 && m.section != SourcesSection {
		lines = append(lines, widget.Notice(t, widget.Warning, plural(count, "source needs", "sources need")+" attention; see 4 SOURCES.", width)...)
	}
	if m.section == HomeSection && m.loaded && m.canClaim() {
		lines = append(lines, m.claimChip(width)...)
	}
	return lines
}

// overlay reports whether something is drawn over the active area and
// takes its keys.
func (m Model) overlay() bool {
	return m.help || m.share.open || m.claim.stage != claimClosed
}

// hints is the key bar for the active area. Hints marked Keep survive the
// narrowest width.
func (m Model) hints() []widget.Hint {
	g := m.theme.Glyphs
	quit := widget.Hint{Key: "q", Label: "quit", Keep: true}
	switch {
	case m.claim.stage != claimClosed:
		return []widget.Hint{{Key: "esc", Label: "cancel", Keep: true}, {Key: "ctrl+c", Label: "quit", Keep: true}}
	case m.share.open:
		return []widget.Hint{{Key: "o", Label: "open", Keep: true}, {Key: "y", Label: "copy", Keep: true}, {Key: "esc", Label: "close", Keep: true}}
	case m.welcome && !m.help:
		hints := []widget.Hint{{Key: "enter", Label: "dashboard", Keep: true}}
		if m.canClaim() {
			hints = append(hints, widget.Hint{Key: "c", Label: "claim", Keep: true})
		}
		return append(hints, widget.Hint{Key: "r", Label: "scan again"}, widget.Hint{Key: "s", Label: "share"}, widget.Hint{Key: "?", Label: "help"}, quit)
	}
	hints := []widget.Hint{{Key: "1" + g.Range + "5", Label: "views", Keep: true}}
	switch m.section {
	case HomeSection:
		hints = append(hints, widget.Hint{Key: "p", Label: "period"}, widget.Hint{Key: "d", Label: "detail"}, widget.Hint{Key: "s", Label: "share"}, widget.Hint{Key: "j/k", Label: "scroll"})
	case ModelsSection:
		hints = append(hints, widget.Hint{Key: "p", Label: "period"}, widget.Hint{Key: "j/k", Label: "move"})
	case SourcesSection:
		hints = append(hints, widget.Hint{Key: "p", Label: "period"}, widget.Hint{Key: "j/k", Label: "scroll"})
	case SettingsSection:
		hints = append(hints, widget.Hint{Key: "j/k", Label: "scroll"})
	case BoardsSection:
		if m.board.gate != gateOpen || m.multiplayer == nil {
			break
		}
		// The scope's own action: an invite for a group, opting in or out
		// for the global board.
		action := widget.Hint{Key: "y", Label: "invite"}
		if board := m.board.snapshot.Board; board != nil && board.Scope == multiplayer.ScopeGlobal {
			action = widget.Hint{Key: "o", Label: "join global"}
			if viewerRow(board) >= 0 {
				action.Label = "leave global"
			}
		}
		hints = append(hints,
			widget.Hint{Key: "g", Label: "scope"}, widget.Hint{Key: "p", Label: "period"}, widget.Hint{Key: "m", Label: "metric"},
			widget.Hint{Key: "j/k", Label: "move"}, action, widget.Hint{Key: "s", Label: "share"},
			widget.Hint{Key: "c", Label: "create"}, widget.Hint{Key: "i", Label: "join"},
		)
	}
	// Every key that does something here is named, so none has to be found
	// by accident. When the bar is too narrow, hints drop from the end, and
	// help still lists them all.
	if m.section != BoardsSection && m.canClaim() {
		hints = append(hints, widget.Hint{Key: "c", Label: "claim"})
	}
	hints = append(hints, widget.Hint{Key: "r", Label: "refresh"})
	if m.themes != nil && m.theme.Color {
		hints = append(hints, widget.Hint{Key: "t", Label: m.theme.Mode.Other().String() + " theme"})
	}
	return append(hints,
		widget.Hint{Key: "?", Label: "help", Keep: true},
		widget.Hint{Key: "q", Label: "quit", Keep: true},
	)
}

// helpView lists the keys and explains how to read the numbers.
func (m Model) helpView(width int) []string {
	t := m.theme
	g := t.Glyphs
	keys := [][2]string{
		{"1" + g.Range + "5", "switch view"},
		{"tab", "next view; shift+tab goes back"},
		{"p", "change period"},
		{"j / k", "move the selection or scroll"},
		{"d", "show exact token categories on HOME"},
		{"s", "draft a post of your totals to share"},
		{"c", "claim your spot: sign in with GitHub, when signed out"},
		{"r", "scan local usage again"},
		{"t", "switch between the dark and light theme"},
		{"?", "close help"},
		{"q", "quit"},
	}
	if m.section == BoardsSection {
		keys = [][2]string{
			{"g", "next board: each of your groups, then GLOBAL"},
			{"p", "change period"},
			{"m", "rank by estimate or by tokens"},
			{"j / k", "move through the standings"},
			{"y", "copy an invite to this group; the owner creates it"},
			{"c / i", "create a group / join with an invite code"},
			{"o", "join or leave the global board"},
			{"s", "draft a post of your totals to share"},
			{"r", "reload"},
			{"t", "switch between the dark and light theme"},
			{"?", "close help"},
			{"q", "quit"},
		}
	}
	legend := [][2]string{
		{"$ EST", "what the tokens would cost at API list prices. It is an estimate, not what you paid."},
		{g.AtLeast + "1.2M", "a lower bound: part of the usage was not reported or has no price."},
		{g.Unavailable, "not available. It never means zero."},
		{"PRICED", "the share of tokens the estimate covers."},
	}
	if m.section == BoardsSection {
		legend = append(legend,
			[2]string{g.Up + " 2", "rank moved, against the same point of the previous period."},
			[2]string{"!", "a daily total above the plausibility limit. Usage is self-reported."},
		)
	}
	render := func(rows [][2]string, inner int) []string {
		var lines []string
		for _, row := range rows {
			for index, line := range widget.Wrap(row[1], max(inner-8, 8)) {
				label := ""
				if index == 0 {
					label = row[0]
				}
				lines = append(lines, widget.PadRight(t.Text.Render(label), 8)+t.Muted.Render(line))
			}
		}
		return lines
	}
	if width >= wideWidth {
		right := (width - 1) / 2
		left := width - 1 - right
		return widget.Columns(1,
			widget.Panel(t, "keys", left, render(keys, widget.InnerWidth(left))),
			widget.Panel(t, "reading the numbers", right, render(legend, widget.InnerWidth(right))),
		)
	}
	inner := widget.InnerWidth(width)
	return append(widget.Panel(t, "keys", width, render(keys, inner)), widget.Panel(t, "reading the numbers", width, render(legend, inner))...)
}
