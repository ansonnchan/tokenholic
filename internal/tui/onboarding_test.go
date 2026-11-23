package tui

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/ansonnchan/tokenholic/internal/multiplayer"
)

// claimable starts a signed-out device with the sample usage and records
// what it opens and copies.
func claimable(t *testing.T, device *fakeAccount, remote *fakeMultiplayer) (Model, *[]string, *[]string) {
	t.Helper()
	opened, copied := &[]string{}, &[]string{}
	config := Config{
		Account:   device,
		OpenURL:   func(link string) error { *opened = append(*opened, link); return nil },
		Clipboard: func(text string) { *copied = append(*copied, text) },
	}
	if remote != nil {
		config.Multiplayer = remote
	}
	return start(t, &fakeApplication{dashboard: sampleDashboard()}, config, 88, 0), opened, copied
}

func TestFirstLaunchScansOnceAndRevealsTheHeadline(t *testing.T) {
	t.Parallel()
	app := firstLaunch(sampleDashboard())
	model := start(t, app, Config{Account: &fakeAccount{}}, 120, 0)
	if app.refreshes != 1 || app.loads != 2 {
		t.Fatalf("first launch ran %d scans and %d loads", app.refreshes, app.loads)
	}
	got := view(model)
	for _, want := range []string{
		"SCANNED", "Claude CLI ✓", "Codex CLI and Desktop ✓",
		"YOU BURNED ≥4.26B TOKENS · ≥$1,180.42 EST · LAST 30 DAYS",
		"[ c  CLAIM YOUR SPOT ]", "Nothing leaves this machine until you claim.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("first launch = %q, want %q", got, want)
		}
	}
	// It is a greeting, not a dashboard: no tabs yet.
	if strings.Contains(got, "[HOME]") || strings.Contains(got, "not found") {
		t.Errorf("first launch shows tabs or a missing source: %q", got)
	}
	home := press(model, "enter")
	if got := view(home); !strings.Contains(got, "[HOME]") || !strings.Contains(got, "YOUR USAGE") || strings.Contains(got, "YOU BURNED") {
		t.Fatalf("after enter = %q", got)
	}
	// Coming back to an area reloads; it must not scan or greet again.
	if press(home, "3", "1"); app.refreshes != 1 {
		t.Fatalf("a later load scanned again: %d scans", app.refreshes)
	}
	if got := view(press(model, "4")); !strings.Contains(got, "[SOURCES]") || strings.Contains(got, "YOU BURNED") {
		t.Errorf("a view key from the greeting = %q", got)
	}
}

func TestALaterLaunchGoesStraightToHome(t *testing.T) {
	t.Parallel()
	app := &fakeApplication{dashboard: sampleDashboard()}
	if got := view(start(t, app, Config{Account: &fakeAccount{}}, 88, 0)); app.refreshes != 0 || strings.Contains(got, "YOU BURNED") || !strings.Contains(got, "[HOME]") {
		t.Fatalf("later launch scanned %d times: %q", app.refreshes, got)
	}
}

func TestFirstLaunchStatesWhatItCouldNotDo(t *testing.T) {
	t.Parallel()
	// Nothing found: instructions, never a headline of zero.
	nothing := view(start(t, firstLaunch(emptyDashboard()), Config{}, 88, 0))
	if !strings.Contains(nothing, "No supported coding-agent usage found.") || strings.Contains(nothing, "YOU BURNED") || strings.Contains(nothing, " 0 TOKENS") {
		t.Errorf("empty machine = %q", nothing)
	}
	// With no API there is no claim to offer.
	local := view(start(t, firstLaunch(sampleDashboard()), Config{}, 88, 0))
	if strings.Contains(local, "CLAIM") || !strings.Contains(local, "[ enter  OPEN THE DASHBOARD ]") {
		t.Errorf("local-only first launch = %q", local)
	}
	// A failed scan says so and keeps the cause off the screen.
	app := firstLaunch(sampleDashboard())
	app.refreshErr = errors.New("open /synthetic/private/path: denied")
	failed := view(start(t, app, Config{}, 88, 0))
	if !strings.Contains(failed, "Could not refresh local data") || strings.Contains(failed, "synthetic/private") {
		t.Errorf("failed first scan = %q", failed)
	}
}

