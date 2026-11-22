package tui

import (
	"context"
	"errors"
	"strings"

	"github.com/ansonnchan/tokenholic/internal/account"
	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/numfmt"
	"github.com/ansonnchan/tokenholic/internal/share"
	"github.com/ansonnchan/tokenholic/internal/tui/widget"
	tea "github.com/charmbracelet/bubbletea"
)

// Account signs this device in and uploads its usage. The TUI shows the
// steps; the login protocol, the credential, and the upload stay behind
// this interface. Nothing is uploaded until Sync, and Sync is called only
// after the user has claimed.
type Account interface {
	// Login is the GitHub login of this device's session, or "" when it
	// has none.
	Login(context.Context) (string, error)
	// Begin starts device authorization and returns the code to show.
	Begin(context.Context) (account.DeviceStart, error)
	// Complete waits for the user to authorize, stores the session, and
	// returns their login.
	Complete(context.Context, account.DeviceStart) (string, error)
	// Sync uploads pending usage buckets and returns how many were accepted.
	Sync(context.Context) (int64, error)
}

// Autosync turns on hourly background sync. A claim offers it once the first
// upload is done; the operating system's scheduler stays behind this
// interface, and nothing is installed unless the user says yes.
type Autosync interface {
	// Available reports whether to offer it: this system can schedule a
	// sync, and none is scheduled already.
	Available(context.Context) bool
	// Enable installs the schedule and starts it.
	Enable(context.Context) error
}

var (
	// ErrUpgradeRequired means the server no longer accepts this version's
	// uploads. Local usage is kept and uploads after an upgrade.
	ErrUpgradeRequired = errors.New("this tokenholic version is too old to sync")
	// ErrSyncBusy means another process on this machine is syncing.
	ErrSyncBusy = errors.New("another synchronization is running")
	// ErrNoCredentialStore means the operating system's credential store
	// cannot hold a sign-in, so starting one would only strand the user at
	// GitHub.
	ErrNoCredentialStore = errors.New("the credential store is not available")
)

// claimStage is where the in-TUI claim has got to.
type claimStage int

const (
	claimClosed claimStage = iota
	// claimConfirm states what claiming uploads and waits for a yes.
	claimConfirm
	claimStarting
	// claimWaiting shows the code while the user authorizes at GitHub.
	claimWaiting
	claimSyncing
	claimFailed
	// claimSyncFailed is signed in, with the first upload still to do.
	claimSyncFailed
	// claimAutosync is claimed and uploaded, asking whether to keep it
	// current with an hourly sync.
	claimAutosync
	claimAutosyncEnabling
	// claimAutosyncFailed says the schedule could not be installed. The
	// claim itself is done.
	claimAutosyncFailed
)

type claimState struct {
	stage     claimStage
	challenge account.DeviceStart
	failure   string
	note      string
	// uploaded is what the first upload sent, kept to say so on the way out.
	uploaded int64
	// id discards the result of a step the user has cancelled.
	id     uint64
	cancel context.CancelFunc
}

type accountResult struct {
	login string
	err   error
}

type claimStarted struct {
	id        uint64
	challenge account.DeviceStart
	err       error
}

type claimCompleted struct {
	id    uint64
	login string
	err   error
}

type claimSynced struct {
	id       uint64
	uploaded int64
	err      error
	// offer is whether hourly sync can be offered now.
	offer bool
}

type claimScheduled struct {
	id  uint64
	err error
}

// shareState is the draft post and its link while the share overlay is open.
type shareState struct {
	open bool
	text string
	link string
	note string
}

func (m Model) loadAccount() tea.Cmd {
	return func() tea.Msg {
		login, err := m.account.Login(m.ctx)
		return accountResult{login: login, err: err}
	}
}

// canClaim reports whether a claim can be offered: an API is configured and
// this device is known to have no session.
func (m Model) canClaim() bool {
	return m.account != nil && m.accountKnown && m.login == ""
}

// openClaim shows what claiming does. It starts nothing.
func (m Model) openClaim() (tea.Model, tea.Cmd) {
	m.help, m.share.open = false, false
	m.claim = claimState{stage: claimConfirm, id: m.claim.id + 1}
	return m, nil
}

