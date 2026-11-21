package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	usageSync "github.com/ansonnchan/tokenholic/internal/sync"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/adapters"
	"github.com/ansonnchan/tokenholic/internal/autosync"
	"github.com/ansonnchan/tokenholic/internal/doctor"
	"github.com/ansonnchan/tokenholic/internal/multiplayer"
	"github.com/ansonnchan/tokenholic/internal/normalize"
)

func TestRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "version", args: []string{"--version"}, wantCode: 0, wantStdout: "dev\n"},
		{name: "unknown", args: []string{"collect"}, wantCode: 2, wantStderr: "unknown command"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			got := Run(tt.args, &stdout, &stderr)
			if got != tt.wantCode {
				t.Fatalf("Run() code = %d, want %d", got, tt.wantCode)
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want substring %q", stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want substring %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestRootCommandLaunchesLocalTUIWithEnvironment(t *testing.T) {
	t.Parallel()
	input := strings.NewReader("q")
	deps := dependencies{
		homeDir: func() (string, error) { return "/synthetic/home", nil },
		getenv: func(name string) string {
			switch name {
			case "CODEX_HOME":
				return "/synthetic/codex"
			case "NO_COLOR":
				return "1"
			default:
				return ""
			}
		},
		input: input,
		launch: func(ctx context.Context, home, codexHome, apiURL string, gotInput io.Reader, _ io.Writer, noColor bool) error {
			if ctx == nil || home != "/synthetic/home" || codexHome != "/synthetic/codex" || apiURL != "" || gotInput != input || !noColor {
				t.Fatalf("launch inputs = %v %q %q %q %v %v", ctx, home, codexHome, apiURL, gotInput, noColor)
			}
			return nil
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr, deps); code != 0 || stderr.Len() != 0 {
		t.Fatalf("root code/stdout/stderr = %d/%q/%q", code, stdout.String(), stderr.String())
	}
}

func TestRootCommandRedactsStartupFailure(t *testing.T) {
	t.Parallel()
	deps := dependencies{
		homeDir: func() (string, error) { return "/synthetic/home", nil },
		getenv:  func(string) string { return "" },
		input:   strings.NewReader("q"),
		launch: func(context.Context, string, string, string, io.Reader, io.Writer, bool) error {
			return errors.New("private/path/state.db")
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr, deps); code != 1 || !strings.Contains(stderr.String(), "tokenholic doctor") || strings.Contains(stderr.String(), "private/path") {
		t.Fatalf("root code/stderr = %d/%q", code, stderr.String())
	}
}

func TestLocalTimezoneResolvesLoadableLocation(t *testing.T) {
	zone := localTimezone()
	if zone == "" || zone == "Local" {
		t.Fatalf("localTimezone() = %q", zone)
	}
	if _, err := time.LoadLocation(zone); err != nil {
		t.Fatalf("localTimezone() = %q: %v", zone, err)
	}
}

func TestDoctorCommandUsesBoundedInjectedDiagnostics(t *testing.T) {
	t.Parallel()

	privateHome := "/synthetic/private-home"
	deps := dependencies{
		homeDir: func() (string, error) { return privateHome, nil },
		getenv:  func(string) string { return "" },
		timeout: time.Second,
		diagnose: func(ctx context.Context, home, codexHome string) doctor.Report {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("doctor context has no deadline")
			}
			if home != privateHome || codexHome != "" {
				t.Fatalf("diagnose inputs = %q/%q", home, codexHome)
			}
			return doctor.Report{GOOS: "darwin", GOARCH: "arm64", Findings: []adapters.Finding{{
				Kind: adapters.RootClaudeCLI, Status: normalize.HealthDetected, RecordCount: 1,
			}}}
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"doctor"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("doctor code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Claude Code CLI") || strings.Contains(stdout.String(), privateHome) {
		t.Fatalf("doctor stdout = %q", stdout.String())
	}
}

func TestDoctorCommandRejectsRelativeCodexHome(t *testing.T) {
	t.Parallel()

	deps := dependencies{
		homeDir: func() (string, error) { return "/synthetic/home", nil },
		getenv:  func(string) string { return "relative" },
		timeout: time.Second,
		diagnose: func(context.Context, string, string) doctor.Report {
			t.Fatal("diagnose should not run")
			return doctor.Report{}
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"doctor"}, &stdout, &stderr, deps); code != 1 || !strings.Contains(stderr.String(), "absolute") {
		t.Fatalf("doctor code/stdout/stderr = %d/%q/%q", code, stdout.String(), stderr.String())
	}
}

func TestDoctorCommandHandlesHomeResolutionFailure(t *testing.T) {
	t.Parallel()

	deps := dependencies{
		homeDir: func() (string, error) { return "", errors.New("synthetic") },
		getenv:  func(string) string { return "" }, timeout: time.Second,
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"doctor"}, &stdout, &stderr, deps); code != 1 || !strings.Contains(stderr.String(), "resolve") {
		t.Fatalf("doctor code/stderr = %d/%q", code, stderr.String())
	}
}

func TestAccountCommandsUseConfiguredAPIAndRedactFailures(t *testing.T) {
	t.Parallel()
	manager := &fakeAccountManager{}
	deps := dependencies{
		getenv: func(name string) string {
			if name == "TOKENHOLIC_API_URL" {
				return "https://api.example.test"
			}
			return ""
		},
		account: func(apiURL string) (accountManager, error) {
			if apiURL != "https://api.example.test" {
				t.Fatalf("api URL = %q", apiURL)
			}
			return manager, nil
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"login"}, &stdout, &stderr, deps); code != 0 || manager.loginVersion != version {
		t.Fatalf("login = %d/%q/%q manager=%#v", code, stdout.String(), stderr.String(), manager)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"logout"}, &stdout, &stderr, deps); code != 0 || !manager.loggedOut || !strings.Contains(stdout.String(), "revoked") {
		t.Fatalf("logout = %d/%q/%q manager=%#v", code, stdout.String(), stderr.String(), manager)
	}
	manager.loginErr = errors.New("secret provider token at /private/path")
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"login"}, &stdout, &stderr, deps); code != 1 || strings.Contains(stderr.String(), "provider token") || strings.Contains(stderr.String(), "/private/path") {
		t.Fatalf("failed login = %d/%q/%q", code, stdout.String(), stderr.String())
	}
}

