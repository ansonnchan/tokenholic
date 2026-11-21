package tui

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/ansonnchan/tokenholic/internal/multiplayer"
	tea "github.com/charmbracelet/bubbletea"
)

// flashDuration is how long a success message stays before the view
// returns to its usual state.
const flashDuration = 4 * time.Second

func (m Model) loadBoard(requestID uint64) tea.Cmd {
	if m.multiplayer == nil {
		return func() tea.Msg { return boardLoadResult{requestID: requestID, err: errMultiplayerNotConfigured} }
	}
	scope, period, metric := m.board.scope, m.board.period, m.board.metric
	return func() tea.Msg {
		snapshot, err := m.multiplayer.Load(m.ctx, scope, period, metric)
		return boardLoadResult{requestID: requestID, snapshot: snapshot, err: err}
	}
}

// reloadBoard loads the current scope, period, and metric again, keeping the
// board on screen until the answer arrives.
func (m Model) reloadBoard() (tea.Model, tea.Cmd) {
	m.help = false
	m.board.loading = true
	m.board.request++
	return m, m.withTick(m.loadBoard(m.board.request))
}

func (m Model) applyBoardLoad(message boardLoadResult) Model {
	// A slow load must not replace the result of a newer one.
	if message.requestID != m.board.request {
		return m
	}
	m.board.loading = false
	switch {
	case errors.Is(message.err, errMultiplayerNotConfigured):
		m.board.gate, m.board.loaded, m.board.failure = gateNotConfigured, true, ""
	case errors.Is(message.err, multiplayer.ErrSignedOut):
		m.board.gate, m.board.loaded, m.board.failure = gateSignedOut, true, ""
		m.board.snapshot = multiplayer.Snapshot{}
	case message.err != nil:
		// The error can carry a URL or a response body; show a stable
		// message and keep the last board under it.
		m.board.failure = "Could not load the board"
	default:
		m.board.gate, m.board.loaded, m.board.failure = gateOpen, true, ""
		m.board.snapshot = message.snapshot
		m.board.scope = m.currentScope()
		if board := message.snapshot.Board; board != nil {
			// Show what the server answered for, and keep the cursor on the
			// viewer's row until they move it.
			m.board.period, m.board.metric = board.Period, board.Metric
			if viewer := viewerRow(board); !m.board.moved && viewer >= 0 {
				m.board.cursor = viewer
			}
			m.board.cursor = max(0, min(m.board.cursor, len(board.Rows)-1))
		}
	}
	return m
}

// boardKey handles the keys that mean something only in the board area.
func (m Model) boardKey(key string) (bool, tea.Model, tea.Cmd) {
	if m.board.gate != gateOpen || m.multiplayer == nil {
		return false, m, nil
	}
	board := m.board.snapshot.Board
	switch key {
	case "g", "G":
		scopes := m.scopes()
		step := 1
		if key == "G" {
			step = len(scopes) - 1
		}
		m.board.scope = scopes[(max(indexOf(scopes, m.currentScope()), 0)+step)%len(scopes)]
		if m.board.snapshot.Board == nil && !m.board.snapshot.Global {
			// With no group there is only one place to go.
			m.board.scope = multiplayer.GlobalScope
		}
		m.board.cursor, m.board.moved = 0, false
		model, command := m.reloadBoard()
		return true, model, command
	case "p":
		m.board.period = boardPeriods[(indexOf(boardPeriods, m.board.period)+1)%len(boardPeriods)]
		model, command := m.reloadBoard()
		return true, model, command
	case "m":
		m.board.metric = boardMetrics[(indexOf(boardMetrics, m.board.metric)+1)%len(boardMetrics)]
		model, command := m.reloadBoard()
		return true, model, command
	case "c":
		m.board.prompt = boardPrompt{action: promptCreate}
		return true, m, nil
	case "i":
		m.board.prompt = boardPrompt{action: promptJoin}
		return true, m, nil
	case "o":
		if board == nil || board.Scope != multiplayer.ScopeGlobal {
			return true, m, nil
		}
		m.board.prompt = boardPrompt{action: promptGlobalJoin}
		if viewerRow(board) >= 0 {
			m.board.prompt = boardPrompt{action: promptGlobalLeave}
		}
		return true, m, nil
	case "y":
		model, command := m.copyInvite()
		return true, model, command
	default:
		return false, m, nil
	}
}

// copyInvite copies the group's invite code, creating one first when this
// session has none. Only an owner can create one.
func (m Model) copyInvite() (tea.Model, tea.Cmd) {
	board := m.board.snapshot.Board
	group := m.board.snapshot.Selected
	if board == nil || board.Scope != multiplayer.ScopeGroup {
		return m, nil
	}
	if invite, created := m.board.invites[group.ID]; created {
		return m.flashNow(m.copyText(invite.Code, "Invite"))
	}
	if group.Role != "owner" {
		return m.flashNow("Only the group's owner can create an invite")
	}
	m.board.loading = true
	m.board.request++
	requestID := m.board.request
	return m, m.withTick(func() tea.Msg {
		invite, err := m.multiplayer.Invite(m.ctx, group.ID, multiplayer.DefaultInviteLifetime, multiplayer.DefaultInviteUses)
		return boardActionResult{requestID: requestID, failure: "Could not create an invite", invite: &invite, groupID: group.ID, err: err}
	})
}