func (m Model) closeClaim() Model {
	if m.claim.cancel != nil {
		m.claim.cancel()
	}
	// A later id makes any step still running report to nobody.
	m.claim = claimState{id: m.claim.id + 1}
	return m
}

// updateClaim handles keys while the claim overlay is open. Like a prompt,
// it takes every key: nothing else navigates or quits except ctrl+c.
func (m Model) updateClaim(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case m.claim.stage == claimAutosyncEnabling:
		// Installing takes a moment and cannot be stopped halfway.
		return m, nil
	case key.Type == tea.KeyEnter && m.claim.stage == claimAutosync:
		m.claim.stage = claimAutosyncEnabling
		id := m.claim.id
		return m, m.withTick(func() tea.Msg {
			return claimScheduled{id: id, err: m.autosync.Enable(m.ctx)}
		})
	case m.claim.stage == claimAutosync && key.Type == tea.KeyEsc,
		m.claim.stage == claimAutosyncFailed && (key.Type == tea.KeyEsc || key.Type == tea.KeyEnter):
		// The claim is done either way: saying no only skips the schedule.
		return m.finishClaim("")
	case key.Type == tea.KeyEsc:
		return m.closeClaim(), nil
	case key.Type == tea.KeyEnter && (m.claim.stage == claimConfirm || m.claim.stage == claimFailed):
		return m.beginClaim()
	case key.Type == tea.KeyEnter && m.claim.stage == claimSyncFailed:
		return m.syncClaim()
	case m.claim.stage == claimWaiting && key.String() == "o":
		m.claim.note = m.open(m.claim.challenge.VerificationURI, "The page")
		return m, nil
	case m.claim.stage == claimWaiting && key.String() == "y":
		m.claim.note = m.copyText(m.claim.challenge.UserCode, "Code")
		return m, nil
	default:
		return m, nil
	}
}

func (m Model) beginClaim() (tea.Model, tea.Cmd) {
	m.claim = claimState{stage: claimStarting, id: m.claim.id + 1}
	id := m.claim.id
	return m, m.withTick(func() tea.Msg {
		challenge, err := m.account.Begin(m.ctx)
		return claimStarted{id: id, challenge: challenge, err: err}
	})
}

func (m Model) syncClaim() (tea.Model, tea.Cmd) {
	m.claim.stage, m.claim.failure = claimSyncing, ""
	id := m.claim.id
	return m, m.withTick(func() tea.Msg {
		uploaded, err := m.account.Sync(m.ctx)
		// Asked here, off the update loop: it queries the scheduler.
		offer := err == nil && m.autosync != nil && m.autosync.Available(m.ctx)
		return claimSynced{id: id, uploaded: uploaded, err: err, offer: offer}
	})
}

// finishClaim closes a completed claim and goes to the boards the user
// claimed a spot on, saying what was done.
func (m Model) finishClaim(also string) (tea.Model, tea.Cmd) {
	separator := " " + m.theme.Glyphs.Separator + " "
	done := "Signed in as " + memberLogin(m.login) + separator + uploadedText(m.claim.uploaded)
	if also != "" {
		done += separator + also
	}
	m = m.closeClaim()
	m.welcome = false
	m.section, m.scroll = BoardsSection, 0
	model, flash := m.flashNow(done)
	m = model.(Model)
	var reload tea.Cmd
	if m.app != nil && !m.loading {
		// The upload emptied the outbox; show that.
		m.loading = true
		m.localRequest++
		reload = m.loadLocal(m.localRequest)
	}
	board, load := m.reloadBoard()
	return board, tea.Batch(flash, reload, load)
}

