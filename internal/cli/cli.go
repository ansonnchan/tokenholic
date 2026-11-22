package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ansonnchan/tokenholic/internal/account"
	"github.com/ansonnchan/tokenholic/internal/adapters"
	"github.com/ansonnchan/tokenholic/internal/application"
	"github.com/ansonnchan/tokenholic/internal/autosync"
	"github.com/ansonnchan/tokenholic/internal/doctor"
	"github.com/ansonnchan/tokenholic/internal/multiplayer"
	"github.com/ansonnchan/tokenholic/internal/preferences"
	"github.com/ansonnchan/tokenholic/internal/store"
	usageSync "github.com/ansonnchan/tokenholic/internal/sync"
)

// version is replaced at link time for release artifacts.
var version = "dev"

// defaultAPIURL is the hosted API a release build talks to. cmd/release sets
// it at link time. A development build has none, so it can never reach the
// hosted service by accident and must be pointed at an API explicitly.
var defaultAPIURL = ""

// noAPIAddress is shown only by a development build: a release build always
// has an address.
const noAPIAddress = "this development build has no API address; set TOKENHOLIC_API_URL to one"

// Run executes the native command without owning process termination.
func Run(args []string, stdout, stderr io.Writer) int {
	return run(args, stdout, stderr, defaultDependencies())
}

type dependencies struct {
	homeDir func() (string, error)
	getenv  func(string) string
	// defaultAPIURL is the address used when TOKENHOLIC_API_URL is unset.
	defaultAPIURL string
	diagnose      func(context.Context, string, string, doctor.ParserProbe) doctor.Report
	input         io.Reader
	launch        func(context.Context, string, string, string, io.Reader, io.Writer, displayOptions) error
	timeout       time.Duration
	account       func(string) (accountManager, error)
	groups        func(string) (groupManager, error)
	sync          func(context.Context, string, string, string, bool, io.Writer) error
	executable    func() (string, error)
	autosync      func(autosync.Config) (autosyncManager, error)
	// usage scans local records and returns the dashboard's totals.
	usage func(ctx context.Context, home, codexHome string) (application.Dashboard, error)
	// preferences is where the user's saved choices live.
	preferences func() (string, error)
	// demo opens the interface on invented data.
	demo func(context.Context, io.Reader, io.Writer, displayOptions) error
}

type accountManager interface {
	Login(context.Context, string, io.Writer) error
	Logout(context.Context) error
	DeleteAccount(context.Context) error
}

type groupManager interface {
	List(context.Context) ([]multiplayer.Group, error)
	Create(context.Context, string) (multiplayer.Group, error)
	Join(context.Context, string) (multiplayer.Group, error)
	Invite(context.Context, string, time.Duration, int) (multiplayer.Invite, error)
	Leave(context.Context, string) error
	SetGlobalParticipation(context.Context, bool) error
	Profile(context.Context) (multiplayer.Profile, error)
	ShareLink(context.Context) (multiplayer.ShareLink, error)
	SetProfileVisibility(context.Context, bool) error
	SetGroupVisibility(context.Context, string, bool) (multiplayer.Group, error)
	DeleteGroup(context.Context, string) error
	TransferGroup(context.Context, string, string) error
}

type autosyncManager interface {
	Enable(context.Context) error
	Disable(context.Context) error
	Status(context.Context) (autosync.Status, error)
	// Requirement names what the operating system must provide.
	Requirement() string
}

// apiOrigin returns the API address in effect and whether the user chose it.
// TOKENHOLIC_API_URL is an override for self-hosting and development; nobody
// has to set it to use a release build.
func (d dependencies) apiOrigin() (origin string, overridden bool) {
	if configured := strings.TrimSpace(d.getenv("TOKENHOLIC_API_URL")); configured != "" {
		return configured, true
	}
	return strings.TrimSpace(d.defaultAPIURL), false
}

func defaultDependencies() dependencies {
	return dependencies{
		homeDir:       os.UserHomeDir,
		getenv:        os.Getenv,
		defaultAPIURL: defaultAPIURL,
		input:         os.Stdin,
		executable:    os.Executable,
		launch:        launchLocalTUI,
		timeout:       5 * time.Second,
		account: func(apiURL string) (accountManager, error) {
			client, err := account.NewClient(apiURL, nil)
			if err != nil {
				return nil, err
			}
			return account.NewManager(client, account.NewKeyringStore(apiURL))
		},
		groups: func(apiURL string) (groupManager, error) {
			client, err := multiplayer.NewClient(apiURL, nil)
			if err != nil {
				return nil, err
			}
			return multiplayer.NewService(client, account.NewKeyringStore(apiURL), localTimezone())
		},
		sync:        synchronizeLocal,
		usage:       loadLocalUsage,
		preferences: preferences.DefaultPath,
		demo:        launchDemo,
		autosync: func(config autosync.Config) (autosyncManager, error) {
			return autosync.NewManager(config)
		},
		diagnose: func(ctx context.Context, home, codexHome string, parser doctor.ParserProbe) doctor.Report {
			discovery := adapters.Discoverer{Roots: defaultRoots(home, codexHome)}
			path, err := store.DefaultPath()
			if err != nil {
				report := doctor.Diagnose(ctx, discovery, parser, nil)
				report.Database = doctor.DatabaseStatus{Location: "default application data", State: doctor.DatabaseError, SupportedVersion: store.SupportedSchemaVersion()}
				return report
			}
			return doctor.Diagnose(ctx, discovery, parser, doctor.LocalDatabaseProbe{Path: path, Default: true})
		},
	}
}

