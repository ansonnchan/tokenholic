package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/ansonnchan/tokenholic/internal/application"
	"github.com/ansonnchan/tokenholic/internal/multiplayer"
	"github.com/ansonnchan/tokenholic/internal/store"
	"github.com/ansonnchan/tokenholic/internal/tui/theme"
	tea "github.com/charmbracelet/bubbletea"
)

const (
	// MinimumWidth is the narrowest supported terminal width.
	MinimumWidth = 48
	// stackWidth is where the header fits on one line.
	stackWidth = 88
	// wideWidth is where a view gains a second column.
	wideWidth = 120
	// spinnerInterval paces the one spinner shown while work runs.
	spinnerInterval = 120 * time.Millisecond
)

// Section identifies one top-level area, in key order.
type Section int

const (
	// HomeSection is the personal dashboard.
	HomeSection Section = iota
	// BoardsSection shows group standings.
	BoardsSection
	// ModelsSection shows per-model usage and attribution.
	ModelsSection
	// SourcesSection shows local adapter health and usage by source.
	SourcesSection
	// SettingsSection explains privacy and this device's setup.
	SettingsSection
)

const sectionCount = 5

// Application supplies committed local state and owns refresh behavior.
// Implementations keep collection and persistence outside the TUI package.
type Application interface {
	Load(context.Context) (application.Dashboard, error)
	Refresh(context.Context) error
}

// Multiplayer supplies authenticated board snapshots and the few changes the
// board area makes. HTTP, credentials, and ranking stay behind it.
type Multiplayer interface {
	// Load returns the scope list and one board: a group by ID, the global
	// board for multiplayer.GlobalScope, or the first group for "".
	Load(context.Context, string, multiplayer.Period, multiplayer.Metric) (multiplayer.Snapshot, error)
	Create(context.Context, string) (multiplayer.Group, error)
	Join(context.Context, string) (multiplayer.Group, error)
	Invite(context.Context, string, time.Duration, int) (multiplayer.Invite, error)
	SetGlobalParticipation(context.Context, bool) error
}

// Config controls environment-sensitive shell behavior.
type Config struct {
	// NoColor disables all styling; ASCII swaps glyphs for their fallbacks;
	// Mode is the look to start in, light unless set. They are independent.
	NoColor     bool
	ASCII       bool
	Mode        theme.Mode
	Multiplayer Multiplayer
	// Theme is the theme to start with. Run builds it from the output and
	// Mode; tests and previews set a fixed one.
	Theme *theme.Theme
	// Themes builds the theme for a mode, which is what lets the user switch
	// while the TUI runs. Run sets it from the output; nil means the look
	// cannot change.
	Themes func(theme.Mode) theme.Theme
	// SaveMode remembers a switch for the next run. nil means a switch lasts
	// until the TUI quits.
	SaveMode func(theme.Mode) error
	// Clipboard copies text for the user. Run sets it to an OSC 52 write to
	// the output; nil means copying is unavailable.
	Clipboard func(string)
	// Account signs in and uploads; nil means no API is configured and no
	// claim is offered.
	Account Account
	// Autosync turns on hourly background sync; nil means a claim does not
	// offer it.
	Autosync Autosync
	// OpenURL shows a link in the user's browser; nil means it cannot.
	OpenURL func(string) error
	// Notice replaces the header's freshness text with a fixed label. The
	// demo sets it, so no picture of invented data can pass for real usage.
	Notice string
}