// applyClaim advances the claim on the result of its running step.
func (m Model) applyClaim(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case claimStarted:
		if message.id != m.claim.id {
			return m, nil
		}
		if errors.Is(message.err, ErrNoCredentialStore) {
			// The terminal command has room to say which store and what to do.
			m.claim.stage, m.claim.failure = claimFailed, "This system's credential store did not answer, so a sign-in cannot be saved. Run `tokenholic claim` in a terminal to see what to check"
			return m, nil
		}
		if message.err != nil || message.challenge.UserCode == "" {
			m.claim.stage, m.claim.failure = claimFailed, "Could not start sign-in. Check your connection; the API can take a minute to wake"
			return m, nil
		}
		ctx, cancel := context.WithCancel(m.ctx)
		m.claim.stage, m.claim.challenge, m.claim.cancel = claimWaiting, message.challenge, cancel
		id, challenge := m.claim.id, message.challenge
		return m, func() tea.Msg {
			login, err := m.account.Complete(ctx, challenge)
			return claimCompleted{id: id, login: login, err: err}
		}
	case claimCompleted:
		if message.id != m.claim.id {
			return m, nil
		}
		if m.claim.cancel != nil {
			m.claim.cancel()
			m.claim.cancel = nil
		}
		if message.err != nil {
			m.claim.stage, m.claim.failure = claimFailed, "Sign-in did not finish: the code expired or was not approved"
			return m, nil
		}
		m.login, m.accountKnown = message.login, true
		return m.syncClaim()
	case claimSynced:
		if message.id != m.claim.id {
			return m, nil
		}
		switch {
		case errors.Is(message.err, ErrUpgradeRequired):
			m.claim.stage = claimSyncFailed
			m.claim.failure = "Signed in, but this version is too old to upload. Upgrade with `npx @tokenholic/cli@latest`; your usage is kept and uploads afterwards"
			return m, nil
		case errors.Is(message.err, ErrSyncBusy):
			m.claim.stage, m.claim.failure = claimSyncFailed, "Signed in, but another sync is running on this machine"
			return m, nil
		case message.err != nil:
			m.claim.stage, m.claim.failure = claimSyncFailed, "Signed in, but the first upload did not finish. Your usage is kept"
			return m, nil
		}
		m.claim.uploaded = message.uploaded
		if message.offer {
			m.claim.stage = claimAutosync
			return m, nil
		}
		return m.finishClaim("")
	case claimScheduled:
		if message.id != m.claim.id {
			return m, nil
		}
		if message.err != nil {
			// The terminal command has room to say what the scheduler needs.
			m.claim.stage, m.claim.failure = claimAutosyncFailed, "Hourly sync could not be turned on here. Your claim and upload are done; run `tokenholic autosync enable` in a terminal to see what is missing"
			return m, nil
		}
		return m.finishClaim("hourly sync on")
	default:
		return m, nil
	}
}

func uploadedText(buckets int64) string {
	if buckets == 1 {
		return "1 bucket uploaded"
	}
	return numfmt.Exact(uint64(max(buckets, 0))) + " buckets uploaded"
}

func memberLogin(login string) string {
	return "@" + strings.TrimPrefix(cleanLine(login), "@")
}

// open asks the browser to show a link and says what happened.
func (m Model) open(link, what string) string {
	if m.openURL == nil {
		return "Open it yourself: no browser can be started from here"
	}
	if err := m.openURL(link); err != nil {
		return "Could not open a browser; open the address yourself"
	}
	return what + " opened in your browser"
}