func run(args []string, stdout, stderr io.Writer, deps dependencies) int {
	if len(args) > 0 && args[0] == "sync" {
		return runSyncCommand(args[1:], stdout, stderr, deps)
	}
	if len(args) > 0 && args[0] == "show-data" {
		if len(args) != 1 {
			fmt.Fprintln(stderr, "show-data accepts no arguments")
			return 2
		}
		return runSynchronization(true, stdout, stderr, deps)
	}
	if len(args) > 0 && args[0] == "group" {
		return runGroupCommand(args[1:], stdout, stderr, deps)
	}
	if len(args) > 0 && args[0] == "global" {
		return runGlobalCommand(args[1:], stdout, stderr, deps)
	}
	if len(args) > 0 && args[0] == "profile" {
		return runProfileCommand(args[1:], stdout, stderr, deps)
	}
	if len(args) > 0 && args[0] == "account" {
		return runAccountCommand(args[1:], stdout, stderr, deps)
	}
	if len(args) > 0 && args[0] == "autosync" {
		return runAutosyncCommand(args[1:], stdout, stderr, deps)
	}
	if len(args) > 0 && args[0] == "share" {
		return runShareCommand(args[1:], stdout, stderr, deps)
	}
	if len(args) > 0 && args[0] == "ccusage" {
		return runCCUsageCommand(args[1:], stdout, stderr, deps)
	}
	if len(args) > 0 && args[0] == "demo" {
		return runDemoCommand(args[1:], stdout, stderr, deps)
	}
	flags := flag.NewFlagSet("tokenholic", flag.ContinueOnError)
	flags.SetOutput(stderr)
	showVersion := flags.Bool("version", false, "print the version")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	if *showVersion {
		fmt.Fprintln(stdout, version)
		return 0
	}
	if flags.NArg() == 1 && flags.Arg(0) == "doctor" {
		return runDoctor(stdout, stderr, deps)
	}
	if flags.NArg() == 1 && flags.Arg(0) == "login" {
		return runLogin(stdout, stderr, deps)
	}
	if flags.NArg() == 1 && flags.Arg(0) == "claim" {
		return runClaim(stdout, stderr, deps)
	}
	if flags.NArg() == 1 && flags.Arg(0) == "logout" {
		return runLogout(stdout, stderr, deps)
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "unknown command %q\n", flags.Arg(0))
		return 2
	}

	return runTUI(stdout, stderr, deps)
}

func runSyncCommand(args []string, stdout, stderr io.Writer, deps dependencies) int {
	flags := flag.NewFlagSet("tokenholic sync", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dryRun := flags.Bool("dry-run", false, "show the exact upload requests without sending them")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "sync accepts only --dry-run")
		return 2
	}
	return runSynchronization(*dryRun, stdout, stderr, deps)
}

