package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/ansonnchan/tokenholic/internal/application"
	"github.com/ansonnchan/tokenholic/internal/multiplayer"
	tea "github.com/charmbracelet/bubbletea"
)

const (
	// MinimumWidth is the narrowest supported terminal width.
	MinimumWidth = 48
	wideWidth    = 88
)

// Section identifies one top-level local application area.
type Section int

const (
	// OverviewSection summarizes local usage and freshness.
	OverviewSection Section = iota
	// SourcesSection shows local adapter and source health.
	SourcesSection
	// ModelsSection shows local model usage and attribution.
	ModelsSection
	// SettingsSection explains controls, settings, and privacy.
	SettingsSection
	// GroupsSection shows authenticated private-group leaderboards.
	GroupsSection
)

var sections = []Section{OverviewSection, SourcesSection, ModelsSection, SettingsSection, GroupsSection}

// ContentState identifies the semantic state of the active section.
type ContentState string

const (
	// StateLoading means the application is reading the active snapshot.
	StateLoading ContentState = "loading"
	// StateEmpty means the snapshot contains no displayable records.
	StateEmpty ContentState = "empty"
	// StateReady means the snapshot is complete and healthy.
	StateReady ContentState = "ready"
	// StateWarning means content is available with an actionable warning.
	StateWarning ContentState = "warning"
	// StateError means the application could not load the active snapshot.
	StateError ContentState = "error"
)

// Page contains presentation-ready, privacy-safe content for one section.
type Page struct {
	Lines        []string
	Warning      string
	EmptyMessage string
}

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
	NoColor     bool
	Multiplayer Multiplayer
}

// Model is the Bubble Tea model for the local application shell.
type Model struct {
	ctx         context.Context
	app         Application
	multiplayer Multiplayer
	styles      styleSet
	section     Section
	state       ContentState
	page        Page
	errMessage  string
	width       int
	height      int
	help        bool
	refreshing  bool
	requestID   uint64
	groups      multiplayer.Snapshot
	groupID     string
	period      multiplayer.Period
	metric      multiplayer.Metric
	groupInput  groupInput
}

type groupInput struct {
	action string
	value  string
}

type loadResult struct {
	section   Section
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

var errMultiplayerNotConfigured = errors.New("multiplayer is not configured")

// NewModel constructs a shell whose data access is limited to app.
func NewModel(ctx context.Context, app Application, config Config) Model {
	if ctx == nil {
		ctx = context.Background()
	}
	state := StateLoading
	if app == nil {
		state = StateEmpty
	}
	return Model{
		ctx:         ctx,
		app:         app,
		multiplayer: config.Multiplayer,
		styles:      newStyles(config.NoColor),
		section:     OverviewSection,
		state:       state,
		requestID:   1,
		period:      multiplayer.PeriodToday,
		metric:      multiplayer.MetricTotalTokens,
	}
}

// Init loads the initial application snapshot.
func (m Model) Init() tea.Cmd {
	return m.load(m.section, m.requestID)
}

// Update applies terminal, navigation, help, refresh, and load messages.
func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height
		return m, nil
	case tea.KeyMsg:
		return m.updateKey(message)
	case loadResult:
		// A slow request must not replace a newer selection, including when the
		// user leaves and returns to the same tab before the first load finishes.
		if message.section != m.section || message.requestID != m.requestID {
			return m, nil
		}
		m.refreshing = false
		m.errMessage = ""
		m.page = buildPage(message.section, message.dashboard)
		switch {
		case message.err != nil:
			m.state = StateError
			m.errMessage = "Could not load local data"
		case m.page.Warning != "":
			m.state = StateWarning
		case m.page.EmptyMessage != "" || len(m.page.Lines) == 0:
			m.state = StateEmpty
		default:
			m.state = StateReady
		}
		return m, nil
	case groupLoadResult:
		if m.section != GroupsSection || message.requestID != m.requestID {
			return m, nil
		}
		m.refreshing = false
		m.errMessage = ""
		switch {
		case errors.Is(message.err, errMultiplayerNotConfigured):
			m.page = Page{Warning: "Multiplayer needs an API configuration", Lines: []string{
				"Multiplayer is not configured.",
				"Set TOKENHOLIC_API_URL and run `tokenholic claim`, then reopen tokenholic.",
			}}
			m.state = StateWarning
		case errors.Is(message.err, multiplayer.ErrSignedOut):
			m.page = Page{Warning: "Sign in is required for private groups", Lines: []string{
				"No signed-in multiplayer account.",
				"Run `tokenholic claim`, then press r to retry.",
			}}
			m.state = StateWarning
		case message.err != nil:
			m.state = StateError
			m.errMessage = "Could not load private groups"
		default:
			m.groups = message.snapshot
			m.groupID = message.snapshot.Selected.ID
			m.page = groupPage(message.snapshot)
			if m.page.Warning != "" {
				m.state = StateWarning
			} else {
				m.state = StateReady
			}
		}
		return m, nil
	case groupMutationResult:
		if m.section != GroupsSection || message.requestID != m.requestID {
			return m, nil
		}
		m.refreshing = false
		if message.err != nil {
			m.state = StateError
			m.errMessage = "Could not " + message.action + " private group"
			return m, nil
		}
		m.groupID = message.groupID
		m.requestID++
		return m, m.loadGroups(m.requestID)
	case refreshResult:
		if message.err != nil {
			m.refreshing = false
			m.state = StateError
			m.errMessage = "Could not refresh local data"
			return m, nil
		}
		m.requestID++
		return m, m.load(m.section, m.requestID)
	default:
		return m, nil
	}
}

