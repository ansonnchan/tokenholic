package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

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

// Multiplayer supplies authenticated group snapshots and inline mutations.
type Multiplayer interface {
	Load(context.Context, string, multiplayer.Period, multiplayer.Metric) (multiplayer.Snapshot, error)
	Create(context.Context, string) (multiplayer.Group, error)
	Join(context.Context, string) (multiplayer.Group, error)
}

// Config controls environment-sensitive shell behavior.
type Config struct {
	// NoColor disables all styling; ASCII swaps glyphs for their fallbacks;
	// Background overrides light or dark detection. They are independent.
	NoColor     bool
	ASCII       bool
	Background  theme.Background
	Multiplayer Multiplayer
	// Theme replaces the detected theme. Run sets it from the output; tests
	// and previews set it to a fixed one.
	Theme *theme.Theme
}

// Model is the Bubble Tea model for the application shell. It holds the last
// stable snapshot of each area so a refresh never blanks the screen.
type Model struct {
	ctx         context.Context
	app         Application
	multiplayer Multiplayer
	theme       theme.Theme
	section     Section
	width       int
	height      int
	help        bool
	scroll      int
	spinner     int
	ticking     bool
	tickEvery   time.Duration

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
}

type groupInput struct {
	action string
	value  string
}

type loadResult struct {
	requestID uint64
	dashboard application.Dashboard
	err       error
}

type refreshResult struct {
	err error
}

type groupLoadResult struct {
	requestID uint64
	snapshot  multiplayer.Snapshot
	err       error
}

type groupMutationResult struct {
	requestID uint64
	action    string
	groupID   string
	err       error
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
		section:      HomeSection,
		tickEvery:    spinnerInterval,
		loading:      app != nil,
		localRequest: 1,
		period:       store.PeriodLast30Days,
		board: boardState{
			period: multiplayer.PeriodToday,
			metric: multiplayer.MetricTotalTokens,
		},
	}
}

// Init loads the first local snapshot.
func (m Model) Init() tea.Cmd {
	if m.app == nil {
		return nil
	}
	return tea.Batch(m.loadLocal(m.localRequest), m.tick())
}

// Update applies terminal, key, load, refresh, and spinner messages.
func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
		m.scroll = min(m.scroll, m.maxScroll())
		return m, nil
	case tea.KeyMsg:
		return m.updateKey(message)
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
	case groupLoadResult:
		return m.applyGroupLoad(message), nil
	case groupMutationResult:
		if message.requestID != m.board.request {
			return m, nil
		}
		if message.err != nil {
			m.board.loading = false
			m.board.failure = "Could not " + message.action + " private group"
			return m, nil
		}
		m.board.groupID = message.groupID
		m.board.request++
		return m, m.loadGroups(m.board.request)
	default:
		return m, nil
	}
}

func (m Model) applyGroupLoad(message groupLoadResult) Model {
	if message.requestID != m.board.request {
		return m
	}
	m.board.loading, m.board.loaded, m.board.failure = false, true, ""
	switch {
	case errors.Is(message.err, errMultiplayerNotConfigured):
		m.board.page = boardPage{warning: "Multiplayer needs an API configuration", lines: []string{
			"Multiplayer is not configured.",
			"Set TOKENHOLIC_API_URL and run `tokenholic claim`, then reopen tokenholic.",
		}}
	case errors.Is(message.err, multiplayer.ErrSignedOut):
		m.board.page = boardPage{warning: "Sign in is required for private groups", lines: []string{
			"No signed-in multiplayer account.",
			"Run `tokenholic claim`, then press r to retry.",
		}}
	case message.err != nil:
		// Keep the last stable board under the failure.
		m.board.failure = "Could not load private groups"
	default:
		m.board.snapshot = message.snapshot
		m.board.groupID = message.snapshot.Selected.ID
		m.board.page = groupPage(message.snapshot)
	}
	return m
}

func (m Model) updateKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "ctrl+c" {
		return m, tea.Quit
	}
	if m.board.input.action != "" {
		return m.updateGroupInput(key)
	}
	boards := m.section == BoardsSection
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
			return m.reloadGroups()
		}
		if m.app == nil || m.loading {
			return m, nil
		}
		m.loading, m.refreshing = true, true
		return m, m.withTick(m.refresh())
	case "p":
		if boards {
			m.board.period = nextPeriod(m.board.period)
			return m.reloadGroups()
		}
		if m.section == SettingsSection {
			return m, nil
		}
		m.period = periods[(indexOf(periods, m.period)+1)%len(periods)]
		m.modelCursor = 0
		m.scroll = min(m.scroll, m.maxScroll())
		return m, nil
	case "m":
		if boards {
			m.board.metric = nextMetric(m.board.metric)
			return m.reloadGroups()
		}
		return m, nil
	case "d":
		if m.section == HomeSection {
			m.detail = !m.detail
			m.scroll = min(m.scroll, m.maxScroll())
		}
		return m, nil
	case "j", "down":
		return m.move(1)
	case "k", "up":
		return m.move(-1)
	case "c":
		if boards && m.multiplayer != nil {
			m.board.input = groupInput{action: "create"}
		}
		return m, nil
	case "i":
		if boards && m.multiplayer != nil {
			m.board.input = groupInput{action: "join"}
		}
		return m, nil
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
		return m.selectGroup(delta)
	case m.section == ModelsSection:
		count := len(summaryFor(m.dashboard, m.period).Models)
		m.modelCursor = max(0, min(m.modelCursor+delta, count-1))
	default:
		m.scroll = max(0, min(m.scroll+delta, m.maxScroll()))
	}
	return m, nil
}