// claimView is the claim overlay for the current stage.
func (m Model) claimView(width int) []string {
	t := m.theme
	g := t.Glyphs
	inner := widget.InnerWidth(width)
	separator := " " + g.Separator + " "
	var lines []string
	say := func(text string) { lines = append(lines, widget.Wrap(text, inner)...) }
	field := func(label, text string) {
		const labelWidth = 10
		for index, line := range widget.Wrap(text, max(inner-labelWidth, 8)) {
			head := ""
			if index == 0 {
				head = widget.Label(t, label)
			}
			lines = append(lines, widget.PadRight(head, labelWidth)+line)
		}
	}
	keys := func(action string, hints ...string) {
		lines = append(lines, "")
		if action != "" {
			lines = append(lines, action)
		}
		lines = append(lines, t.Muted.Render(widget.Truncate(t, strings.Join(hints, separator), inner)))
	}
	switch m.claim.stage {
	case claimConfirm:
		say("Claiming signs this device in with GitHub and uploads your usage counts, so you can join boards.")
		lines = append(lines, "")
		field("uploaded", "when, which coding agent, which model, and token counts per category")
		field("never", "prompts, responses, code, paths, project or repository names, credentials")
		field("check", "`tokenholic show-data` prints the exact payload")
		keys(widget.Action(t, "enter", "continue with GitHub"), "esc cancel")
	case claimStarting:
		lines = append(lines, t.Muted.Render(m.spin()+" Asking GitHub for a code"))
		keys("", "esc cancel")
	case claimWaiting:
		field("1  open", cleanLine(m.claim.challenge.VerificationURI))
		lines = append(lines, widget.PadRight(widget.Label(t, "2  enter"), 10)+t.Accent.Render(cleanLine(m.claim.challenge.UserCode)))
		lines = append(lines, "", t.Muted.Render(m.spin()+" Waiting for you to approve at GitHub"))
		if m.claim.note != "" {
			lines = append(lines, t.Muted.Render(widget.Truncate(t, m.claim.note, inner)))
		}
		keys(widget.Action(t, "o", "open the page"), "y copy code", "esc cancel")
	case claimSyncing:
		say("Signed in as " + memberLogin(m.login) + ".")
		lines = append(lines, t.Muted.Render(m.spin()+" Uploading your usage"))
	case claimFailed:
		lines = append(lines, widget.Notice(t, widget.Failure, m.claim.failure+".", inner)...)
		keys(widget.Action(t, "enter", "try again"), "esc close")
	case claimSyncFailed:
		lines = append(lines, widget.Notice(t, widget.Warning, m.claim.failure+".", inner)...)
		keys(widget.Action(t, "enter", "retry the upload"), "esc continue without it")
	case claimAutosync, claimAutosyncEnabling:
		say("Signed in as " + memberLogin(m.login) + separator + uploadedText(m.claim.uploaded) + ".")
		lines = append(lines, "")
		say("Keep your boards current? Hourly sync uploads new usage counts in the background, at login and every hour.")
		lines = append(lines, "")
		field("installs", "a scheduled job for your user, and a copy of tokenholic to run it")
		field("uploads", "the same counts as just now, and nothing else")
		field("undo", "`tokenholic autosync disable` removes it")
		if m.claim.stage == claimAutosyncEnabling {
			lines = append(lines, "", t.Muted.Render(m.spin()+" Turning on hourly sync"))
		} else {
			keys(widget.Action(t, "enter", "turn on hourly sync"), "esc not now")
		}
	case claimAutosyncFailed:
		lines = append(lines, widget.Notice(t, widget.Warning, m.claim.failure+".", inner)...)
		lines = append(lines, "", widget.Action(t, "enter", "continue"))
	}
	title := "claim your spot"
	if m.claim.stage >= claimAutosync {
		title = "keep it current"
	}
	return widget.Panel(t, title, width, lines)
}

// welcomeView is the first launch: the scan, then the headline it found.
// It has no tabs; the dashboard is one key away.
func (m Model) welcomeView(width int) []string {
	t := m.theme
	g := t.Glyphs
	separator := " " + g.Separator + " "
	var lines []string
	if mark, ok := widget.BlockWordmark(t, width); ok && width >= stackWidth {
		lines = append(lines, mark...)
	} else {
		lines = append(lines, widget.Wordmark(t))
	}
	lines = append(lines, "")

	health := m.dashboard.Diagnostics.AdapterHealth
	switch {
	case m.refreshing || !m.loaded:
		lines = append(lines, widget.Label(t, "scanning")+"  "+m.spin()+" reading local coding-agent usage")
	case m.localError != "":
		lines = append(lines, widget.Notice(t, widget.Failure, m.localError+". Press r to retry, or run `tokenholic doctor`.", width)...)
	default:
		// One entry per source that exists here; a source that was simply
		// not found is left to the SOURCES view.
		var found []string
		for _, record := range health {
			name := sourceName(string(record.Health.Source)) + " " + surfaceName(string(record.Health.Surface))
			switch {
			case record.Health.Status == normalize.HealthDetected:
				found = append(found, name+" "+g.Check)
			case needsAttention(record.Health.Status):
				found = append(found, name+": "+healthText(record.Health.Status))
			}
		}
		if len(found) == 0 {
			found = []string{"no coding agent found"}
		}
		// Entries wrap whole, under the label on the first line only.
		const indent = 9
		label, row := widget.Label(t, "scanned"), ""
		flush := func() {
			lines = append(lines, widget.PadRight(label, indent)+widget.Truncate(t, row, width-indent))
			label, row = "", ""
		}
		for _, entry := range found {
			if row != "" && widget.Width(row)+widget.Width(separator)+widget.Width(entry) > width-indent {
				flush()
			}
			if row != "" {
				row += separator
			}
			row += entry
		}
		flush()
	}
	lines = append(lines, "")

	summary := m.dashboard.Last30Days
	switch {
	case !m.loaded || m.refreshing:
	case noSupportedSources(m.dashboard.AllTime, health):
		lines = append(lines, noSourcesLines(t, width)...)
		lines = append(lines, "")
	case isEmpty(summary):
		lines = append(lines, widget.Wrap("No coding-agent usage in the last 30 days. Open the dashboard for other periods.", width)...)
		lines = append(lines, "")
	default:
		headline := "YOU BURNED " + tokensText(g, summary.Tokens.Total()) + " TOKENS"
		if summary.APIEstimate.Value != nil {
			headline += separator + estimateText(g, summary.APIEstimate) + " EST"
		}
		headline += separator + "LAST 30 DAYS"
		for _, line := range widget.Wrap(headline, width) {
			lines = append(lines, t.Strong.Render(line))
		}
		lines = append(lines, "")
	}

	if m.loaded && !m.refreshing {
		if m.canClaim() {
			lines = append(lines, widget.Action(t, "c", "claim your spot"))
			lines = append(lines, t.Muted.Render(widget.Truncate(t, "Nothing leaves this machine until you claim.", width)))
		} else {
			lines = append(lines, widget.Action(t, "enter", "open the dashboard"))
		}
	}
	return lines
}

