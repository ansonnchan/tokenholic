package tui

import (
	"strconv"

	"github.com/ansonnchan/tokenholic/internal/tui/widget"
)

// settingsView explains what tokenholic keeps and how this device is set up.
func (m Model) settingsView(width int) []string {
	t := m.theme
	if width >= wideWidth {
		right := (width - 1) / 2
		left := width - 1 - right
		return widget.Columns(1,
			widget.Panel(t, "privacy", left, m.privacyLines(widget.InnerWidth(left))),
			append(
				widget.Panel(t, "this device", right, m.deviceLines(widget.InnerWidth(right))),
				widget.Panel(t, "display", right, m.displayLines(widget.InnerWidth(right)))...,
			),
		)
	}
	inner := widget.InnerWidth(width)
	lines := widget.Panel(t, "privacy", width, m.privacyLines(inner))
	lines = append(lines, widget.Panel(t, "this device", width, m.deviceLines(inner))...)
	return append(lines, widget.Panel(t, "display", width, m.displayLines(inner))...)
}

func (m Model) privacyLines(width int) []string {
	t := m.theme
	var lines []string
	paragraph := func(label, text string) {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, widget.Label(t, label))
		lines = append(lines, widget.Wrap(text, width)...)
	}
	paragraph("stored on this device", "Normalized usage counts only: when, which coding agent, which model, and how many tokens in each category.")
	paragraph("never stored", "Prompts, responses, source code, command text, or credentials.")
	paragraph("never stored", "Paths, project names, repository names, or branch names.")
	paragraph("uploaded", "Nothing, until you claim an account. Then the same counts, and nothing else. `tokenholic show-data` prints the exact payload.")
	return lines
}

func (m Model) deviceLines(width int) []string {
	t := m.theme
	diagnostics := m.dashboard.Diagnostics
	field := func(label, value string) string {
		return widget.PadRight(widget.Label(t, label), 13) + widget.Truncate(t, value, max(width-13, 1))
	}
	integrity := "ok"
	if !diagnostics.IntegrityOK {
		integrity = "failed; run `tokenholic doctor`"
	}
	return []string{
		field("account", m.accountText()),
		field("timezone", m.dashboard.Timezone),
		field("last scan", age(diagnostics.LastSuccessfulCollection, m.dashboard.GeneratedAt)),
		field("to upload", plural(diagnostics.PendingOutbox, "bucket", "buckets")),
		field("database", "schema "+strconv.Itoa(diagnostics.SchemaVersion)+" of "+strconv.Itoa(diagnostics.SupportedSchemaVersion)+" "+t.Glyphs.Separator+" integrity "+integrity),
	}
}

// accountText says who this device is signed in as, or how to sign in.
func (m Model) accountText() string {
	switch {
	case m.account == nil:
		return "local only; no API is configured"
	case !m.accountKnown:
		return "not checked yet"
	case m.login == "":
		return "not signed in; press c to claim"
	default:
		return "signed in as " + memberLogin(m.login)
	}
}

func (m Model) displayLines(width int) []string {
	t := m.theme
	var lines []string
	for _, setting := range [][2]string{
		{"TOKENHOLIC_THEME", "light or dark, when the terminal's background is detected wrongly"},
		{"NO_COLOR", "any value turns all styling off"},
		{"TOKENHOLIC_ASCII", "any value replaces symbols with ASCII"},
	} {
		lines = append(lines, t.Text.Render(setting[0]))
		for _, line := range widget.Wrap(setting[1], max(width-2, 8)) {
			lines = append(lines, "  "+t.Muted.Render(line))
		}
	}
	return lines
}