func TestClaimIsOfferedOnlyToASignedOutDevice(t *testing.T) {
	t.Parallel()
	app := &fakeApplication{dashboard: sampleDashboard()}
	cases := []struct {
		name   string
		device Account
		want   bool
	}{
		{"signed out", &fakeAccount{}, true},
		{"signed in", &fakeAccount{login: "you"}, false},
		// An unreadable vault is not proof of being signed out.
		{"vault unreadable", &fakeAccount{loginErr: errors.New("synthetic vault failure")}, false},
		{"no API", nil, false},
	}
	for _, test := range cases {
		model := start(t, app, Config{Account: test.device}, 88, 0)
		if got := strings.Contains(view(model), "CLAIM YOUR SPOT"); got != test.want {
			t.Errorf("%s: claim offered = %t, want %t", test.name, got, test.want)
		}
		if opened := press(model, "c"); (opened.claim.stage != claimClosed) != test.want {
			t.Errorf("%s: c opened the claim = %t, want %t", test.name, opened.claim.stage != claimClosed, test.want)
		}
	}
	signedIn := view(press(start(t, app, Config{Account: &fakeAccount{login: "you"}}, 88, 0), "5"))
	if !strings.Contains(signedIn, "signed in as @you") {
		t.Errorf("settings = %q", signedIn)
	}
}

func TestNothingStartsOrUploadsBeforeTheUserConfirms(t *testing.T) {
	t.Parallel()
	device := &fakeAccount{completeErr: errHeld}
	model, opened, copied := claimable(t, device, nil)
	model = press(model, "c")
	confirm := view(model)
	for _, want := range []string{"CLAIM YOUR SPOT", "UPLOADED", "token counts per category", "NEVER", "prompts, responses, code, paths", "tokenholic show-data", "[ enter  CONTINUE WITH GITHUB ]"} {
		if !strings.Contains(confirm, want) {
			t.Errorf("confirmation = %q, want %q", confirm, want)
		}
	}
	if device.begun != 0 || device.synced != 0 {
		t.Fatalf("opening the claim began %d logins and %d uploads", device.begun, device.synced)
	}
	// Escape backs out with nothing done.
	if backed := press(model, "esc"); backed.claim.stage != claimClosed || device.begun != 0 {
		t.Fatalf("esc: stage %d, %d logins begun", backed.claim.stage, device.begun)
	}

	waiting := press(model, "enter")
	code := view(waiting)
	for _, want := range []string{"https://github.com/login/device", "WDJB-MJHT", "Waiting for you to approve at GitHub", "[ o  OPEN THE PAGE ]"} {
		if !strings.Contains(code, want) {
			t.Errorf("code step = %q, want %q", code, want)
		}
	}
	// The user is still at GitHub: signed out, and nothing uploaded.
	if device.begun != 1 || device.synced != 0 || waiting.login != "" || strings.Contains(code, "synthetic-flow-token") {
		t.Fatalf("while waiting: begun %d, uploads %d, login %q", device.begun, device.synced, waiting.login)
	}
	waiting = press(waiting, "o", "y")
	if len(*opened) != 1 || (*opened)[0] != "https://github.com/login/device" || len(*copied) != 1 || (*copied)[0] != "WDJB-MJHT" {
		t.Fatalf("opened %v, copied %v", *opened, *copied)
	}
	// Every other key is ignored: none quits, navigates, or starts a scan.
	held := press(waiting, "q", "1", "r", "?", "s")
	if held.claim.stage != claimWaiting || !strings.Contains(view(held), "WDJB-MJHT") {
		t.Fatalf("a key escaped the claim: stage %d", held.claim.stage)
	}

	// Cancelling drops the login: an approval that arrives late signs
	// nobody in and uploads nothing.
	cancelled := press(waiting, "esc")
	late, _ := cancelled.Update(claimCompleted{id: waiting.claim.id, login: "you"})
	if got := late.(Model); got.login != "" || got.claim.stage != claimClosed || device.synced != 0 {
		t.Fatalf("after cancel: login %q, stage %d, uploads %d", got.login, got.claim.stage, device.synced)
	}
}