// Model is the Bubble Tea model for the application shell. It holds the last
// stable snapshot of each area so a refresh never blanks the screen.
type Model struct {
	ctx         context.Context
	app         Application
	multiplayer Multiplayer
	theme       theme.Theme
	themes      func(theme.Mode) theme.Theme
	saveMode    func(theme.Mode) error
	// modeUnsaved is true when the last switch could not be remembered.
	modeUnsaved bool
	clipboard   func(string)
	account     Account
	autosync    Autosync
	openURL     func(string) error
	notice      string
	section     Section
	width       int
	height      int
	help        bool
	scroll      int
	spinner     int
	ticking     bool
	tickEvery   time.Duration
	flashFor    time.Duration

	// Local areas share one dashboard and one period.
	dashboard    application.Dashboard
	loaded       bool
	loading      bool
	refreshing   bool
	localError   string
	localRequest uint64
	period       store.Period
	detail       bool
	modelCursor  int

	board boardState

	// login is this device's GitHub login, known once accountKnown is set.
	login        string
	accountKnown bool
	// welcome is the first-launch screen; started records that the first
	// load has been seen, so the automatic scan runs once.
	welcome bool
	started bool
	claim   claimState
	share   shareState
	// sharedLink is printed after the program exits.
	sharedLink string
}

type loadResult struct {
	requestID uint64
	dashboard application.Dashboard
	err       error
}

type refreshResult struct {
	err error
}

type boardLoadResult struct {
	requestID uint64
	snapshot  multiplayer.Snapshot
	err       error
}

// boardActionResult is the outcome of a change made from the board area.
// scope, when set, is the scope to show next.
type boardActionResult struct {
	requestID uint64
	failure   string
	scope     string
	invite    *multiplayer.Invite
	groupID   string
	flash     string
	err       error
}

type flashExpired struct {
	id uint64
}

type spinnerTick struct{}

var errMultiplayerNotConfigured = errors.New("multiplayer is not configured")

// NewModel constructs a shell whose data access is limited to app and the
// multiplayer service in config.
func NewModel(ctx context.Context, app Application, config Config) Model {
	if ctx == nil {
		ctx = context.Background()
	}
	tokens := theme.Fixed(false, true, config.ASCII)
	if config.Theme != nil {
		tokens = *config.Theme
	}
	return Model{
		ctx:          ctx,
		app:          app,
		multiplayer:  config.Multiplayer,
		theme:        tokens,
		themes:       config.Themes,
		saveMode:     config.SaveMode,
		clipboard:    config.Clipboard,
		account:      config.Account,
		autosync:     config.Autosync,
		openURL:      config.OpenURL,
		notice:       config.Notice,
		section:      HomeSection,
		tickEvery:    spinnerInterval,
		flashFor:     flashDuration,
		loading:      app != nil,
		localRequest: 1,
		period:       store.PeriodLast30Days,
		board: boardState{
			period:  multiplayer.PeriodWeek,
			metric:  multiplayer.MetricAPIEstimate,
			invites: map[string]multiplayer.Invite{},
		},
	}
}

// Init loads the first local snapshot.
func (m Model) Init() tea.Cmd {
	var commands []tea.Cmd
	if m.account != nil {
		commands = append(commands, m.loadAccount())
	}
	if m.app != nil {
		commands = append(commands, m.loadLocal(m.localRequest), m.tick())
	}
	return tea.Batch(commands...)
}