func TestAccountCommandRequiresAPIURL(t *testing.T) {
	t.Parallel()
	deps := dependencies{getenv: func(string) string { return "" }}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"login"}, &stdout, &stderr, deps); code != 1 || !strings.Contains(stderr.String(), "TOKENHOLIC_API_URL") {
		t.Fatalf("login = %d/%q/%q", code, stdout.String(), stderr.String())
	}
}

func TestClaimLogsInAndPerformsInitialSync(t *testing.T) {
	t.Parallel()
	manager := &fakeAccountManager{}
	syncCalls := 0
	deps := dependencies{
		homeDir: func() (string, error) { return "/synthetic/home", nil },
		getenv: func(name string) string {
			if name == "TOKENHOLIC_API_URL" {
				return "https://api.example.test"
			}
			return ""
		},
		account: func(string) (accountManager, error) { return manager, nil },
		sync: func(_ context.Context, _, _, apiURL string, dryRun bool, output io.Writer) error {
			syncCalls++
			if apiURL != "https://api.example.test" || dryRun {
				t.Fatalf("claim sync inputs = %q/%v", apiURL, dryRun)
			}
			_, _ = io.WriteString(output, "Synchronized 1 bucket in 1 batch.\n")
			return nil
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"claim"}, &stdout, &stderr, deps); code != 0 || manager.loginVersion != version || syncCalls != 1 || stderr.Len() != 0 {
		t.Fatalf("claim = %d/%q/%q manager=%#v syncs=%d", code, stdout.String(), stderr.String(), manager, syncCalls)
	}
}

func TestSyncCommandTellsAnOutdatedClientToUpgrade(t *testing.T) {
	t.Parallel()
	deps := dependencies{
		homeDir: func() (string, error) { return "/synthetic/home", nil },
		getenv: func(name string) string {
			if name == "TOKENHOLIC_API_URL" {
				return "https://api.example.test"
			}
			return ""
		},
		sync: func(context.Context, string, string, string, bool, io.Writer) error {
			return fmt.Errorf("upload batch: %w", usageSync.ErrUpgradeRequired)
		},
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"sync"}, &stdout, &stderr, deps)
	if code != 1 || !strings.Contains(stderr.String(), "npx @tokenholic/cli@latest") || !strings.Contains(stderr.String(), "local usage was preserved") {
		t.Fatalf("sync = %d stderr=%q, want an upgrade instruction", code, stderr.String())
	}
}