func TestClaimSignsInUploadsAndOpensTheBoard(t *testing.T) {
	t.Parallel()
	device := &fakeAccount{uploaded: 4014}
	remote := sampleRemote()
	model, _, _ := claimable(t, device, remote)
	claimed := press(model, "c", "enter")
	got := view(claimed)
	if device.begun != 1 || device.completed != 1 || device.synced != 1 || claimed.login != "you" {
		t.Fatalf("begun %d, completed %d, uploads %d, login %q", device.begun, device.completed, device.synced, claimed.login)
	}
	for _, want := range []string{"[BOARDS]", "NIGHT-SHIFT", "DONE  Signed in as @you · 4,014 buckets uploaded"} {
		if !strings.Contains(got, want) {
			t.Errorf("after claiming = %q, want %q", got, want)
		}
	}
	// Signed in: the claim is gone, and c in BOARDS creates a group again.
	if strings.Contains(view(press(claimed, "1")), "CLAIM YOUR SPOT") {
		t.Error("the claim is still offered after signing in")
	}
	if got := view(press(claimed, "c")); !strings.Contains(got, "Create group name:") {
		t.Errorf("c on the board after claiming = %q", got)
	}
}

func TestClaimFailuresAreRecoverableAndHideTheCause(t *testing.T) {
	t.Parallel()
	// The code could not be requested.
	device := &fakeAccount{beginErr: errors.New("POST https://api.example.test/v1/auth: synthetic outage")}
	model, _, _ := claimable(t, device, nil)
	failed := press(model, "c", "enter")
	if got := view(failed); !strings.Contains(got, "ERROR  Could not start sign-in.") || strings.Contains(got, "api.example.test") || !strings.Contains(got, "[ enter  TRY AGAIN ]") {
		t.Fatalf("failed start = %q", got)
	}
	device.beginErr = nil
	if retried := press(failed, "enter"); device.begun != 2 || retried.login != "you" {
		t.Fatalf("retry: begun %d, login %q", device.begun, retried.login)
	}

	// The code expired or was refused.
	device = &fakeAccount{completeErr: errors.New("synthetic: access_denied")}
	model, _, _ = claimable(t, device, nil)
	if got := view(press(model, "c", "enter")); !strings.Contains(got, "the code expired or was not approved") || strings.Contains(got, "access_denied") || device.synced != 0 {
		t.Fatalf("refused code = %q after %d uploads", got, device.synced)
	}

	// Signed in, but the first upload failed: retrying uploads again
	// without another login, and escape continues signed in.
	device = &fakeAccount{syncErr: errors.New("PUT https://api.example.test/v1/sync: synthetic outage")}
	model, _, _ = claimable(t, device, nil)
	stuck := press(model, "c", "enter")
	if got := view(stuck); !strings.Contains(got, "Signed in, but the first upload did not finish. Your usage is kept.") || strings.Contains(got, "api.example.test") || !strings.Contains(got, "[ enter  RETRY THE UPLOAD ]") {
		t.Fatalf("failed upload = %q", got)
	}
	device.syncErr = nil
	if done := press(stuck, "enter"); device.begun != 1 || device.synced != 2 || done.claim.stage != claimClosed || done.section != BoardsSection {
		t.Fatalf("upload retry: begun %d, uploads %d, stage %d", device.begun, device.synced, done.claim.stage)
	}
	if continued := press(stuck, "esc"); continued.login != "you" || continued.claim.stage != claimClosed || strings.Contains(view(continued), "CLAIM YOUR SPOT") {
		t.Fatalf("continuing without the upload: login %q", continued.login)
	}

	for failure, want := range map[error]string{
		ErrUpgradeRequired: "@tokenholic/cli@latest",
		ErrSyncBusy:        "another sync is running",
	} {
		device = &fakeAccount{syncErr: failure}
		model, _, _ = claimable(t, device, nil)
		if got := view(press(model, "c", "enter")); !strings.Contains(strings.Join(strings.Fields(got), " "), want) {
			t.Errorf("%v: view = %q, want %q", failure, got, want)
		}
	}
}