func (m Model) activate(section Section) (tea.Model, tea.Cmd) {
	if section == m.section {
		return m, nil
	}
	m.section = section
	m.help = false
	m.scroll = 0
	if section == BoardsSection {
		m.board.loading = true
		m.board.request++
		return m, m.withTick(m.loadGroups(m.board.request))
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

func (m Model) loadGroups(requestID uint64) tea.Cmd {
	if m.multiplayer == nil {
		return func() tea.Msg { return groupLoadResult{requestID: requestID, err: errMultiplayerNotConfigured} }
	}
	groupID, period, metric := m.board.groupID, m.board.period, m.board.metric
	return func() tea.Msg {
		snapshot, err := m.multiplayer.Load(m.ctx, groupID, period, metric)
		return groupLoadResult{requestID: requestID, snapshot: snapshot, err: err}
	}
}

func (m Model) reloadGroups() (tea.Model, tea.Cmd) {
	m.help = false
	m.board.loading = true
	m.board.request++
	return m, m.withTick(m.loadGroups(m.board.request))
}

func (m Model) selectGroup(delta int) (tea.Model, tea.Cmd) {
	groups := m.board.snapshot.Groups
	if len(groups) < 2 {
		return m, nil
	}
	index := 0
	for candidate := range groups {
		if groups[candidate].ID == m.board.groupID {
			index = candidate
			break
		}
	}
	m.board.groupID = groups[(index+delta+len(groups))%len(groups)].ID
	return m.reloadGroups()
}

func (m Model) updateGroupInput(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyEsc:
		m.board.input = groupInput{}
		return m, nil
	case tea.KeyEnter:
		value := strings.TrimSpace(m.board.input.value)
		if value == "" {
			return m, nil
		}
		action := m.board.input.action
		m.board.input = groupInput{}
		m.board.loading = true
		m.board.request++
		requestID := m.board.request
		return m, m.withTick(func() tea.Msg {
			var group multiplayer.Group
			var err error
			if action == "create" {
				group, err = m.multiplayer.Create(m.ctx, value)
			} else {
				group, err = m.multiplayer.Join(m.ctx, value)
			}
			return groupMutationResult{requestID: requestID, action: action, groupID: group.ID, err: err}
		})
	case tea.KeyBackspace, tea.KeyDelete:
		runes := []rune(m.board.input.value)
		if len(runes) > 0 {
			m.board.input.value = string(runes[:len(runes)-1])
		}
		return m, nil
	case tea.KeyRunes:
		limit := 80
		if m.board.input.action == "join" {
			limit = 128
		}
		runes := []rune(m.board.input.value)
		for _, character := range key.Runes {
			if len(runes) >= limit || unicode.IsControl(character) {
				continue
			}
			runes = append(runes, character)
		}
		m.board.input.value = string(runes)
		return m, nil
	default:
		return m, nil
	}
}

func nextPeriod(period multiplayer.Period) multiplayer.Period {
	order := []multiplayer.Period{
		multiplayer.PeriodToday, multiplayer.PeriodWeek,
		multiplayer.PeriodLast30Days, multiplayer.PeriodAllTime,
	}
	return order[(indexOf(order, period)+1)%len(order)]
}

func nextMetric(metric multiplayer.Metric) multiplayer.Metric {
	order := []multiplayer.Metric{multiplayer.MetricTotalTokens, multiplayer.MetricAPIEstimate}
	return order[(indexOf(order, metric)+1)%len(order)]
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
	if config.Theme == nil {
		tokens := theme.New(output, theme.Options{NoColor: config.NoColor, ASCII: config.ASCII, Background: config.Background})
		config.Theme = &tokens
	}
	program := tea.NewProgram(
		NewModel(ctx, app, config),
		tea.WithContext(ctx),
		tea.WithInput(input),
		tea.WithOutput(output),
		tea.WithAltScreen(),
	)
	if _, err := program.Run(); err != nil {
		return fmt.Errorf("run TUI: %w", err)
	}
	return nil
}