func TestSyncCommandParsesDryRunAndRedactsFailure(t *testing.T) {
	t.Parallel()
	var gotDryRun bool
	apiURL := ""
	deps := dependencies{
		homeDir: func() (string, error) { return "/synthetic/home", nil },
		getenv: func(name string) string {
			if name == "TOKENHOLIC_API_URL" {
				return apiURL
			}
			return ""
		},
		sync: func(_ context.Context, home, codexHome, gotAPIURL string, dryRun bool, output io.Writer) error {
			if home != "/synthetic/home" || codexHome != "" || gotAPIURL != apiURL {
				t.Fatalf("sync inputs = %q/%q/%q", home, codexHome, gotAPIURL)
			}
			gotDryRun = dryRun
			_, _ = io.WriteString(output, "{\"schema_version\":2}\n")
			return nil
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"sync", "--dry-run"}, &stdout, &stderr, deps); code != 0 || !gotDryRun || !strings.Contains(stdout.String(), "schema_version") {
		t.Fatalf("sync dry run = %d/%q/%q dry=%v", code, stdout.String(), stderr.String(), gotDryRun)
	}
	gotDryRun = false
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"show-data"}, &stdout, &stderr, deps); code != 0 || !gotDryRun || !strings.Contains(stdout.String(), "schema_version") {
		t.Fatalf("show-data = %d/%q/%q dry=%v", code, stdout.String(), stderr.String(), gotDryRun)
	}
	apiURL = "https://api.example.test"
	deps.sync = func(context.Context, string, string, string, bool, io.Writer) error {
		return errors.New("private/path and credential")
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"sync"}, &stdout, &stderr, deps); code != 1 || strings.Contains(stderr.String(), "private/path") || strings.Contains(stderr.String(), "credential") {
		t.Fatalf("failed sync = %d/%q/%q", code, stdout.String(), stderr.String())
	}
	deps.sync = func(context.Context, string, string, string, bool, io.Writer) error {
		return autosync.ErrAlreadyRunning
	}
	stderr.Reset()
	if code := run([]string{"sync"}, &stdout, &stderr, deps); code != 1 || !strings.Contains(stderr.String(), "already running") {
		t.Fatalf("overlapping sync = %d/%q", code, stderr.String())
	}
}

func TestAutosyncCommandsUseExplicitManagerBoundary(t *testing.T) {
	manager := &fakeAutosyncManager{status: autosync.Status{Installed: true, Loaded: true}}
	managerCalls := 0
	deps := dependencies{
		homeDir:    func() (string, error) { return "/synthetic/home", nil },
		executable: func() (string, error) { return "/synthetic/bin/tokenholic", nil },
		getenv: func(name string) string {
			switch name {
			case "TOKENHOLIC_API_URL":
				return "https://api.example.test"
			case "CODEX_HOME":
				return "/synthetic/codex"
			default:
				return ""
			}
		},
		autosync: func(home, executable, apiURL, codexHome string) (autosyncManager, error) {
			managerCalls++
			if home != "/synthetic/home" || executable != "/synthetic/bin/tokenholic" {
				t.Fatalf("autosync inputs = %q/%q/%q/%q", home, executable, apiURL, codexHome)
			}
			if managerCalls == 1 && (apiURL != "https://api.example.test" || codexHome != "/synthetic/codex") {
				t.Fatalf("enable autosync inputs = %q/%q", apiURL, codexHome)
			}
			if managerCalls > 1 && (apiURL != "" || codexHome != "") {
				t.Fatalf("non-enable autosync inputs = %q/%q", apiURL, codexHome)
			}
			return manager, nil
		},
	}
	for _, test := range []struct {
		command string
		want    string
	}{
		{command: "enable", want: "Enabled autosync"},
		{command: "status", want: "installed and loaded"},
		{command: "disable", want: "Local usage was not changed"},
	} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := run([]string{"autosync", test.command}, &stdout, &stderr, deps); code != 0 || !strings.Contains(stdout.String(), test.want) || stderr.Len() != 0 {
			t.Fatalf("autosync %s = %d/%q/%q", test.command, code, stdout.String(), stderr.String())
		}
	}
	if !manager.enabled || !manager.disabled {
		t.Fatalf("autosync manager = %#v", manager)
	}
}

func TestAutosyncEnableRequiresAPIAndRedactsFailure(t *testing.T) {
	base := dependencies{
		homeDir:    func() (string, error) { return "/synthetic/home", nil },
		executable: func() (string, error) { return "/synthetic/bin/tokenholic", nil },
		getenv:     func(string) string { return "" },
		autosync: func(string, string, string, string) (autosyncManager, error) {
			return &fakeAutosyncManager{}, nil
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"autosync", "enable"}, &stdout, &stderr, base); code != 1 || !strings.Contains(stderr.String(), "TOKENHOLIC_API_URL") {
		t.Fatalf("missing API = %d/%q", code, stderr.String())
	}
	base.getenv = func(string) string { return "https://api.example.test" }
	base.autosync = func(string, string, string, string) (autosyncManager, error) {
		return &fakeAutosyncManager{err: errors.New("private credential /private/path")}, nil
	}
	stderr.Reset()
	if code := run([]string{"autosync", "enable"}, &stdout, &stderr, base); code != 1 || strings.Contains(stderr.String(), "private") || strings.Contains(stderr.String(), "credential") {
		t.Fatalf("failed enable = %d/%q", code, stderr.String())
	}
}