func TestAnExpiredSessionIsOfferedTheClaimAgain(t *testing.T) {
	t.Parallel()
	device := &fakeAccount{login: "you"}
	remote := &fakeMultiplayer{loadError: multiplayer.ErrSignedOut}
	model, _, _ := claimable(t, device, remote)
	board := press(model, "2")
	if got := view(board); !strings.Contains(got, "[ c  CLAIM YOUR SPOT ]") || !strings.Contains(got, "Nothing uploads until you claim.") {
		t.Fatalf("board with an expired session = %q", got)
	}
	if opened := press(board, "c"); opened.claim.stage != claimConfirm {
		t.Fatalf("c at the sign-in gate: stage %d", opened.claim.stage)
	}
}

func TestShareDraftsTheUsersOwnTotalsAndSendsNothing(t *testing.T) {
	t.Parallel()
	device := &fakeAccount{}
	model, opened, copied := claimable(t, device, sampleRemote())
	shared := press(model, "s")
	draft := view(shared)
	for _, want := range []string{"SHARE", "I burned at least 4.26B tokens with coding agents in the last 30 days.", "(an estimate)", ">_ npx @tokenholic/cli", "https://x.com/intent/post?text="} {
		if !strings.Contains(strings.Join(strings.Fields(strings.ReplaceAll(draft, "│", "")), " "), want) {
			t.Errorf("share = %q, want %q", draft, want)
		}
	}
	link, err := url.Parse(shared.share.link)
	if err != nil || link.Scheme != "https" || link.Host != "x.com" || link.Path != "/intent/post" || link.Query().Get("text") != shared.share.text {
		t.Fatalf("link = %q, %v", shared.share.link, err)
	}
	// An estimate is never spending, and a private group is never named.
	for _, banned := range []string{"spent", "spend", "paid", "night-shift", "day-crew", "@dana"} {
		if strings.Contains(strings.ToLower(shared.share.text), banned) {
			t.Errorf("draft contains %q: %q", banned, shared.share.text)
		}
	}
	// Drafting sends nothing: no login, no upload, nothing opened yet.
	if device.begun != 0 || device.synced != 0 || len(*opened) != 0 {
		t.Fatalf("share began %d logins, %d uploads, opened %v", device.begun, device.synced, *opened)
	}
	shared = press(shared, "y", "o")
	if len(*copied) != 1 || (*copied)[0] != shared.share.link || len(*opened) != 1 || (*opened)[0] != shared.share.link {
		t.Fatalf("copied %v, opened %v", *copied, *opened)
	}
	closed := press(shared, "esc")
	if closed.share.open || closed.sharedLink != shared.share.link {
		t.Fatalf("after closing: open %t, remembered %q", closed.share.open, closed.sharedLink)
	}
	// The draft follows the period on screen, and the board can share too.
	if today := press(model, "p", "p", "s"); !strings.Contains(today.share.text, "tokens with coding agents today.") {
		t.Errorf("today's draft = %q", today.share.text)
	}
	if board := press(model, "2", "s"); !board.share.open || strings.Contains(board.share.text, "night-shift") {
		t.Errorf("share from the board: open %t, text %q", board.share.open, board.share.text)
	}
	// Elsewhere s does nothing, and a period with no usage has no draft.
	if press(model, "3", "s").share.open {
		t.Error("s opened a share from MODELS")
	}
	quiet := start(t, &fakeApplication{dashboard: quietToday()}, Config{}, 88, 0)
	if press(quiet, "p", "p", "s").share.open {
		t.Error("s drafted a post for a period with no usage")
	}
}

