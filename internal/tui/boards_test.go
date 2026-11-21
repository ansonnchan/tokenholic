package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/ansonnchan/tokenholic/internal/multiplayer"
	tea "github.com/charmbracelet/bubbletea"
)

// openBoard starts on the sample account's board with a clipboard that
// records what was copied.
func openBoard(t *testing.T, remote *fakeMultiplayer, width, height int) (Model, *[]string) {
	t.Helper()
	copied := &[]string{}
	config := Config{Multiplayer: remote, Clipboard: func(text string) { *copied = append(*copied, text) }}
	return press(start(t, &fakeApplication{dashboard: sampleDashboard()}, config, width, height), "2"), copied
}

func rowWith(model Model, text string) string {
	for _, line := range strings.Split(view(model), "\n") {
		if strings.Contains(line, text) {
			return line
		}
	}
	return ""
}

func TestBoardShowsWhatTheServerRanked(t *testing.T) {
	t.Parallel()
	remote := sampleRemote()
	model, _ := openBoard(t, remote, 120, 0)
	if remote.scope != "" || remote.period != multiplayer.PeriodWeek || remote.metric != multiplayer.MetricAPIEstimate {
		t.Fatalf("first load asked for %q %s %s", remote.scope, remote.period, remote.metric)
	}
	got := view(model)
	for _, want := range []string{
		"NIGHT-SHIFT", "[THIS WEEK]", "[$ EST]", "▲▼ VS LAST WEEK",
		"1  @dana", "$1,180.00  ▲ 1", "$1,090.00  ▼ 1", "$840.50  –", "$712.25  NEW",
		"5  @you  ★ YOU", "≥$560.75  71% priced", "—  no estimate", "—  no usage",
		"1–8 OF 8 · RESETS MON 00:00 America/Vancouver",
		"RANK #5", "LAST WEEK", "$512.10", "SCOPE    1 of 3", "ROLE     owner",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("board = %q, want %q", got, want)
		}
	}
	// An unranked row keeps its place and value but never gains a rank, and
	// a missing value is never a zero.
	if row := rowWith(model, "@sam"); !strings.Contains(row, "—  @sam") {
		t.Errorf("unranked row = %q", row)
	}
	if strings.Contains(got, "$0.00") {
		t.Errorf("an unavailable estimate rendered as zero: %q", got)
	}
	// Every dollar figure sits under an EST label and none is called spending.
	if lower := strings.ToLower(got); strings.Contains(lower, "spent") || strings.Contains(lower, "spend") {
		t.Errorf("an estimate is described as spending: %q", got)
	}
}

func TestBoardExplainsTheRowUnderTheCursor(t *testing.T) {
	t.Parallel()
	model, _ := openBoard(t, sampleRemote(), 120, 0)
	// The cursor starts on the viewer's own row.
	if got := view(model); !strings.Contains(got, "▸  5  @you") || !strings.Contains(got, "Rank #5, was #7 last week") {
		t.Fatalf("first selection = %q", got)
	}
	coverage := view(press(model, "j"))
	for _, want := range []string{
		"▸  —  @sam", "≥$560.75 EST · last week $498.20",
		"Unranked: 71% of these tokens are priced, and an estimate is ranked from 95%.",
		"mostly gpt-5.2-codex (inferred)", "4 with an unknown model", "212 with an inferred model", "without an estimate.",
	} {
		if !strings.Contains(coverage, want) {
			t.Errorf("coverage detail = %q, want %q", coverage, want)
		}
	}
	unpriced := view(press(model, "j", "j"))
	for _, want := range []string{"Unranked: none of this usage has a price.", "A daily total is above the plausibility limit. Usage is self-reported."} {
		if !strings.Contains(unpriced, want) {
			t.Errorf("unpriced detail = %q, want %q", unpriced, want)
		}
	}
	if got := view(press(model, "j", "j", "j", "j", "j", "j")); !strings.Contains(got, "▸  —  @zed") || !strings.Contains(got, "No usage in this period.") {
		t.Errorf("cursor past the last row = %q", got)
	}
	top := press(model, "k", "k", "k", "k", "k", "k", "k", "k")
	if got := view(top); !strings.Contains(got, "▸  1  @dana") || !strings.Contains(got, "Rank #1, was #2 last week") {
		t.Errorf("cursor above the first row = %q", got)
	}
	// A reload must not pull a cursor the user moved back to their own row.
	if got := view(press(top, "r")); !strings.Contains(got, "▸  1  @dana") {
		t.Errorf("cursor after a reload = %q", got)
	}
}