// copyText copies through the terminal and says what happened. Whether a
// terminal honors the copy cannot be known, so the code also stays on screen.
func (m Model) copyText(text, what string) string {
	if m.clipboard == nil {
		return what + " shown; copying is unavailable here"
	}
	m.clipboard(text)
	return what + " copied"
}

// flashNow shows a short success message and schedules its removal.
func (m Model) flashNow(text string) (tea.Model, tea.Cmd) {
	m.board.flash = text
	m.board.flashID++
	id := m.board.flashID
	return m, tea.Tick(m.flashFor, func(time.Time) tea.Msg { return flashExpired{id: id} })
}

func (m Model) applyBoardAction(message boardActionResult) (tea.Model, tea.Cmd) {
	if message.requestID != m.board.request {
		return m, nil
	}
	if message.err != nil {
		m.board.loading = false
		m.board.failure = message.failure
		return m, nil
	}
	m.board.failure = ""
	var flash tea.Cmd
	switch {
	case message.invite != nil:
		m.board.invites[message.groupID] = *message.invite
		var model tea.Model
		model, flash = m.flashNow(m.copyText(message.invite.Code, "Invite"))
		m = model.(Model)
	case message.flash != "":
		var model tea.Model
		model, flash = m.flashNow(message.flash)
		m = model.(Model)
	}
	if message.scope != "" {
		m.board.scope = message.scope
		m.board.cursor, m.board.moved = 0, false
	}
	model, reload := m.reloadBoard()
	return model, tea.Batch(reload, flash)
}

// updateBoardPrompt handles keys while an inline prompt is open. Every key
// is input to the prompt: none of them navigates or quits.
func (m Model) updateBoardPrompt(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	action := m.board.prompt.action
	confirm := action == promptGlobalJoin || action == promptGlobalLeave
	switch {
	case key.Type == tea.KeyEsc:
		m.board.prompt = boardPrompt{}
		return m, nil
	case confirm:
		// Only an explicit y changes visibility; anything else leaves it.
		if key.Type != tea.KeyRunes || string(key.Runes) != "y" {
			m.board.prompt = boardPrompt{}
			return m, nil
		}
		return m.runBoardAction(action, "")
	case key.Type == tea.KeyEnter:
		value := strings.TrimSpace(m.board.prompt.value)
		if value == "" {
			return m, nil
		}
		return m.runBoardAction(action, value)
	case key.Type == tea.KeyBackspace || key.Type == tea.KeyDelete:
		if runes := []rune(m.board.prompt.value); len(runes) > 0 {
			m.board.prompt.value = string(runes[:len(runes)-1])
		}
		return m, nil
	case key.Type == tea.KeyRunes || key.Type == tea.KeySpace:
		limit := 80
		if action == promptJoin {
			limit = 128
		}
		runes := []rune(m.board.prompt.value)
		typed := key.Runes
		if key.Type == tea.KeySpace {
			typed = []rune{' '}
		}
		for _, character := range typed {
			if len(runes) >= limit || unicode.IsControl(character) {
				continue
			}
			runes = append(runes, character)
		}
		m.board.prompt.value = string(runes)
		return m, nil
	default:
		return m, nil
	}
}

// runBoardAction closes the prompt and performs what it asked for.
func (m Model) runBoardAction(action, value string) (tea.Model, tea.Cmd) {
	m.board.prompt = boardPrompt{}
	m.board.loading = true
	m.board.request++
	requestID := m.board.request
	return m, m.withTick(func() tea.Msg {
		result := boardActionResult{requestID: requestID}
		switch action {
		case promptCreate:
			group, err := m.multiplayer.Create(m.ctx, value)
			result.failure, result.scope, result.flash, result.err = "Could not create the group", group.ID, "Group created", err
		case promptJoin:
			group, err := m.multiplayer.Join(m.ctx, value)
			result.failure, result.scope, result.flash, result.err = "Could not join with that invite", group.ID, "Joined", err
		case promptGlobalJoin:
			result.failure, result.flash = "Could not join the global board", "You are on the global board"
			result.err = m.multiplayer.SetGlobalParticipation(m.ctx, true)
		case promptGlobalLeave:
			result.failure, result.flash = "Could not leave the global board", "You left the global board"
			result.err = m.multiplayer.SetGlobalParticipation(m.ctx, false)
		}
		return result
	})
}

// terminalClipboard copies through OSC 52, which most terminals accept and
// which works over SSH. The sequence draws nothing, so writing it beside the
// renderer cannot disturb the frame.
func terminalClipboard(output io.Writer) func(string) {
	return func(text string) {
		fmt.Fprintf(output, "\x1b]52;c;%s\x07", base64.StdEncoding.EncodeToString([]byte(text)))
	}
}