// Update applies terminal, key, load, refresh, and spinner messages.
func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
		m.scroll = min(m.scroll, m.maxScroll())
		// The frame fills the terminal exactly, so nothing after it erases
		// what a wider or taller frame left behind. Start each size clean.
		return m, tea.ClearScreen
	case tea.KeyMsg:
		return m.updateKey(message)
	case modeSaved:
		m.modeUnsaved = message.err != nil
		return m, nil
	case spinnerTick:
		if !m.busy() {
			m.ticking = false
			return m, nil
		}
		m.ticking = true
		m.spinner++
		return m, m.tick()
	case loadResult:
		// A slow load must not replace the result of a newer one.
		if message.requestID != m.localRequest {
			return m, nil
		}
		m.loading, m.refreshing = false, false
		if message.err != nil {
			// Keep the last stable dashboard on screen under the notice.
			m.localError = "Could not load local data"
			return m, nil
		}
		m.dashboard, m.loaded, m.localError = message.dashboard, true, ""
		if first := !m.started; first {
			m.started = true
			if m.dashboard.Diagnostics.LastSuccessfulCollection == nil {
				// A first launch has nothing to show yet: scan without
				// being asked, and greet the user with what it finds.
				m.welcome = true
				m.loading, m.refreshing = true, true
				return m, m.withTick(m.refresh())
			}
		}
		m.modelCursor = min(m.modelCursor, max(len(summaryFor(m.dashboard, m.period).Models)-1, 0))
		m.scroll = min(m.scroll, m.maxScroll())
		return m, nil
	case refreshResult:
		if message.err != nil {
			// The error can wrap a private path; show a stable message only.
			m.loading, m.refreshing = false, false
			m.localError = "Could not refresh local data"
			return m, nil
		}
		m.localRequest++
		return m, m.loadLocal(m.localRequest)
	case accountResult:
		// A vault that cannot be read is not the same as being signed out,
		// so a failure offers no claim.
		m.login, m.accountKnown = message.login, message.err == nil
		return m, nil
	case claimStarted, claimCompleted, claimSynced, claimScheduled:
		return m.applyClaim(message)
	case boardLoadResult:
		return m.applyBoardLoad(message), nil
	case boardActionResult:
		return m.applyBoardAction(message)
	case flashExpired:
		if message.id == m.board.flashID {
			m.board.flash = ""
		}
		return m, nil
	default:
		return m, nil
	}
}

func (m Model) updateKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "ctrl+c" {
		return m, tea.Quit
	}
	if m.claim.stage != claimClosed {
		return m.updateClaim(key)
	}
	if m.share.open {
		return m.updateShare(key)
	}
	if m.board.prompt.action != "" {
		return m.updateBoardPrompt(key)
	}
	if m.welcome && key.Type == tea.KeyEnter {
		m.welcome, m.help = false, false
		return m, nil
	}
	boards := m.section == BoardsSection && !m.welcome
	if boards && !m.help {
		if handled, model, command := m.boardKey(key.String()); handled {
			return model, command
		}
	}
	switch key.String() {
	case "q":
		return m, tea.Quit
	case "?":
		m.help = !m.help
		m.scroll = 0
		return m, nil
	case "1", "2", "3", "4", "5":
		return m.activate(Section(key.String()[0] - '1'))
	case "tab":
		return m.activate((m.section + 1) % sectionCount)
	case "shift+tab":
		return m.activate((m.section + sectionCount - 1) % sectionCount)
	case "r":
		if boards {
			return m.reloadBoard()
		}
		if m.app == nil || m.loading {
			return m, nil
		}
		m.loading, m.refreshing = true, true
		return m, m.withTick(m.refresh())
	case "p":
		if boards || m.section == SettingsSection {
			return m, nil
		}
		m.period = periods[(indexOf(periods, m.period)+1)%len(periods)]
		m.modelCursor = 0
		m.scroll = min(m.scroll, m.maxScroll())
		return m, nil
	case "d":
		if m.section == HomeSection {
			m.detail = !m.detail
			m.scroll = min(m.scroll, m.maxScroll())
		}
		return m, nil
	case "c":
		// In BOARDS a signed-in user's c creates a group, handled above.
		if m.canClaim() {
			return m.openClaim()
		}
		return m, nil
	case "s":
		if m.welcome || m.section == HomeSection || m.section == BoardsSection {
			return m.openShare()
		}
		return m, nil
	case "t":
		return m.switchTheme()
	case "j", "down":
		return m.move(1)
	case "k", "up":
		return m.move(-1)
	default:
		return m, nil
	}
}

// move steps the active area's selection, or scrolls it when it has none.
func (m Model) move(delta int) (tea.Model, tea.Cmd) {
	switch {
	case m.help:
		m.scroll = max(0, min(m.scroll+delta, m.maxScroll()))
	case m.section == BoardsSection:
		if board := m.board.snapshot.Board; board != nil && len(board.Rows) > 0 {
			m.board.cursor = max(0, min(m.board.cursor+delta, len(board.Rows)-1))
			m.board.moved = true
		}
	case m.section == ModelsSection:
		count := len(summaryFor(m.dashboard, m.period).Models)
		m.modelCursor = max(0, min(m.modelCursor+delta, count-1))
	default:
		m.scroll = max(0, min(m.scroll+delta, m.maxScroll()))
	}
	return m, nil
}