func TestBoardSwitchesPeriodAndMetricThroughTheServer(t *testing.T) {
	t.Parallel()
	remote := sampleRemote()
	model, _ := openBoard(t, remote, 88, 0)
	tokens := press(model, "m")
	if remote.metric != multiplayer.MetricTotalTokens {
		t.Fatalf("metric asked = %s", remote.metric)
	}
	// By tokens, a lower bound is ranked and stays labelled as one.
	if got := view(tokens); !strings.Contains(got, "[TOKENS]") || !strings.Contains(got, "6  @sam") || !strings.Contains(got, "≥717M") || strings.Contains(got, "$1,180.00") || strings.Contains(got, " EST ·") {
		t.Errorf("token board = %q", got)
	}
	if press(tokens, "m"); remote.metric != multiplayer.MetricAPIEstimate {
		t.Errorf("metric after two presses = %s", remote.metric)
	}
	legends := map[multiplayer.Period]string{
		multiplayer.PeriodLast30Days: "VS PRIOR 30 DAYS", multiplayer.PeriodAllTime: "VS 24H AGO",
		multiplayer.PeriodToday: "VS YESTERDAY", multiplayer.PeriodWeek: "VS LAST WEEK",
	}
	for range legends {
		model = press(model, "p")
		if got := view(model); !strings.Contains(got, legends[remote.period]) {
			t.Errorf("period %s: legend missing from %q", remote.period, got)
		}
	}
	if remote.period != multiplayer.PeriodWeek {
		t.Errorf("period after a full cycle = %s", remote.period)
	}
	if got := view(press(model, "p", "p")); !strings.Contains(got, "ALL SYNCED HISTORY") {
		t.Errorf("all-time board = %q", got)
	}
}

func TestBoardCyclesGroupsAndTheGlobalBoard(t *testing.T) {
	t.Parallel()
	remote := sampleRemote()
	model, _ := openBoard(t, remote, 120, 0)
	model = press(model, "g")
	if got := view(model); remote.scope != "group-day" || !strings.Contains(got, "DAY-CREW") || !strings.Contains(got, "SCOPE    2 of 3") || !strings.Contains(got, "BOARD    public") {
		t.Fatalf("second scope %q = %q", remote.scope, got)
	}
	// A member cannot create an invite, and is told so.
	if got := view(press(model, "y")); len(remote.invited) != 0 || !strings.Contains(got, "Only the group's owner can create an invite") {
		t.Errorf("member invite = %q after %v", got, remote.invited)
	}
	model = press(model, "g")
	global := view(model)
	for _, want := range []string{
		"GLOBAL", "SCOPE    3 of 3", "LISTED   no", "o join", "You are not on the global board.",
		"Ranks are hidden until 3 members can be ranked.", "—  @dana",
	} {
		if remote.scope != multiplayer.GlobalScope || !strings.Contains(global, want) {
			t.Errorf("global scope %q = %q, want %q", remote.scope, global, want)
		}
	}
	// Hidden ranks show no rank anywhere, and y does nothing without a group.
	if strings.Contains(global, "RANK #") || len(remote.invited) != 0 {
		t.Errorf("global board shows a rank: %q", global)
	}
	if press(model, "y"); len(remote.invited) != 0 {
		t.Errorf("invite created from the global board")
	}
	if press(model, "g"); remote.scope != "group-night" {
		t.Errorf("scope after a full cycle = %q", remote.scope)
	}
	if press(model, "G"); remote.scope != "group-day" {
		t.Errorf("scope after going back = %q", remote.scope)
	}
}