func TestShareLinkIsPrintedAfterTheViewCloses(t *testing.T) {
	t.Parallel()
	model, _, _ := claimable(t, &fakeAccount{}, nil)
	var output strings.Builder
	writeSharedLink(&output, model)
	if output.Len() != 0 {
		t.Fatalf("printed %q without a share", output.String())
	}
	shared := press(model, "s", "esc")
	writeSharedLink(&output, shared)
	if got := output.String(); got != "Share link:\n"+shared.sharedLink+"\n" || !strings.HasPrefix(shared.sharedLink, "https://x.com/intent/post?text=") {
		t.Fatalf("printed %q", got)
	}
}

func TestOpeningALinkSaysWhenItCannot(t *testing.T) {
	t.Parallel()
	app := &fakeApplication{dashboard: sampleDashboard()}
	none := press(start(t, app, Config{}, 88, 0), "s", "o", "y")
	if got := view(none); !strings.Contains(got, "copying is unavailable here") {
		t.Errorf("share without a clipboard = %q", got)
	}
	if got := view(press(start(t, app, Config{}, 88, 0), "s", "o")); !strings.Contains(got, "no browser can be started from here") {
		t.Errorf("share without a browser = %q", got)
	}
	failing := Config{OpenURL: func(string) error { return errors.New("synthetic: no opener at /synthetic/bin") }}
	if got := view(press(start(t, app, failing, 88, 0), "s", "o")); !strings.Contains(got, "Could not open a browser") || strings.Contains(got, "synthetic") {
		t.Errorf("share with a failing browser = %q", got)
	}
}

// scheduled starts a signed-out device whose claim can offer hourly sync.
func scheduled(t *testing.T, device *fakeAccount, scheduler *fakeAutosync) Model {
	t.Helper()
	config := Config{Account: device, Autosync: scheduler, Multiplayer: sampleRemote()}
	return start(t, &fakeApplication{dashboard: sampleDashboard()}, config, 88, 0)
}

// Hourly sync is asked for once, after the first upload, and installed only
// on a yes.
func TestClaimOffersHourlySyncAndInstallsItOnlyOnYes(t *testing.T) {
	t.Parallel()
	scheduler := &fakeAutosync{}
	asked := press(scheduled(t, &fakeAccount{uploaded: 4014}, scheduler), "c", "enter")
	offer := view(asked)
	for _, want := range []string{"KEEP IT CURRENT", "4,014 buckets uploaded", "TURN ON HOURLY SYNC", "esc not now", "`tokenholic autosync disable`"} {
		if !strings.Contains(offer, want) {
			t.Errorf("offer = %q, want %q", offer, want)
		}
	}
	if scheduler.asked != 1 || scheduler.enabled != 0 {
		t.Fatalf("before an answer: asked %d, enabled %d", scheduler.asked, scheduler.enabled)
	}
	// Any other key is not a yes.
	if press(asked, "y", "q", "1"); scheduler.enabled != 0 {
		t.Fatalf("a key other than enter installed the schedule")
	}

	yes := view(press(asked, "enter"))
	if scheduler.enabled != 1 || !strings.Contains(yes, "[BOARDS]") || !strings.Contains(yes, "DONE  Signed in as @you · 4,014 buckets uploaded · hourly sync on") {
		t.Fatalf("after yes: enabled %d, view %q", scheduler.enabled, yes)
	}

	decliner := &fakeAutosync{}
	no := view(press(scheduled(t, &fakeAccount{uploaded: 4014}, decliner), "c", "enter", "esc"))
	if decliner.enabled != 0 || !strings.Contains(no, "[BOARDS]") || !strings.Contains(no, "DONE  Signed in as @you · 4,014 buckets uploaded") || strings.Contains(no, "hourly sync") {
		t.Fatalf("after no: enabled %d, view %q", decliner.enabled, no)
	}
}