func runSynchronization(dryRun bool, stdout, stderr io.Writer, deps dependencies) int {
	home, err := deps.homeDir()
	if err != nil {
		fmt.Fprintln(stderr, "could not resolve the local home directory")
		return 1
	}
	codexHome := deps.getenv("CODEX_HOME")
	if codexHome != "" && !filepath.IsAbs(codexHome) {
		fmt.Fprintln(stderr, "CODEX_HOME must be an absolute directory")
		return 1
	}
	apiURL, _ := deps.apiOrigin()
	if apiURL == "" && !dryRun {
		fmt.Fprintln(stderr, noAPIAddress)
		return 1
	}
	if deps.sync == nil {
		fmt.Fprintln(stderr, "synchronization support is unavailable")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := deps.sync(ctx, home, codexHome, apiURL, dryRun, stdout); err != nil {
		if errors.Is(err, autosync.ErrAlreadyRunning) {
			fmt.Fprintln(stderr, "synchronization is already running")
			return 1
		}
		if errors.Is(err, usageSync.ErrUpgradeRequired) {
			// Stable text with no payload detail, so it is safe to show as is.
			fmt.Fprintln(stderr, "this tokenholic version is too old to sync; upgrade with `npx @tokenholic/cli@latest`")
			fmt.Fprintln(stderr, "local usage was preserved and will upload after the upgrade")
			return 1
		}
		if dryRun {
			fmt.Fprintln(stderr, "could not show usage data; local pending data was preserved")
		} else if reportVaultProblem(stderr, err) {
			fmt.Fprintln(stderr, "nothing was uploaded; local pending data was preserved")
		} else {
			fmt.Fprintln(stderr, "could not synchronize usage; local pending data was preserved")
		}
		return 1
	}
	return 0
}

func runClaim(stdout, stderr io.Writer, deps dependencies) int {
	if code := runLogin(stdout, stderr, deps); code != 0 {
		return code
	}
	fmt.Fprintln(stdout)
	if code := runSynchronization(false, stdout, stderr, deps); code != 0 {
		fmt.Fprintln(stderr, "Signed in, but the initial sync did not complete; run `tokenholic sync` to retry.")
		return code
	}
	return 0
}

func runLogin(stdout, stderr io.Writer, deps dependencies) int {
	manager, ok := configuredAccountManager(stderr, deps)
	if !ok {
		return 1
	}
	if err := manager.Login(context.Background(), version, stdout); err != nil {
		if !reportVaultProblem(stderr, err) {
			fmt.Fprintln(stderr, "could not sign in; check the API configuration and try again")
		}
		return 1
	}
	return 0
}

func runLogout(stdout, stderr io.Writer, deps dependencies) int {
	manager, ok := configuredAccountManager(stderr, deps)
	if !ok {
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := manager.Logout(ctx); err != nil {
		reportVaultProblem(stderr, err)
		fmt.Fprintln(stderr, "could not sign out; the device remains authorized")
		return 1
	}
	fmt.Fprintln(stdout, "Signed out and revoked this device.")
	return 0
}

// reportVaultProblem explains a credential store that cannot be used. The
// text is fixed per operating system and names no path or secret. It reports
// whether it wrote anything.
func reportVaultProblem(stderr io.Writer, err error) bool {
	if !errors.Is(err, account.ErrVaultUnavailable) {
		return false
	}
	fmt.Fprintln(stderr, account.VaultHelp())
	return true
}

// invalidAPIAddress names whose address was refused: the user's override, or
// the one the build carries, which only a broken release could get wrong.
func invalidAPIAddress(overridden bool) string {
	if overridden {
		return "TOKENHOLIC_API_URL is invalid"
	}
	return "this build's API address is invalid; reinstall tokenholic"
}

func configuredAccountManager(stderr io.Writer, deps dependencies) (accountManager, bool) {
	apiURL, overridden := deps.apiOrigin()
	if apiURL == "" {
		fmt.Fprintln(stderr, noAPIAddress)
		return nil, false
	}
	if deps.account == nil {
		fmt.Fprintln(stderr, "account support is unavailable")
		return nil, false
	}
	manager, err := deps.account(apiURL)
	if err != nil {
		fmt.Fprintln(stderr, invalidAPIAddress(overridden))
		return nil, false
	}
	return manager, true
}

func runTUI(stdout, stderr io.Writer, deps dependencies) int {
	home, err := deps.homeDir()
	if err != nil {
		fmt.Fprintln(stderr, "could not resolve the local home directory")
		return 1
	}
	codexHome := deps.getenv("CODEX_HOME")
	if codexHome != "" && !filepath.IsAbs(codexHome) {
		fmt.Fprintln(stderr, "CODEX_HOME must be an absolute directory")
		return 1
	}
	if deps.launch == nil {
		fmt.Fprintln(stderr, "could not start the local application")
		return 1
	}
	apiURL, _ := deps.apiOrigin()
	if err := deps.launch(context.Background(), home, codexHome, apiURL, deps.input, stdout, displayFromEnvironment(deps.getenv)); err != nil {
		// Runtime errors can wrap private local paths. Keep CLI output stable and
		// direct detailed investigation through the redacted doctor report.
		fmt.Fprintln(stderr, "could not start the local application; run `tokenholic doctor`")
		return 1
	}
	return 0
}

func runDoctor(stdout, stderr io.Writer, deps dependencies) int {
	home, err := deps.homeDir()
	if err != nil {
		fmt.Fprintln(stderr, "could not resolve the local home directory")
		return 1
	}
	codexHome := deps.getenv("CODEX_HOME")
	if codexHome != "" && !filepath.IsAbs(codexHome) {
		fmt.Fprintln(stderr, "CODEX_HOME must be an absolute directory")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), deps.timeout)
	defer cancel()
	reader := dayLevelReader(home, deps.getenv, deps.preferences)
	report := deps.diagnose(ctx, home, codexHome, doctor.CCUsageProbe{Roots: reader.Roots, Runner: reader.Runner, Off: reader.Off})
	switch apiURL, overridden := deps.apiOrigin(); {
	case overridden:
		report.API = doctor.APIOverridden
	case apiURL != "":
		report.API = doctor.APIBuiltIn
	default:
		report.API = doctor.APINone
	}
	fmt.Fprint(stdout, report.Render())
	return report.ExitCode()
}