func (m Model) activate(section Section) (tea.Model, tea.Cmd) {
	if section == m.section && !m.welcome {
		return m, nil
	}
	m.welcome = false
	m.section = section
	m.help = false
	m.scroll = 0
	if section == BoardsSection {
		return m.reloadBoard()
	}
	if m.app == nil || m.loading {
		return m, nil
	}
	// Re-read committed state so another process's sync shows up, keeping
	// the current dashboard on screen meanwhile.
	m.loading = true
	m.localRequest++
	return m, m.loadLocal(m.localRequest)
}

// busy reports whether the spinner has anything to show.
func (m Model) busy() bool {
	return m.refreshing || !m.loaded && m.loading || m.board.loading
}

// withTick starts the spinner alongside command unless it is already running.
func (m *Model) withTick(command tea.Cmd) tea.Cmd {
	if m.ticking {
		return command
	}
	m.ticking = true
	return tea.Batch(command, m.tick())
}

func (m Model) tick() tea.Cmd {
	return tea.Tick(m.tickEvery, func(time.Time) tea.Msg { return spinnerTick{} })
}

func (m Model) loadLocal(requestID uint64) tea.Cmd {
	return func() tea.Msg {
		dashboard, err := m.app.Load(m.ctx)
		return loadResult{requestID: requestID, dashboard: dashboard, err: err}
	}
}

func (m Model) refresh() tea.Cmd {
	return func() tea.Msg {
		return refreshResult{err: m.app.Refresh(m.ctx)}
	}
}

// indexOf is the position of value in values, or -1 so that the next one
// after an unknown value is the first.
func indexOf[T comparable](values []T, value T) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return -1
}

// Run starts the terminal application with explicit streams.
func Run(ctx context.Context, app Application, input io.Reader, output io.Writer, config Config) error {
	if input == nil || output == nil {
		return errors.New("run TUI: input and output are required")
	}
	if config.Themes == nil {
		config.Themes = func(mode theme.Mode) theme.Theme {
			return theme.New(output, theme.Options{NoColor: config.NoColor, ASCII: config.ASCII, Mode: mode})
		}
	}
	if config.Theme == nil {
		tokens := config.Themes(config.Mode)
		config.Theme = &tokens
	}
	if config.Clipboard == nil && !config.NoColor {
		// NO_COLOR asks for no escape sequences at all, so it also turns
		// this one off; the code stays on screen to copy by hand.
		config.Clipboard = terminalClipboard(output)
	}
	program := tea.NewProgram(
		NewModel(ctx, app, config),
		tea.WithContext(ctx),
		tea.WithInput(input),
		tea.WithOutput(output),
		tea.WithAltScreen(),
	)
	final, err := program.Run()
	if err != nil {
		return fmt.Errorf("run TUI: %w", err)
	}
	writeSharedLink(output, final)
	return nil
}

// modeSaved reports whether a theme switch was remembered.
type modeSaved struct{ err error }

// switchTheme moves between the dark and light look and asks for the choice
// to be remembered. An output that takes no color has no look to switch.
func (m Model) switchTheme() (tea.Model, tea.Cmd) {
	if m.themes == nil || !m.theme.Color {
		return m, nil
	}
	mode := m.theme.Mode.Other()
	m.theme = m.themes(mode)
	if m.saveMode == nil {
		return m, nil
	}
	save := m.saveMode
	return m, func() tea.Msg { return modeSaved{err: save(mode)} }
}

// writeSharedLink prints the last share link once the full-screen view is
// gone, where it can be selected and clicked. A wrapped link inside a panel
// cannot.
func writeSharedLink(output io.Writer, final tea.Model) {
	if model, ok := final.(Model); ok && model.sharedLink != "" {
		fmt.Fprintf(output, "Share link:\n%s\n", model.sharedLink)
	}
}