func TestBoardWindowsALongBoardAndPinsTheViewer(t *testing.T) {
	t.Parallel()
	remote := sampleRemote()
	remote.boards["group-night"] = longBoard(100, 31, 250)
	model, _ := openBoard(t, remote, 88, 30)
	frame := model.frame()
	if len(frame) > 30 {
		t.Fatalf("frame is %d rows in a terminal of 30", len(frame))
	}
	got := strings.Join(frame, "\n")
	if row := rowWith(model, "★ YOU"); !strings.Contains(row, "▸") || !strings.Contains(row, "31  @you") || !strings.Contains(got, "OF 250") {
		t.Fatalf("long board = %q", got)
	}
	// Moving far from the viewer's row keeps it on screen, after the list.
	moved := press(model, strings.Split(strings.Repeat("k ", 25), " ")[:25]...)
	lines := moved.frame()
	if len(lines) > 30 {
		t.Fatalf("frame is %d rows in a terminal of 30", len(lines))
	}
	cursor, viewer := -1, -1
	for index, line := range lines {
		if strings.Contains(line, "▸") {
			cursor = index
		}
		if strings.Contains(line, "★ YOU") && strings.Contains(line, "▌") {
			viewer = index
		}
	}
	if cursor < 0 || viewer < 0 || viewer <= cursor || !strings.Contains(lines[cursor], "@member-006") {
		t.Fatalf("cursor line %d, pinned viewer line %d in %q", cursor, viewer, lines)
	}
	// The viewer's row appears once, never duplicated inside the window.
	if count := strings.Count(strings.Join(lines, "\n"), "★ YOU"); count != 1 {
		t.Errorf("viewer row appears %d times", count)
	}
}

func TestOwnerCreatesAnInviteOnceAndCopiesIt(t *testing.T) {
	t.Parallel()
	remote := sampleRemote()
	model, copied := openBoard(t, remote, 120, 0)
	if got := view(model); !strings.Contains(got, "y create and copy") {
		t.Fatalf("owner panel = %q", got)
	}
	model = press(model, "y")
	got := view(model)
	if len(remote.invited) != 1 || remote.invited[0] != "group-night" || len(*copied) != 1 || (*copied)[0] != "ovt_Q7M2KXsynthetic" {
		t.Fatalf("invited %v, copied %v", remote.invited, *copied)
	}
	// The whole code is on screen, because a terminal may ignore the copy.
	for _, want := range []string{"ovt_Q7M2KXsynthetic", "expires in 24h · 10 uses", "DONE  Invite copied", "y copy"} {
		if !strings.Contains(got, want) {
			t.Errorf("after creating = %q, want %q", got, want)
		}
	}
	// Copying again reuses the code instead of creating another.
	if press(model, "y"); len(remote.invited) != 1 || len(*copied) != 2 {
		t.Errorf("second copy: invited %v, copied %v", remote.invited, *copied)
	}
	// The success message fades; the code stays.
	faded, _ := model.Update(flashExpired{id: model.board.flashID})
	if got := view(faded.(Model)); strings.Contains(got, "DONE") || !strings.Contains(got, "ovt_Q7M2KXsynthetic") {
		t.Errorf("after the message fades = %q", got)
	}
	// An older timer must not clear a newer message.
	stale, _ := press(model, "y").Update(flashExpired{id: model.board.flashID})
	if got := view(stale.(Model)); !strings.Contains(got, "DONE  Invite copied") {
		t.Errorf("a stale timer cleared the message: %q", got)
	}
}

func TestInviteFailureAndMissingClipboardAreSaidPlainly(t *testing.T) {
	t.Parallel()
	remote := sampleRemote()
	remote.actionError = errors.New("synthetic outage at https://api.example.test/private")
	model, copied := openBoard(t, remote, 120, 0)
	if got := view(press(model, "y")); !strings.Contains(got, "ERROR  Could not create an invite. Press r to retry.") || strings.Contains(got, "api.example.test") || len(*copied) != 0 {
		t.Fatalf("failed invite = %q, copied %v", got, *copied)
	}
	remote = sampleRemote()
	plain := press(start(t, &fakeApplication{dashboard: sampleDashboard()}, Config{Multiplayer: remote}, 120, 0), "2", "y")
	if got := view(plain); !strings.Contains(got, "ovt_Q7M2KXsynthetic") || !strings.Contains(got, "Invite shown; copying is unavailable here") {
		t.Fatalf("invite without a clipboard = %q", got)
	}
}

