package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	usageSync "github.com/ansonnchan/tokenholic/internal/sync"
	"github.com/ansonnchan/tokenholic/internal/tui/theme"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/account"
	"github.com/ansonnchan/tokenholic/internal/adapters"
	"github.com/ansonnchan/tokenholic/internal/autosync"
	"github.com/ansonnchan/tokenholic/internal/doctor"
	"github.com/ansonnchan/tokenholic/internal/multiplayer"
	"github.com/ansonnchan/tokenholic/internal/normalize"
)

// Synthetic locations are absolute on every system and name nothing real:
// /synthetic/... on Unix and <drive>:\synthetic\... on Windows.
var (
	syntheticRoot        = filepath.Join(filepath.VolumeName(os.TempDir())+string(filepath.Separator), "synthetic")
	syntheticHome        = filepath.Join(syntheticRoot, "home")
	syntheticPrivateHome = filepath.Join(syntheticRoot, "private-home")
	syntheticCodexHome   = filepath.Join(syntheticRoot, "codex")
	syntheticBinary      = filepath.Join(syntheticRoot, "bin", "tokenholic")
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

// TOKENHOLIC_THEME is an optional override. Left unset, or set to anything
// that is not a look, it leaves the saved choice in charge.
func TestThemeVariableIsOnlyAnOverride(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]displayOptions{
		"":      {},
		"dark":  {mode: theme.Dark, modeSet: true},
		"LIGHT": {mode: theme.Light, modeSet: true},
		"auto":  {},
	} {
		got := displayFromEnvironment(func(name string) string {
			if name == "TOKENHOLIC_THEME" {
				return value
			}
			return ""
		})
		if got != want {
			t.Errorf("TOKENHOLIC_THEME=%q: %+v, want %+v", value, got, want)
		}
	}
}