func (m Model) updateKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "ctrl+c" {
		return m, tea.Quit
	}
	if m.groupInput.action != "" {
		return m.updateGroupInput(key)
	}
	switch key.String() {
	case "q":
		return m, tea.Quit
	case "?":
		m.help = !m.help
		return m, nil
	case "1":
		return m.activate(OverviewSection)
	case "2":
		return m.activate(SourcesSection)
	case "3":
		return m.activate(ModelsSection)
	case "4":
		return m.activate(SettingsSection)
	case "5":
		return m.activate(GroupsSection)
	case "tab", "right", "l":
		return m.activate(sections[(int(m.section)+1)%len(sections)])
	case "shift+tab", "left", "h":
		return m.activate(sections[(int(m.section)+len(sections)-1)%len(sections)])
	case "j", "down":
		if m.section == GroupsSection {
			return m.selectGroup(1)
		}
		return m.activate(sections[(int(m.section)+1)%len(sections)])
	case "k", "up":
		if m.section == GroupsSection {
			return m.selectGroup(-1)
		}
		return m.activate(sections[(int(m.section)+len(sections)-1)%len(sections)])
	case "p":
		if m.section == GroupsSection {
			m.period = nextPeriod(m.period)
			return m.reloadGroups()
		}
		return m, nil
	case "m":
		if m.section == GroupsSection {
			m.metric = nextMetric(m.metric)
			return m.reloadGroups()
		}
		return m, nil
	case "c":
		if m.section == GroupsSection && m.multiplayer != nil {
			m.groupInput = groupInput{action: "create"}
		}
		return m, nil
	case "i":
		if m.section == GroupsSection && m.multiplayer != nil {
			m.groupInput = groupInput{action: "join"}
		}
		return m, nil
	case "r":
		if m.section == GroupsSection {
			return m.reloadGroups()
		}
		if m.app == nil || m.refreshing {
			return m, nil
		}
		m.refreshing = true
		return m, m.refresh()
	default:
		return m, nil
	}
}

func (m Model) activate(section Section) (tea.Model, tea.Cmd) {
	if section == m.section {
		return m, nil
	}
	m.section = section
	m.help = false
	m.state = StateLoading
	m.page = Page{}
	m.errMessage = ""
	m.requestID++
	return m, m.load(section, m.requestID)
}

func (m Model) load(section Section, requestID uint64) tea.Cmd {
	if section == GroupsSection {
		return m.loadGroups(requestID)
	}
	if m.app == nil {
		return nil
	}
	return func() tea.Msg {
		dashboard, err := m.app.Load(m.ctx)
		return loadResult{section: section, requestID: requestID, dashboard: dashboard, err: err}
	}
}