func TestJoiningTheGlobalBoardNeedsAnExplicitYes(t *testing.T) {
	t.Parallel()
	remote := sampleRemote()
	model, _ := openBoard(t, remote, 120, 0)
	// o means nothing on a group board.
	if got := view(press(model, "o")); strings.Contains(got, "Join the global board?") {
		t.Fatalf("o opened a prompt on a group board: %q", got)
	}
	model = press(model, "g", "g", "o")
	if got := view(model); !strings.Contains(got, "Join the global board? Your GitHub login and totals become visible") || !strings.Contains(got, "y join · esc cancel") {
		t.Fatalf("join prompt = %q", got)
	}
	// Anything but y leaves visibility alone: enter, another letter, escape.
	for _, key := range []string{"enter", "n", "esc", "q"} {
		declined := press(model, key)
		if remote.participating != nil || strings.Contains(view(declined), "Join the global board?") {
			t.Fatalf("key %q: participating=%v view=%q", key, remote.participating, view(declined))
		}
	}
	joined := press(model, "y")
	got := view(joined)
	if remote.participating == nil || !*remote.participating {
		t.Fatal("y did not join")
	}
	for _, want := range []string{"LISTED   yes", "o leave", "@you  ★ YOU", "RANKS HIDDEN", "DONE  You are on the global board"} {
		if !strings.Contains(got, want) {
			t.Errorf("after joining = %q, want %q", got, want)
		}
	}
	leaving := press(joined, "o")
	if got := view(leaving); !strings.Contains(got, "Leave the global board?") {
		t.Fatalf("leave prompt = %q", got)
	}
	if got := view(press(leaving, "y")); *remote.participating || !strings.Contains(got, "LISTED   no") || strings.Contains(got, "★ YOU") {
		t.Fatalf("after leaving: participating=%v view=%q", *remote.participating, got)
	}
}

func TestCreateAndJoinPromptsTreatKeysAsText(t *testing.T) {
	t.Parallel()
	remote := sampleRemote()
	model, _ := openBoard(t, remote, 88, 0)
	model = press(model, "c", "New", " ", "group")
	if got := view(model); !strings.Contains(got, "Create group name: New group_") {
		t.Fatalf("create prompt = %q", got)
	}
	// While typing, q must not quit and 1 must not navigate.
	model = press(model, "q", "1", "g")
	if got := view(model); !strings.Contains(got, "New groupq1g_") || !strings.Contains(got, "[BOARDS]") {
		t.Fatalf("typed keys acted as commands: %q", got)
	}
	model = press(model, "enter")
	if got := view(model); remote.created != "New groupq1g" || remote.scope != "group-created" || !strings.Contains(got, "NEW GROUPQ1G") || !strings.Contains(got, "DONE  Group created") {
		t.Fatalf("created %q, scope %q, view %q", remote.created, remote.scope, got)
	}
	model = press(model, "i", "ovt_secret")
	if got := view(model); strings.Contains(got, "ovt_secret") || !strings.Contains(got, "**********_") || !strings.Contains(got, "code hidden") {
		t.Fatalf("invite code was not masked: %q", got)
	}
	if got := view(press(model, "esc")); strings.Contains(got, "Join with invite code") || remote.joined != "" {
		t.Fatalf("esc kept the prompt: %q", got)
	}
	if got := view(press(model, "enter")); remote.joined != "ovt_secret" || remote.scope != "group-joined" || !strings.Contains(got, "JOINED") {
		t.Fatalf("joined with %q, scope %q, view %q", remote.joined, remote.scope, got)
	}
	// A refused code names the failure and never echoes the code.
	remote.actionError = errors.New("synthetic refusal")
	if got := view(press(model, "i", "ovt_bad", "enter")); !strings.Contains(got, "Could not join with that invite") || strings.Contains(got, "ovt_bad") || strings.Contains(got, "synthetic refusal") {
		t.Fatalf("refused join = %q", got)
	}
}