func TestGroupCommandsUseConfiguredMultiplayerBoundary(t *testing.T) {
	manager := &fakeGroupManager{groups: []multiplayer.Group{{ID: "group-id", Name: "Quiet group", Role: "owner", Timezone: "UTC"}}}
	deps := dependencies{
		getenv: func(name string) string {
			if name == "TOKENHOLIC_API_URL" {
				return "https://api.example.test"
			}
			return ""
		},
		groups: func(apiURL string) (groupManager, error) {
			if apiURL != "https://api.example.test" {
				t.Fatalf("api URL = %q", apiURL)
			}
			return manager, nil
		},
	}
	tests := []struct {
		args []string
		want string
	}{
		{args: []string{"group", "list"}, want: "Quiet group · owner · UTC"},
		{args: []string{"group", "create", "New group"}, want: "Created private group New group"},
		{args: []string{"group", "join", "ovt_synthetic"}, want: "Joined private group Joined"},
		{args: []string{"group", "invite", "--expires", "1h", "--uses", "2", "Quiet group"}, want: "Invite code: ovt_invite"},
		{args: []string{"group", "leave", "Quiet group"}, want: "Left private group Quiet group"},
	}
	for _, test := range tests {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := run(test.args, &stdout, &stderr, deps); code != 0 || !strings.Contains(stdout.String(), test.want) || stderr.Len() != 0 {
			t.Fatalf("run(%v) = %d/%q/%q", test.args, code, stdout.String(), stderr.String())
		}
	}
	if manager.created != "New group" || manager.joined != "ovt_synthetic" || manager.inviteUses != 2 || manager.left != "Quiet group" {
		t.Fatalf("group manager = %#v", manager)
	}
}

func TestGlobalCommandJoinsAndLeavesOnlyWhenAsked(t *testing.T) {
	manager := &fakeGroupManager{}
	deps := dependencies{
		getenv: func(string) string { return "https://api.example.test" },
		groups: func(string) (groupManager, error) { return manager, nil },
	}
	tests := []struct {
		args     []string
		wantCode int
		wantSent *bool
		wantOut  string
		wantErr  string
	}{
		{args: []string{"global", "join"}, wantSent: boolPointer(true), wantOut: "visible to every signed-in tokenholic user"},
		{args: []string{"global", "leave"}, wantSent: boolPointer(false), wantOut: "no longer listed"},
		{args: []string{"global"}, wantCode: 2, wantErr: "join or leave"},
		{args: []string{"global", "status"}, wantCode: 2, wantErr: "join or leave"},
		{args: []string{"global", "join", "now"}, wantCode: 2, wantErr: "join or leave"},
	}
	for _, test := range tests {
		manager.participating = nil
		var stdout, stderr bytes.Buffer
		code := run(test.args, &stdout, &stderr, deps)
		if code != test.wantCode || !strings.Contains(stdout.String(), test.wantOut) || !strings.Contains(stderr.String(), test.wantErr) {
			t.Fatalf("run(%v) = %d/%q/%q", test.args, code, stdout.String(), stderr.String())
		}
		if (manager.participating == nil) != (test.wantSent == nil) || manager.participating != nil && *manager.participating != *test.wantSent {
			t.Fatalf("run(%v) sent %v, want %v", test.args, manager.participating, test.wantSent)
		}
	}

	// A failure reports nothing as changed and leaks no detail.
	manager.err, manager.participating = errors.New("credential at /private/path"), nil
	var stdout, stderr bytes.Buffer
	if code := run([]string{"global", "join"}, &stdout, &stderr, deps); code != 1 || stdout.Len() != 0 || strings.Contains(stderr.String(), "/private/path") {
		t.Fatalf("failed join = %d/%q/%q", code, stdout.String(), stderr.String())
	}
	stderr.Reset()
	if code := run([]string{"global", "join"}, &stdout, &stderr, dependencies{getenv: func(string) string { return "" }}); code != 1 || !strings.Contains(stderr.String(), "TOKENHOLIC_API_URL") {
		t.Fatalf("join without an API = %d/%q", code, stderr.String())
	}
}

func boolPointer(value bool) *bool { return &value }