func (m Model) loadGroups(requestID uint64) tea.Cmd {
	if m.multiplayer == nil {
		return func() tea.Msg { return groupLoadResult{requestID: requestID, err: errMultiplayerNotConfigured} }
	}
	return func() tea.Msg {
		snapshot, err := m.multiplayer.Load(m.ctx, m.groupID, m.period, m.metric)
		return groupLoadResult{requestID: requestID, snapshot: snapshot, err: err}
	}
}

func (m Model) reloadGroups() (tea.Model, tea.Cmd) {
	m.help = false
	m.state = StateLoading
	m.errMessage = ""
	m.refreshing = true
	m.requestID++
	return m, m.loadGroups(m.requestID)
}

func (m Model) selectGroup(delta int) (tea.Model, tea.Cmd) {
	if len(m.groups.Groups) < 2 {
		return m, nil
	}
	index := 0
	for candidate := range m.groups.Groups {
		if m.groups.Groups[candidate].ID == m.groupID {
			index = candidate
			break
		}
	}
	index = (index + delta + len(m.groups.Groups)) % len(m.groups.Groups)
	m.groupID = m.groups.Groups[index].ID
	return m.reloadGroups()
}

func (m Model) updateGroupInput(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.Type {
	case tea.KeyEsc:
		m.groupInput = groupInput{}
		return m, nil
	case tea.KeyEnter:
		value := strings.TrimSpace(m.groupInput.value)
		if value == "" {
			return m, nil
		}
		action := m.groupInput.action
		m.groupInput = groupInput{}
		m.state = StateLoading
		m.refreshing = true
		m.requestID++
		requestID := m.requestID
		return m, func() tea.Msg {
			var group multiplayer.Group
			var err error
			if action == "create" {
				group, err = m.multiplayer.Create(m.ctx, value)
			} else {
				group, err = m.multiplayer.Join(m.ctx, value)
			}
			return groupMutationResult{requestID: requestID, action: action, groupID: group.ID, err: err}
		}
	case tea.KeyBackspace, tea.KeyDelete:
		runes := []rune(m.groupInput.value)
		if len(runes) > 0 {
			m.groupInput.value = string(runes[:len(runes)-1])
		}
		return m, nil
	case tea.KeyRunes:
		limit := 80
		if m.groupInput.action == "join" {
			limit = 128
		}
		runes := []rune(m.groupInput.value)
		for _, character := range key.Runes {
			if len(runes) >= limit || unicode.IsControl(character) {
				continue
			}
			runes = append(runes, character)
		}
		m.groupInput.value = string(runes)
		return m, nil
	default:
		return m, nil
	}
}

func nextPeriod(period multiplayer.Period) multiplayer.Period {
	periods := []multiplayer.Period{
		multiplayer.PeriodToday, multiplayer.PeriodWeek,
		multiplayer.PeriodLast30Days, multiplayer.PeriodAllTime,
	}
	for index, candidate := range periods {
		if candidate == period {
			return periods[(index+1)%len(periods)]
		}
	}
	return multiplayer.PeriodToday
}

func nextMetric(metric multiplayer.Metric) multiplayer.Metric {
	metrics := []multiplayer.Metric{
		multiplayer.MetricTotalTokens, multiplayer.MetricAPIEstimate,
	}
	for index, candidate := range metrics {
		if candidate == metric {
			return metrics[(index+1)%len(metrics)]
		}
	}
	return multiplayer.MetricTotalTokens
}

func (m Model) refresh() tea.Cmd {
	return func() tea.Msg {
		return refreshResult{err: m.app.Refresh(m.ctx)}
	}
}

// View renders the current shell without reading application state.
func (m Model) View() string {
	return m.render()
}

// Run starts the terminal application with explicit streams.
func Run(ctx context.Context, app Application, input io.Reader, output io.Writer, config Config) error {
	if input == nil || output == nil {
		return errors.New("run TUI: input and output are required")
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

func sectionName(section Section) string {
	switch section {
	case OverviewSection:
		return "Overview"
	case SourcesSection:
		return "Sources"
	case ModelsSection:
		return "Models"
	case SettingsSection:
		return "Settings"
	case GroupsSection:
		return "Groups"
	default:
		return "Unknown"
	}
}

func cleanLine(line string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, line)
}