func TestRootCommandLaunchesLocalTUIWithEnvironment(t *testing.T) {
	t.Parallel()
	input := strings.NewReader("q")
	deps := dependencies{
		homeDir: func() (string, error) { return syntheticHome, nil },
		getenv: func(name string) string {
			switch name {
			case "CODEX_HOME":
				return syntheticCodexHome
			case "NO_COLOR", "TOKENHOLIC_ASCII":
				return "1"
			case "TOKENHOLIC_THEME":
				return " Light "
			default:
				return ""
			}
		},
		input: input,
		launch: func(ctx context.Context, home, codexHome, apiURL string, gotInput io.Reader, _ io.Writer, display displayOptions) error {
			want := displayOptions{noColor: true, ascii: true, mode: theme.Light, modeSet: true}
			if ctx == nil || home != syntheticHome || codexHome != syntheticCodexHome || apiURL != "" || gotInput != input || display != want {
				t.Fatalf("launch inputs = %v %q %q %q %v %+v", ctx, home, codexHome, apiURL, gotInput, display)
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

func TestOpenBrowserRefusesAnythingButHTTPS(t *testing.T) {
	t.Parallel()
	// Only refusals are tested: an accepted link would start a real browser.
	for _, link := range []string{
		"", "http://github.com/login/device", "file:///synthetic/private", "javascript:alert(1)",
		"https://", "github.com/login/device", "x-apple-reminderkit://", " https://github.com/login/device\nmalicious",
	} {
		if err := openBrowser(link); err == nil {
			t.Errorf("openBrowser(%q) accepted the link", link)
		}
	}
}

func TestRootCommandRedactsStartupFailure(t *testing.T) {
	t.Parallel()
	deps := dependencies{
		homeDir: func() (string, error) { return syntheticHome, nil },
		getenv:  func(string) string { return "" },
		input:   strings.NewReader("q"),
		launch: func(context.Context, string, string, string, io.Reader, io.Writer, displayOptions) error {
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

	privateHome := syntheticPrivateHome
	deps := dependencies{
		homeDir: func() (string, error) { return privateHome, nil },
		getenv:  func(string) string { return "" },
		timeout: time.Second,
		diagnose: func(ctx context.Context, home, codexHome string, _ doctor.ParserProbe) doctor.Report {
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
		homeDir: func() (string, error) { return syntheticHome, nil },
		getenv:  func(string) string { return "relative" },
		timeout: time.Second,
		diagnose: func(context.Context, string, string, doctor.ParserProbe) doctor.Report {
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
		homeDir: func() (string, error) { return syntheticHome, nil },
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
		homeDir: func() (string, error) { return syntheticHome, nil },
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
		homeDir: func() (string, error) { return syntheticHome, nil },
		getenv: func(name string) string {
			if name == "TOKENHOLIC_API_URL" {
				return apiURL
			}
			return ""
		},
		sync: func(_ context.Context, home, codexHome, gotAPIURL string, dryRun bool, output io.Writer) error {
			if home != syntheticHome || codexHome != "" || gotAPIURL != apiURL {
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

func TestCommandsExplainAnUnavailableCredentialStore(t *testing.T) {
	unavailable := fmt.Errorf("read /private/path: %w", account.ErrVaultUnavailable)
	deps := dependencies{
		homeDir: func() (string, error) { return syntheticHome, nil },
		getenv: func(name string) string {
			if name == "TOKENHOLIC_API_URL" {
				return "https://api.example.test"
			}
			return ""
		},
		account: func(string) (accountManager, error) { return &fakeAccountManager{loginErr: unavailable}, nil },
		sync: func(context.Context, string, string, string, bool, io.Writer) error {
			return unavailable
		},
	}
	for _, command := range []string{"login", "claim", "sync"} {
		var stdout, stderr bytes.Buffer
		code := run([]string{command}, &stdout, &stderr, deps)
		if code != 1 || !strings.Contains(stderr.String(), account.VaultHelp()) || strings.Contains(stderr.String(), "/private/path") {
			t.Fatalf("%s = %d/%q", command, code, stderr.String())
		}
	}
}

// A release build carries the hosted API address, so no command needs a
// setting. TOKENHOLIC_API_URL replaces the address when it is set.
func TestCommandsUseTheBuiltInAPIAddressUnlessOverridden(t *testing.T) {
	const builtIn, override = "https://hosted.example.test", "https://self.example.test"
	for _, test := range []struct {
		name       string
		configured string
		want       string
		pinned     bool
		doctor     string
	}{
		{name: "nothing set", want: builtIn, doctor: "API address: built in; no setting needed"},
		{name: "override set", configured: override, want: override, pinned: true, doctor: "API address: set by TOKENHOLIC_API_URL"},
		{name: "override is only whitespace", configured: "  ", want: builtIn, doctor: "API address: built in"},
	} {
		t.Run(test.name, func(t *testing.T) {
			seen := map[string]string{}
			deps := dependencies{
				defaultAPIURL: builtIn,
				homeDir:       func() (string, error) { return syntheticHome, nil },
				executable:    func() (string, error) { return syntheticBinary, nil },
				timeout:       time.Second,
				getenv: func(name string) string {
					if name == "TOKENHOLIC_API_URL" {
						return test.configured
					}
					return ""
				},
				account: func(apiURL string) (accountManager, error) {
					seen["account"] = apiURL
					return &fakeAccountManager{}, nil
				},
				groups: func(apiURL string) (groupManager, error) {
					seen["groups"] = apiURL
					return &fakeGroupManager{}, nil
				},
				sync: func(_ context.Context, _, _, apiURL string, _ bool, _ io.Writer) error {
					seen["sync"] = apiURL
					return nil
				},
				launch: func(_ context.Context, _, _, apiURL string, _ io.Reader, _ io.Writer, _ displayOptions) error {
					seen["launch"] = apiURL
					return nil
				},
				autosync: func(config autosync.Config) (autosyncManager, error) {
					seen["autosync"] = config.APIURL
					if config.PinAPIURL != test.pinned {
						t.Errorf("autosync pins the address = %t, want %t", config.PinAPIURL, test.pinned)
					}
					return &fakeAutosyncManager{}, nil
				},
				diagnose: func(context.Context, string, string, doctor.ParserProbe) doctor.Report {
					return doctor.Report{GOOS: "darwin", GOARCH: "arm64"}
				},
			}
			var stdout, stderr bytes.Buffer
			for _, args := range [][]string{{"login"}, {"sync"}, {"group", "list"}, {"autosync", "enable"}, {}} {
				if code := run(args, &stdout, &stderr, deps); code != 0 {
					t.Fatalf("%v = %d/%q", args, code, stderr.String())
				}
			}
			for _, command := range []string{"account", "sync", "groups", "autosync", "launch"} {
				if seen[command] != test.want {
					t.Errorf("%s used %q, want %q", command, seen[command], test.want)
				}
			}
			stdout.Reset()
			run([]string{"doctor"}, &stdout, &stderr, deps)
			// The source is reported; the address itself never is.
			if !strings.Contains(stdout.String(), test.doctor) || strings.Contains(stdout.String(), "example.test") {
				t.Errorf("doctor = %q", stdout.String())
			}
		})
	}
}

// Only a development build has no address. It says so, and offline commands
// still work.
func TestDevelopmentBuildNamesTheMissingAPIAddress(t *testing.T) {
	deps := dependencies{
		homeDir:    func() (string, error) { return syntheticHome, nil },
		executable: func() (string, error) { return syntheticBinary, nil },
		timeout:    time.Second,
		getenv:     func(string) string { return "" },
		account:    func(string) (accountManager, error) { t.Fatal("account reached"); return nil, nil },
		groups:     func(string) (groupManager, error) { t.Fatal("groups reached"); return nil, nil },
		autosync:   func(autosync.Config) (autosyncManager, error) { return &fakeAutosyncManager{}, nil },
		sync: func(_ context.Context, _, _, apiURL string, dryRun bool, _ io.Writer) error {
			if !dryRun || apiURL != "" {
				t.Fatalf("sync reached with dryRun=%t apiURL=%q", dryRun, apiURL)
			}
			return nil
		},
		diagnose: func(context.Context, string, string, doctor.ParserProbe) doctor.Report {
			return doctor.Report{GOOS: "darwin", GOARCH: "arm64"}
		},
	}
	for _, args := range [][]string{{"login"}, {"claim"}, {"logout"}, {"sync"}, {"group", "list"}, {"global", "join"}, {"autosync", "enable"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr, deps); code != 1 || !strings.Contains(stderr.String(), noAPIAddress) {
			t.Errorf("%v = %d/%q", args, code, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"show-data"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("show-data = %d/%q", code, stderr.String())
	}
	run([]string{"doctor"}, &stdout, &stderr, deps)
	if !strings.Contains(stdout.String(), "API address: none; this is a development build") {
		t.Errorf("doctor = %q", stdout.String())
	}
}

func TestAutosyncCommandsUseExplicitManagerBoundary(t *testing.T) {
	manager := &fakeAutosyncManager{status: autosync.Status{Installed: true, Loaded: true}}
	managerCalls := 0
	deps := dependencies{
		homeDir:    func() (string, error) { return syntheticHome, nil },
		executable: func() (string, error) { return syntheticBinary, nil },
		getenv: func(name string) string {
			switch name {
			case "TOKENHOLIC_API_URL":
				return "https://api.example.test"
			case "CODEX_HOME":
				return syntheticCodexHome
			default:
				return ""
			}
		},
		autosync: func(config autosync.Config) (autosyncManager, error) {
			home, executable, apiURL, codexHome := config.Home, config.Executable, config.APIURL, config.CodexHome
			managerCalls++
			if config.Getenv == nil || config.Getenv("CODEX_HOME") != syntheticCodexHome {
				t.Fatal("autosync did not receive the command's environment")
			}
			if home != syntheticHome || executable != syntheticBinary {
				t.Fatalf("autosync inputs = %q/%q/%q/%q", home, executable, apiURL, codexHome)
			}
			if managerCalls == 1 && (apiURL != "https://api.example.test" || codexHome != syntheticCodexHome) {
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
		homeDir:    func() (string, error) { return syntheticHome, nil },
		executable: func() (string, error) { return syntheticBinary, nil },
		getenv:     func(string) string { return "" },
		autosync: func(autosync.Config) (autosyncManager, error) {
			return &fakeAutosyncManager{}, nil
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := run([]string{"autosync", "enable"}, &stdout, &stderr, base); code != 1 || !strings.Contains(stderr.String(), "TOKENHOLIC_API_URL") {
		t.Fatalf("missing API = %d/%q", code, stderr.String())
	}
	base.getenv = func(string) string { return "https://api.example.test" }
	base.autosync = func(autosync.Config) (autosyncManager, error) {
		return &fakeAutosyncManager{err: errors.New("private credential /private/path")}, nil
	}
	stderr.Reset()
	if code := run([]string{"autosync", "enable"}, &stdout, &stderr, base); code != 1 || strings.Contains(stderr.String(), "private") || strings.Contains(stderr.String(), "credential") {
		t.Fatalf("failed enable = %d/%q", code, stderr.String())
	}
}

// A schedule that starts a bare program hands it a job file. The run takes
// its environment and log files from that file and nothing else.
func TestScheduledRunUsesTheJobFile(t *testing.T) {
	directory := t.TempDir()
	stdout, stderr := filepath.Join(directory, "autosync.log"), filepath.Join(directory, "autosync-error.log")
	writeJob := func(t *testing.T, job autosync.ScheduledJob) string {
		t.Helper()
		encoded, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, "job.json")
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	var failure error
	deps := dependencies{
		homeDir: func() (string, error) { return syntheticHome, nil },
		// The process environment loses to the job file and fills the rest.
		getenv: func(name string) string {
			return map[string]string{"TOKENHOLIC_API_URL": "https://stale.example.test", "CODEX_HOME": syntheticCodexHome}[name]
		},
		sync: func(_ context.Context, _, codexHome, apiURL string, dryRun bool, output io.Writer) error {
			if dryRun || apiURL != "https://api.example.test" || codexHome != syntheticCodexHome {
				t.Fatalf("scheduled sync inputs = %q/%q/dryRun=%t", apiURL, codexHome, dryRun)
			}
			fmt.Fprintln(output, "Synchronized 2 buckets in 1 batches.")
			return failure
		},
	}
	job := writeJob(t, autosync.ScheduledJob{
		Environment: map[string]string{"TOKENHOLIC_API_URL": "https://api.example.test", "NO_COLOR": "1"},
		Stdout:      stdout, Stderr: stderr,
	})
	var terminalOut, terminalErr bytes.Buffer
	if code := run([]string{"autosync", "run", job}, &terminalOut, &terminalErr, deps); code != 0 {
		t.Fatalf("scheduled run = %d", code)
	}
	failure = fmt.Errorf("read /private/path: %w", account.ErrVaultUnavailable)
	if code := run([]string{"autosync", "run", job}, &terminalOut, &terminalErr, deps); code != 1 {
		t.Fatalf("failing scheduled run = %d, want 1", code)
	}
	logged, err := os.ReadFile(stdout)
	if err != nil || strings.Count(string(logged), "Synchronized 2 buckets") != 2 {
		t.Fatalf("output log = %q, %v; want both runs appended", logged, err)
	}
	problems, err := os.ReadFile(stderr)
	if err != nil || !strings.Contains(string(problems), "nothing was uploaded") || strings.Contains(string(problems), "/private/path") {
		t.Fatalf("error log = %q, %v", problems, err)
	}
	// Nothing reaches a console nobody is watching.
	if terminalOut.Len() != 0 || terminalErr.Len() != 0 {
		t.Fatalf("scheduled run wrote to the terminal: %q/%q", terminalOut.String(), terminalErr.String())
	}

	// A job file that sets anything outside the allowlist runs nothing.
	deps.sync = func(context.Context, string, string, string, bool, io.Writer) error {
		t.Fatal("an invalid job file reached synchronization")
		return nil
	}
	invalid := writeJob(t, autosync.ScheduledJob{Environment: map[string]string{"PATH": syntheticRoot}, Stdout: stdout, Stderr: stderr})
	for _, path := range []string{invalid, filepath.Join(directory, "missing.json"), "relative.json"} {
		if code := run([]string{"autosync", "run", path}, &terminalOut, &terminalErr, deps); code != 1 {
			t.Fatalf("scheduled run with %s = %d, want 1", filepath.Base(path), code)
		}
	}
}

func TestAutosyncExplainsAMissingScheduler(t *testing.T) {
	manager := &fakeAutosyncManager{err: fmt.Errorf("inspect /private/path: %w", autosync.ErrSchedulerUnavailable)}
	deps := dependencies{
		homeDir:    func() (string, error) { return syntheticHome, nil },
		executable: func() (string, error) { return syntheticBinary, nil },
		getenv: func(name string) string {
			if name == "TOKENHOLIC_API_URL" {
				return "https://api.example.test"
			}
			return ""
		},
		autosync: func(autosync.Config) (autosyncManager, error) { return manager, nil },
	}
	for _, test := range []struct {
		name      string
		command   string
		installed bool
		err       error
		code      int
		want      string
	}{
		{name: "enable", command: "enable", code: 1, want: "autosync needs a synthetic scheduler, which is not available here"},
		{name: "status with nothing installed", command: "status", code: 0, want: "Autosync is disabled. Enabling it needs a synthetic scheduler"},
		{name: "status with leftover files", command: "status", installed: true, code: 0, want: "Run `tokenholic autosync disable` to remove them"},
		{name: "unsupported system", command: "enable", err: autosync.ErrUnsupported, code: 1, want: "not supported on this operating system"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager.status = autosync.Status{Installed: test.installed}
			if test.err != nil {
				defer func(previous error) { manager.err = previous }(manager.err)
				manager.err = test.err
			}
			var stdout, stderr bytes.Buffer
			code := run([]string{"autosync", test.command}, &stdout, &stderr, deps)
			output := stdout.String() + stderr.String()
			if code != test.code || !strings.Contains(output, test.want) || strings.Contains(output, "/private/path") {
				t.Fatalf("autosync %s = %d/%q", test.command, code, output)
			}
		})
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

func TestProfileAndGroupLifecycleCommands(t *testing.T) {
	manager := &fakeGroupManager{
		groups:  []multiplayer.Group{{ID: "group-id", Name: "Quiet group", Role: "owner", Timezone: "UTC", Visibility: "public", PublicSlug: "g-publicab"}},
		profile: multiplayer.Profile{Login: "member", Visibility: "public", GlobalOptIn: true},
	}
	deps := dependencies{
		getenv: func(string) string { return "https://api.example.test" },
		groups: func(string) (groupManager, error) { return manager, nil },
	}
	tests := []struct {
		args     []string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{args: []string{"profile"}, wantOut: "@member\nProfile: public\nGlobal board: listed"},
		{args: []string{"profile", "public"}, wantOut: "Anyone can see your GitHub login, avatar, and usage totals"},
		{args: []string{"profile", "private"}, wantOut: "no longer appears publicly"},
		{args: []string{"profile", "unlisted"}, wantCode: 2, wantErr: "public or private"},
		{args: []string{"profile", "public", "now"}, wantCode: 2, wantErr: "public or private"},
		{args: []string{"group", "list"}, wantOut: "public board g-publicab"},
		{args: []string{"group", "public", "Quiet group"}, wantOut: "now has a public board: g-publicab"},
		{args: []string{"group", "private", "Quiet group"}, wantOut: "is private again"},
		{args: []string{"group", "public"}, wantCode: 2, wantErr: "requires one group"},
		{args: []string{"group", "transfer", "Quiet group", "@new-owner"}, wantOut: "Transferred Quiet group to new-owner"},
		{args: []string{"group", "transfer", "Quiet group"}, wantCode: 2, wantErr: "GitHub login"},
		// Deletion is permanent, so the name must be given twice.
		{args: []string{"group", "delete", "Quiet group"}, wantCode: 2, wantErr: "permanent"},
		{args: []string{"group", "delete", "--confirm", "Other group", "Quiet group"}, wantCode: 2, wantErr: "permanent"},
		{args: []string{"group", "delete", "--confirm", "Quiet group", "Quiet group"}, wantOut: "Deleted group Quiet group"},
	}
	for _, test := range tests {
		deletedBefore := manager.deleted
		var stdout, stderr bytes.Buffer
		code := run(test.args, &stdout, &stderr, deps)
		if code != test.wantCode || !strings.Contains(stdout.String(), test.wantOut) || !strings.Contains(stderr.String(), test.wantErr) {
			t.Fatalf("run(%v) = %d/%q/%q", test.args, code, stdout.String(), stderr.String())
		}
		if test.wantCode != 0 && manager.deleted != deletedBefore {
			t.Fatalf("run(%v) deleted %q without confirmation", test.args, manager.deleted)
		}
	}
	if manager.deleted != "Quiet group" || manager.transferred != "Quiet group->new-owner" || manager.visibility != "Quiet group=false" ||
		manager.profilePublic == nil || *manager.profilePublic {
		t.Fatalf("manager after the table = %#v", manager)
	}

	// A failure reports nothing as changed and leaks no detail.
	manager.err, manager.profilePublic = errors.New("credential at /private/path"), nil
	for _, args := range [][]string{{"profile"}, {"profile", "public"}, {"group", "public", "Quiet group"}, {"group", "delete", "--confirm", "Quiet group", "Quiet group"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr, deps); code != 1 || stdout.Len() != 0 || strings.Contains(stderr.String(), "/private/path") {
			t.Fatalf("failed run(%v) = %d/%q/%q", args, code, stdout.String(), stderr.String())
		}
	}
	if manager.profilePublic != nil {
		t.Fatal("a failed command changed profile visibility")
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
	profile       multiplayer.Profile
	profilePublic *bool
	visibility    string
	deleted       string
	transferred   string
	shareLink     multiplayer.ShareLink
	shareErr      error
}

func (f *fakeGroupManager) ShareLink(context.Context) (multiplayer.ShareLink, error) {
	return f.shareLink, f.shareErr
}

func (f *fakeGroupManager) Profile(context.Context) (multiplayer.Profile, error) {
	return f.profile, f.err
}
func (f *fakeGroupManager) SetProfileVisibility(_ context.Context, public bool) error {
	if f.err == nil {
		f.profilePublic = &public
	}
	return f.err
}
func (f *fakeGroupManager) SetGroupVisibility(_ context.Context, group string, public bool) (multiplayer.Group, error) {
	f.visibility = fmt.Sprintf("%s=%t", group, public)
	return multiplayer.Group{Name: group, PublicSlug: "g-publicab"}, f.err
}
func (f *fakeGroupManager) DeleteGroup(_ context.Context, group string) error {
	if f.err == nil {
		f.deleted = group
	}
	return f.err
}
func (f *fakeGroupManager) TransferGroup(_ context.Context, group, login string) error {
	f.transferred = group + "->" + login
	return f.err
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

func (f *fakeAutosyncManager) Requirement() string { return "a synthetic scheduler" }

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

func TestDemoOpensTheInterfaceWithoutTouchingTheMachine(t *testing.T) {
	t.Parallel()
	opened := 0
	deps := dependencies{
		// Reaching for the home directory, an account, or a sync would be the
		// demo reading or sending something real.
		homeDir: func() (string, error) { t.Fatal("demo read the home directory"); return "", nil },
		getenv:  func(string) string { return "" },
		account: func(string) (accountManager, error) { t.Fatal("demo opened an account"); return nil, nil },
		sync: func(context.Context, string, string, string, bool, io.Writer) error {
			t.Fatal("demo synchronized")
			return nil
		},
		launch: func(context.Context, string, string, string, io.Reader, io.Writer, displayOptions) error {
			t.Fatal("demo opened the real interface")
			return nil
		},
		demo: func(context.Context, io.Reader, io.Writer, displayOptions) error { opened++; return nil },
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"demo"}, &stdout, &stderr, deps); code != 0 || opened != 1 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "invented") {
		t.Fatalf("demo = %d/%q/%q opened=%d", code, stdout.String(), stderr.String(), opened)
	}
	stdout.Reset()
	if code := run([]string{"demo", "extra"}, &stdout, &stderr, deps); code != 2 || opened != 1 || !strings.Contains(stderr.String(), "no arguments") {
		t.Fatalf("demo extra = %d/%q/%q opened=%d", code, stdout.String(), stderr.String(), opened)
	}
	stderr.Reset()
	deps.demo = func(context.Context, io.Reader, io.Writer, displayOptions) error {
		return errors.New("synthetic: /private/path")
	}
	if code := run([]string{"demo"}, &stdout, &stderr, deps); code != 1 || strings.Contains(stderr.String(), "/private/path") {
		t.Fatalf("failed demo = %d/%q/%q", code, stdout.String(), stderr.String())
	}
}

// The run that cut a first upload off after five minutes is the bug this
// guards: the command must not put a deadline on the upload.
func TestSyncRunsWithoutADeadline(t *testing.T) {
	t.Parallel()
	deps := dependencies{
		homeDir: func() (string, error) { return syntheticHome, nil },
		getenv: func(name string) string {
			if name == "TOKENHOLIC_API_URL" {
				return "https://api.example.test"
			}
			return ""
		},
		sync: func(ctx context.Context, _, _, _ string, _ bool, _ io.Writer) error {
			if deadline, limited := ctx.Deadline(); limited {
				t.Fatalf("sync was given a deadline of %v", deadline)
			}
			return nil
		},
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"sync"}, &stdout, &stderr, deps); code != 0 || stderr.Len() != 0 {
		t.Fatalf("sync = %d/%q/%q", code, stdout.String(), stderr.String())
	}
}

func TestSyncSaysHowFarAnUnfinishedUploadGot(t *testing.T) {
	t.Parallel()
	deps := dependencies{
		homeDir: func() (string, error) { return syntheticHome, nil },
		getenv: func(name string) string {
			if name == "TOKENHOLIC_API_URL" {
				return "https://api.example.test"
			}
			return ""
		},
		sync: func(context.Context, string, string, string, bool, io.Writer) error {
			return &unfinishedUpload{uploaded: 1200, pending: 4214, cause: errors.New("synthetic: https://private.example.test/path refused")}
		},
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"sync"}, &stdout, &stderr, deps)
	message := stderr.String()
	if code != 1 || !strings.Contains(message, "1,200 of 4,214") || !strings.Contains(message, "`tokenholic sync`") {
		t.Fatalf("sync = %d/%q", code, message)
	}
	if strings.Contains(message, "private.example.test") || strings.Contains(message, "could not synchronize") {
		t.Fatalf("message leaks the cause or reads as a plain failure: %q", message)
	}
}

func TestUploadProgressIsQuietForOneBatchAndCountsForMany(t *testing.T) {
	t.Parallel()
	var output bytes.Buffer
	report := reportUpload(&output)
	report(usageSync.Progress{Batch: 0, Batches: 1, Pending: 40})
	report(usageSync.Progress{Batch: 1, Batches: 1, Acknowledged: 40, Pending: 40})
	if output.Len() != 0 {
		t.Fatalf("one batch printed %q", output.String())
	}
	report(usageSync.Progress{Batch: 0, Batches: 43, Pending: 4214})
	report(usageSync.Progress{Batch: 1, Batches: 43, Acknowledged: 100, Pending: 4214})
	report(usageSync.Progress{Batch: 12, Batches: 43, Acknowledged: 1200, Pending: 4214})
	want := "Uploading 4,214 buckets in 43 batches. A long history can take several minutes.\n" +
		"Stopping is safe: what is uploaded is kept, and `tokenholic sync` continues from there.\n" +
		"  100 of 4,214 uploaded\n" +
		"  1,200 of 4,214 uploaded\n"
	if output.String() != want {
		t.Fatalf("progress =\n%s\nwant\n%s", output.String(), want)
	}
}