func TestGroupCommandRequiresAPIAndRedactsFailure(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"group", "list"}, &stdout, &stderr, dependencies{getenv: func(string) string { return "" }}); code != 1 || !strings.Contains(stderr.String(), "TOKENHOLIC_API_URL") {
		t.Fatalf("missing API = %d/%q", code, stderr.String())
	}
	manager := &fakeGroupManager{err: errors.New("credential at /private/path")}
	deps := dependencies{
		getenv: func(string) string { return "https://api.example.test" },
		groups: func(string) (groupManager, error) { return manager, nil },
	}
	stderr.Reset()
	if code := run([]string{"group", "list"}, &stdout, &stderr, deps); code != 1 || strings.Contains(stderr.String(), "credential") || strings.Contains(stderr.String(), "/private/path") {
		t.Fatalf("failed group command = %d/%q", code, stderr.String())
	}
}

func TestAccountDeleteRequiresExactConfirmation(t *testing.T) {
	manager := &fakeAccountManager{}
	deps := dependencies{
		getenv: func(name string) string {
			if name == "TOKENHOLIC_API_URL" {
				return "https://api.example.test"
			}
			return ""
		},
		account: func(string) (accountManager, error) { return manager, nil },
	}
	for _, args := range [][]string{
		{"account", "delete"},
		{"account", "delete", "--confirm", "delete"},
	} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := run(args, &stdout, &stderr, deps); code != 2 || manager.deleted {
			t.Fatalf("run(%v) = %d/%q deleted=%v", args, code, stderr.String(), manager.deleted)
		}
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"account", "delete", "--confirm", "DELETE"}, &stdout, &stderr, deps); code != 0 || !manager.deleted || !strings.Contains(stdout.String(), "Local usage data was not changed") {
		t.Fatalf("confirmed delete = %d/%q/%q manager=%#v", code, stdout.String(), stderr.String(), manager)
	}
}

func TestAccountDeleteFailureKeepsStableOutput(t *testing.T) {
	manager := &fakeAccountManager{deleteErr: errors.New("credential /private/path")}
	deps := dependencies{
		getenv:  func(string) string { return "https://api.example.test" },
		account: func(string) (accountManager, error) { return manager, nil },
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"account", "delete", "--confirm", "DELETE"}, &stdout, &stderr, deps); code != 1 || strings.Contains(stderr.String(), "/private/path") {
		t.Fatalf("failed account delete = %d/%q", code, stderr.String())
	}
}

type fakeAccountManager struct {
	loginVersion string
	loginErr     error
	loggedOut    bool
	deleted      bool
	deleteErr    error
}

type fakeGroupManager struct {
	groups        []multiplayer.Group
	created       string
	joined        string
	inviteUses    int
	left          string
	participating *bool
	err           error
}

func (f *fakeGroupManager) SetGlobalParticipation(_ context.Context, participating bool) error {
	if f.err == nil {
		f.participating = &participating
	}
	return f.err
}

type fakeAutosyncManager struct {
	status   autosync.Status
	err      error
	enabled  bool
	disabled bool
}

func (f *fakeAutosyncManager) Enable(context.Context) error {
	f.enabled = true
	return f.err
}

func (f *fakeAutosyncManager) Disable(context.Context) error {
	f.disabled = true
	return f.err
}

func (f *fakeAutosyncManager) Status(context.Context) (autosync.Status, error) {
	return f.status, f.err
}

func (f *fakeGroupManager) List(context.Context) ([]multiplayer.Group, error) {
	return f.groups, f.err
}
func (f *fakeGroupManager) Create(_ context.Context, name string) (multiplayer.Group, error) {
	f.created = name
	return multiplayer.Group{Name: name, Timezone: "UTC"}, f.err
}
func (f *fakeGroupManager) Join(_ context.Context, code string) (multiplayer.Group, error) {
	f.joined = code
	return multiplayer.Group{Name: "Joined"}, f.err
}
func (f *fakeGroupManager) Invite(_ context.Context, _ string, lifetime time.Duration, uses int) (multiplayer.Invite, error) {
	f.inviteUses = uses
	return multiplayer.Invite{Code: "ovt_invite", ExpiresAt: time.Now().UTC().Add(lifetime), MaxUses: uses}, f.err
}
func (f *fakeGroupManager) Leave(_ context.Context, group string) error {
	f.left = group
	return f.err
}

func (f *fakeAccountManager) Login(_ context.Context, version string, _ io.Writer) error {
	f.loginVersion = version
	return f.loginErr
}

func (f *fakeAccountManager) Logout(context.Context) error {
	f.loggedOut = true
	return nil
}

func (f *fakeAccountManager) DeleteAccount(context.Context) error {
	f.deleted = true
	return f.deleteErr
}