func TestClaimSkipsTheOfferWhenThereIsNothingToOffer(t *testing.T) {
	t.Parallel()
	// Already scheduled, or a system that cannot schedule.
	scheduler := &fakeAutosync{unavailable: true}
	got := view(press(scheduled(t, &fakeAccount{uploaded: 4014}, scheduler), "c", "enter"))
	if scheduler.asked != 1 || scheduler.enabled != 0 || strings.Contains(got, "KEEP IT CURRENT") || !strings.Contains(got, "[BOARDS]") {
		t.Fatalf("unavailable: asked %d, enabled %d, view %q", scheduler.asked, scheduler.enabled, got)
	}
	// An upload that did not finish is not followed by an offer to repeat it hourly.
	failing := &fakeAutosync{}
	got = view(press(scheduled(t, &fakeAccount{syncErr: errors.New("synthetic: unreachable")}, failing), "c", "enter"))
	if failing.asked != 0 || strings.Contains(got, "KEEP IT CURRENT") || !strings.Contains(got, "the first upload did not finish") {
		t.Fatalf("failed upload: asked %d, view %q", failing.asked, got)
	}
}

func TestClaimSurvivesASchedulerThatRefuses(t *testing.T) {
	t.Parallel()
	scheduler := &fakeAutosync{enableErr: errors.New("synthetic: /private/path is not writable")}
	refused := press(scheduled(t, &fakeAccount{uploaded: 4014}, scheduler), "c", "enter", "enter")
	got := view(refused)
	if !strings.Contains(got, "could not be turned on here") || !strings.Contains(got, "`tokenholic autosync enable`") || strings.Contains(got, "/private/path") {
		t.Fatalf("refusal = %q", got)
	}
	if refused.login != "you" {
		t.Fatalf("a refused schedule lost the sign-in: login %q", refused.login)
	}
	after := view(press(refused, "enter"))
	if !strings.Contains(after, "[BOARDS]") || !strings.Contains(after, "4,014 buckets uploaded") || strings.Contains(after, "hourly sync on") {
		t.Fatalf("after continuing = %q", after)
	}
}

// The claim says how far its first upload has got, and how far it got when
// it stops, without the cause.
func TestClaimShowsHowFarTheUploadHasGot(t *testing.T) {
	t.Parallel()
	flat := func(model Model) string { return strings.Join(strings.Fields(view(model)), " ") }

	// Before the upload reports, there is nothing to count.
	model, _, _ := claimable(t, &fakeAccount{syncErr: errHeld}, nil)
	if got := flat(press(model, "c", "enter")); !strings.Contains(got, "Uploading your usage") || strings.Contains(got, "UPLOADED") {
		t.Fatalf("an upload that has not reported = %q", got)
	}

	// One request is over quickly: the count, and no talk of minutes.
	model, _, _ = claimable(t, &fakeAccount{syncErr: errHeld, progress: []UploadProgress{{Pending: 37, Batches: 1}}}, nil)
	if got := flat(press(model, "c", "enter")); !strings.Contains(got, "UPLOADED 0 of 37 buckets") || strings.Contains(got, "minutes") {
		t.Fatalf("a short upload = %q", got)
	}

	model, _, _ = claimable(t, &fakeAccount{syncErr: errHeld, progress: longUpload}, nil)
	got := flat(press(model, "c", "enter"))
	for _, want := range []string{"UPLOADED 300 of 1,240 buckets", "several minutes", "what is uploaded is kept"} {
		if !strings.Contains(got, want) {
			t.Errorf("a long upload = %q, want %q", got, want)
		}
	}

	device := &fakeAccount{syncErr: errors.New("PUT https://api.example.test/v2/sync: synthetic outage"), progress: longUpload}
	model, _, _ = claimable(t, device, nil)
	stopped := press(model, "c", "enter")
	got = flat(stopped)
	if !strings.Contains(got, "300 of 1,240 buckets uploaded before the upload stopped") || !strings.Contains(got, "a retry sends the rest") || strings.Contains(got, "api.example.test") {
		t.Fatalf("a stopped upload = %q", got)
	}
	// The retry starts its own count: what the last one sent is not pending.
	device.syncErr, device.progress = errHeld, []UploadProgress{{Uploaded: 100, Pending: 940, Batches: 10}}
	if got := flat(press(stopped, "enter")); !strings.Contains(got, "UPLOADED 100 of 940 buckets") {
		t.Fatalf("the retry = %q", got)
	}
}