// claimChip is HOME's standing offer to a signed-out user.
func (m Model) claimChip(width int) []string {
	t := m.theme
	chip := widget.Action(t, "c", "claim your spot")
	note := "Rank with friends. Nothing uploads until you claim."
	if widget.Width(chip)+2+widget.Width(note) <= width {
		return []string{chip + "  " + t.Muted.Render(note)}
	}
	lines := []string{chip}
	for _, line := range widget.Wrap(note, width) {
		lines = append(lines, t.Muted.Render(line))
	}
	return lines
}

// openShare drafts a post from the period HOME is showing. The draft holds
// the user's own totals and names no group.
func (m Model) openShare() (tea.Model, tea.Cmd) {
	if !m.loaded {
		return m, nil
	}
	usage, ok := share.UsageFor(summaryFor(m.dashboard, m.period))
	if !ok {
		return m, nil
	}
	text := share.UsageText(usage)
	m.help = false
	m.share = shareState{open: true, text: text, link: share.IntentLink(share.Post{Text: text})}
	// Remembered so the link can be printed where it can be selected, after
	// the full-screen view closes.
	m.sharedLink = m.share.link
	return m, nil
}

func (m Model) updateShare(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Type == tea.KeyEsc || key.String() == "s" || key.String() == "q":
		m.share.open = false
	case key.String() == "o":
		m.share.note = m.open(m.share.link, "The draft")
	case key.String() == "y":
		m.share.note = m.copyText(m.share.link, "Link")
	}
	return m, nil
}

// shareView shows the draft and its link. The user posts it themselves, in
// their browser; tokenholic sends nothing.
func (m Model) shareView(width int) []string {
	t := m.theme
	inner := widget.InnerWidth(width)
	separator := " " + t.Glyphs.Separator + " "
	lines := []string{widget.Rule(t, "draft", "", inner)}
	for _, paragraph := range strings.Split(m.share.text, "\n") {
		lines = append(lines, widget.Wrap(paragraph, inner)...)
	}
	lines = append(lines, "", widget.Rule(t, "link", "", inner))
	for _, line := range widget.Wrap(m.share.link, inner) {
		lines = append(lines, t.Muted.Render(line))
	}
	lines = append(lines, "")
	if m.share.note != "" {
		lines = append(lines, t.Muted.Render(widget.Truncate(t, m.share.note, inner)))
	}
	lines = append(lines, widget.Action(t, "o", "open in browser"))
	lines = append(lines, t.Muted.Render(widget.Truncate(t, "y copy link"+separator+"esc close", inner)))
	for _, line := range widget.Wrap("The link opens X with this draft for you to review and post. It is printed again when you quit.", inner) {
		lines = append(lines, t.Muted.Render(line))
	}
	return widget.Panel(t, "share", width, lines)
}