func TestBoardGatesExplainWhatIsMissing(t *testing.T) {
	t.Parallel()
	app := &fakeApplication{dashboard: sampleDashboard()}
	unconfigured := view(press(start(t, app, Config{}, 88, 0), "2"))
	if !strings.Contains(unconfigured, "TOKENHOLIC_API_URL") || !strings.Contains(unconfigured, "tokenholic claim") || strings.Contains(unconfigured, "g scope") {
		t.Errorf("unconfigured = %q", unconfigured)
	}
	signedOut := view(press(start(t, app, Config{Multiplayer: &fakeMultiplayer{loadError: multiplayer.ErrSignedOut}}, 88, 0), "2"))
	if !strings.Contains(signedOut, "Sign in to join one") || !strings.Contains(signedOut, "tokenholic claim") || strings.Contains(signedOut, "g scope") {
		t.Errorf("signed out = %q", signedOut)
	}
	// With no group, g still reaches the global board.
	remote := &fakeMultiplayer{boards: map[string]*multiplayer.Board{multiplayer.GlobalScope: smallGlobal(false)}}
	model := press(start(t, app, Config{Multiplayer: remote}, 88, 0), "2")
	if got := view(model); !strings.Contains(got, "You are not in a group yet.") || !strings.Contains(got, "g to see the global board") {
		t.Errorf("no groups = %q", got)
	}
	if got := view(press(model, "g")); remote.scope != multiplayer.GlobalScope || !strings.Contains(got, "GLOBAL") || !strings.Contains(got, "SCOPE    1 of 1") {
		t.Errorf("global without groups: scope %q, view %q", remote.scope, got)
	}
}

func TestBoardFailureKeepsTheLastBoardAndHidesTheCause(t *testing.T) {
	t.Parallel()
	remote := sampleRemote()
	model, _ := openBoard(t, remote, 88, 0)
	remote.loadError = errors.New("GET https://api.example.test/v1/groups: synthetic outage")
	failed := press(model, "r")
	got := view(failed)
	if !strings.Contains(got, "ERROR  Could not load the board. Press r to retry.") || !strings.Contains(got, "@dana") || strings.Contains(got, "api.example.test") {
		t.Fatalf("failed reload = %q", got)
	}
	remote.loadError = nil
	if got := view(press(failed, "r")); strings.Contains(got, "ERROR") {
		t.Fatalf("a later success kept the failure: %q", got)
	}
	// A session that expires mid-use returns to the sign-in gate.
	remote.loadError = multiplayer.ErrSignedOut
	if got := view(press(model, "r")); !strings.Contains(got, "Sign in to join one") || strings.Contains(got, "@dana") {
		t.Fatalf("expired session = %q", got)
	}
	// A failure before any board loaded says so instead of spinning forever.
	first := press(start(t, &fakeApplication{dashboard: sampleDashboard()}, Config{Multiplayer: &fakeMultiplayer{loadError: errors.New("synthetic outage")}}, 88, 0), "2")
	if got := view(first); !strings.Contains(got, "Could not load the board") || strings.Contains(got, "Loading boards") {
		t.Fatalf("first load failure = %q", got)
	}
}

func TestStaleBoardLoadsAreIgnored(t *testing.T) {
	t.Parallel()
	model, _ := openBoard(t, sampleRemote(), 88, 0)
	stale := multiplayer.Snapshot{Global: true, Board: smallGlobal(true)}
	updated, _ := model.Update(boardLoadResult{requestID: model.board.request - 1, snapshot: stale})
	if got := view(updated.(Model)); !strings.Contains(got, "NIGHT-SHIFT") {
		t.Fatalf("a stale load replaced the board: %q", got)
	}
}

func TestRemoteTextCannotControlTheTerminal(t *testing.T) {
	t.Parallel()
	remote := sampleRemote()
	board := nightShift()
	board.Rows[0].DisplayName = "dana\x1b[2J\x1b]52;c;AAAA\x07"
	board.Rows[0].UnrankedReason, board.Rows[0].Rank = "new\x1b[31m_reason", nil
	board.Timezone = "UTC\x1b[0m"
	remote.boards["group-night"] = board
	remote.groups[0].Name = "night\x1b[5mshift"
	model, _ := openBoard(t, remote, 120, 0)
	got := view(press(model, "k", "k", "k", "k"))
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Fatalf("remote text reached the terminal with control characters: %q", got)
	}
	// A reason this client does not know is shown, not hidden.
	if !strings.Contains(got, "new [31m_r") && !strings.Contains(got, "new [31m_reason") {
		t.Fatalf("unknown reason is not shown: %q", got)
	}
}

func TestTerminalClipboardWritesOneOSC52Sequence(t *testing.T) {
	t.Parallel()
	var output strings.Builder
	terminalClipboard(&output)("ovt_Q7M2KX")
	if got := output.String(); got != "\x1b]52;c;b3Z0X1E3TTJLWA==\x07" {
		t.Fatalf("clipboard sequence = %q", got)
	}
}

var _ tea.Model = Model{}
