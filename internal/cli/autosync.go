package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ansonnchan/tokenholic/internal/autosync"
)

// scheduleSettings are the local values an enabled schedule is written
// from. `tokenholic autosync enable` and a claim's offer both use them, so
// the two install the same thing.
type scheduleSettings struct {
	home, executable, apiURL, codexHome, ccusagePath string
	// pinAPIURL is whether the user chose the address themselves.
	pinAPIURL bool
	getenv    func(string) string
}

func (s scheduleSettings) config() autosync.Config {
	return autosync.Config{
		Home: s.home, Executable: s.executable, APIURL: s.apiURL, PinAPIURL: s.pinAPIURL,
		CodexHome: s.codexHome, CCUsagePath: s.ccusagePath, Getenv: s.getenv,
	}
}

// scheduleSettingsFrom reads what a schedule needs from this run. It
// reports false when this run cannot describe one: no executable to copy,
// no API address, or a CODEX_HOME that is not absolute.
func scheduleSettingsFrom(home, apiURL string, overridden bool, getenv func(string) string, executable func() (string, error)) (scheduleSettings, bool) {
	if executable == nil || apiURL == "" {
		return scheduleSettings{}, false
	}
	path, err := executable()
	if err != nil {
		return scheduleSettings{}, false
	}
	if path, err = filepath.Abs(path); err != nil {
		return scheduleSettings{}, false
	}
	codexHome := strings.TrimSpace(getenv("CODEX_HOME"))
	if codexHome != "" && !filepath.IsAbs(codexHome) {
		return scheduleSettings{}, false
	}
	// The scheduled copy runs outside the npm install, so it is told where
	// the pinned ccusage was installed. A location that is not absolute is
	// dropped rather than refused: it only means no ccusage for the schedule.
	ccusagePath := strings.TrimSpace(getenv(ccusagePathVariable))
	if !filepath.IsAbs(ccusagePath) {
		ccusagePath = ""
	}
	return scheduleSettings{
		home: home, executable: path, apiURL: apiURL, pinAPIURL: overridden,
		codexHome: codexHome, ccusagePath: ccusagePath, getenv: getenv,
	}, true
}

// hourlySync is the offer a claim makes: whether hourly sync can be turned
// on here, and turning it on.
type hourlySync struct {
	manager autosyncManager
}

// newHourlySync returns nil when this run cannot offer a schedule at all.
func newHourlySync(settings scheduleSettings, build func(autosync.Config) (autosyncManager, error)) *hourlySync {
	if build == nil {
		return nil
	}
	manager, err := build(settings.config())
	if err != nil {
		return nil
	}
	return &hourlySync{manager: manager}
}

// Available reports whether to offer hourly sync: the scheduler answers and
// has nothing of ours installed or loaded. Anything else, including a
// half-installed schedule, is left to `tokenholic autosync`.
func (h *hourlySync) Available(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, schedulerTimeout)
	defer cancel()
	status, err := h.manager.Status(ctx)
	return err == nil && !status.Installed && !status.Loaded
}

// Enable installs the schedule and starts it.
func (h *hourlySync) Enable(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, schedulerTimeout)
	defer cancel()
	return h.manager.Enable(ctx)
}

// offerHourlySync asks, once a terminal claim has uploaded, whether to keep
// the account current in the background. Enter means yes. It never fails
// the claim: the sign-in and the upload are already done.
func offerHourlySync(stdout, stderr io.Writer, deps dependencies) {
	if deps.homeDir == nil {
		return
	}
	home, err := deps.homeDir()
	if err != nil {
		return
	}
	apiURL, overridden := deps.apiOrigin()
	settings, ok := scheduleSettingsFrom(home, apiURL, overridden, deps.getenv, deps.executable)
	if !ok {
		return
	}
	offer := newHourlySync(settings, deps.autosync)
	if offer == nil || !offer.Available(context.Background()) {
		return
	}
	fmt.Fprintln(stdout)
	if deps.terminal == nil || !deps.terminal() || deps.input == nil {
		// Nobody is there to answer, so nothing is installed.
		fmt.Fprintln(stdout, "To keep your boards current in the background, run `tokenholic autosync enable`.")
		return
	}
	fmt.Fprintln(stdout, "Keep your boards current? Hourly sync uploads new usage counts in the background,")
	fmt.Fprintln(stdout, "at login and every hour. It installs a scheduled job for your user and a copy of")
	fmt.Fprintln(stdout, "tokenholic to run it; `tokenholic autosync disable` removes both.")
	fmt.Fprint(stdout, "Turn on hourly sync? [Y/n] ")
	answer, err := bufio.NewReader(deps.input).ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	// Input that ended without a line is not an answer.
	if err != nil && answer == "" {
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "Not turned on. `tokenholic autosync enable` turns it on later.")
		return
	}
	if answer != "" && answer != "y" && answer != "yes" {
		fmt.Fprintln(stdout, "Not turned on. `tokenholic autosync enable` turns it on later.")
		return
	}
	if err := offer.Enable(context.Background()); err != nil {
		if !reportSchedulerProblem(stderr, offer.manager, err) {
			fmt.Fprintln(stderr, "could not enable autosync; no schedule was activated")
		}
		fmt.Fprintln(stderr, "your claim and upload are done; `tokenholic autosync enable` tries again")
		return
	}
	fmt.Fprintln(stdout, enabledText)
}

// schedulerTimeout bounds one conversation with the system's scheduler.
const schedulerTimeout = 15 * time.Second

// enabledText is what a successful enable says, wherever it was asked for.
const enabledText = "Enabled autosync at login and hourly. Re-enable it after upgrading tokenholic."

func runAutosyncCommand(args []string, stdout, stderr io.Writer, deps dependencies) int {
	// `run` is what a schedule invokes; it is not offered to users.
	if len(args) == 2 && args[0] == "run" {
		return runScheduledSync(args[1], deps)
	}
	if len(args) != 1 || (args[0] != "enable" && args[0] != "disable" && args[0] != "status") {
		fmt.Fprintln(stderr, "autosync requires enable, disable, or status")
		return 2
	}
	if deps.homeDir == nil || deps.executable == nil || deps.autosync == nil {
		fmt.Fprintln(stderr, "autosync support is unavailable")
		return 1
	}
	home, err := deps.homeDir()
	if err != nil {
		fmt.Fprintln(stderr, "could not resolve the local home directory")
		return 1
	}
	executable, err := deps.executable()
	if err != nil {
		fmt.Fprintln(stderr, "could not resolve the current tokenholic executable")
		return 1
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		fmt.Fprintln(stderr, "could not resolve the current tokenholic executable")
		return 1
	}
	apiURL, overridden := deps.apiOrigin()
	codexHome := strings.TrimSpace(deps.getenv("CODEX_HOME"))
	// The scheduled copy runs outside the npm install, so it is told where
	// the pinned ccusage was installed. A location that is not absolute is
	// dropped rather than refused: it only means no ccusage for the schedule.
	ccusagePath := strings.TrimSpace(deps.getenv(ccusagePathVariable))
	if !filepath.IsAbs(ccusagePath) {
		ccusagePath = ""
	}
	if args[0] == "enable" {
		if apiURL == "" {
			fmt.Fprintln(stderr, noAPIAddress)
			return 1
		}
		if codexHome != "" && !filepath.IsAbs(codexHome) {
			fmt.Fprintln(stderr, "CODEX_HOME must be an absolute directory")
			return 1
		}
	} else {
		apiURL = ""
		codexHome = ""
		ccusagePath = ""
	}
	manager, err := deps.autosync(scheduleSettings{
		home: home, executable: executable, apiURL: apiURL, pinAPIURL: overridden,
		codexHome: codexHome, ccusagePath: ccusagePath, getenv: deps.getenv,
	}.config())
	if err != nil {
		fmt.Fprintln(stderr, "autosync configuration is invalid")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), schedulerTimeout)
	defer cancel()
	switch args[0] {
	case "enable":
		if err := manager.Enable(ctx); err != nil {
			if !reportSchedulerProblem(stderr, manager, err) {
				fmt.Fprintln(stderr, "could not enable autosync; no schedule was activated")
			}
			return 1
		}
		fmt.Fprintln(stdout, enabledText)
	case "disable":
		if err := manager.Disable(ctx); err != nil {
			if !reportSchedulerProblem(stderr, manager, err) {
				fmt.Fprintln(stderr, "could not disable autosync; inspect its status and retry")
			}
			return 1
		}
		fmt.Fprintln(stdout, "Disabled autosync and removed its installed binary and logs. Local usage was not changed.")
	case "status":
		status, err := manager.Status(ctx)
		if errors.Is(err, autosync.ErrSchedulerUnavailable) && status.Installed {
			// Files from an earlier install can outlive the scheduler, for
			// example after a move between machines or init systems.
			fmt.Fprintf(stdout, "Autosync files are installed, but %s is not available, so nothing runs them. Run `tokenholic autosync disable` to remove them.\n", manager.Requirement())
			return 0
		}
		if errors.Is(err, autosync.ErrSchedulerUnavailable) {
			fmt.Fprintf(stdout, "Autosync is disabled. Enabling it needs %s, which is not available here.\n", manager.Requirement())
			return 0
		}
		if err != nil {
			if !reportSchedulerProblem(stderr, manager, err) {
				fmt.Fprintln(stderr, "could not inspect autosync status")
			}
			return 1
		}
		switch {
		case status.Installed && status.Loaded:
			fmt.Fprintln(stdout, "Autosync is installed and loaded; it runs at login and hourly.")
		case status.Installed:
			fmt.Fprintln(stdout, "Autosync is installed but not loaded; run `tokenholic autosync enable` to repair it.")
		case status.Loaded:
			fmt.Fprintln(stdout, "Autosync is loaded with incomplete files; enable it to repair or disable it to remove the schedule.")
		default:
			fmt.Fprintln(stdout, "Autosync is disabled.")
		}
	}
	return 0
}

// reportSchedulerProblem explains the two failures a user can act on: an
// operating system tokenholic cannot schedule on, and a scheduler that is not
// running. Both messages are fixed text plus the scheduler's name, so they
// are safe to show. It reports whether it wrote anything.
func reportSchedulerProblem(stderr io.Writer, manager autosyncManager, err error) bool {
	switch {
	case errors.Is(err, autosync.ErrUnsupported):
		fmt.Fprintln(stderr, "autosync is not supported on this operating system; run `tokenholic sync` yourself or from your own scheduler")
	case errors.Is(err, autosync.ErrSchedulerUnavailable):
		fmt.Fprintf(stderr, "autosync needs %s, which is not available here; nothing was scheduled\n", manager.Requirement())
		fmt.Fprintln(stderr, "run `tokenholic sync` yourself or from your own scheduler instead")
	default:
		return false
	}
	return true
}

// runScheduledSync is one scheduled run on a system whose scheduler starts a
// bare program: it takes the job's environment and log files from the job
// file, then synchronizes exactly as `tokenholic sync` does. The job file's
// variables are an allowlist and take precedence over the process's own.
func runScheduledSync(jobPath string, deps dependencies) int {
	// A scheduled run has nobody watching a console.
	detachConsole()
	job, err := autosync.LoadScheduledJob(jobPath)
	if err != nil {
		return 1
	}
	open := func(path string) (*os.File, error) {
		return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	}
	stdout, err := open(job.Stdout)
	if err != nil {
		return 1
	}
	defer stdout.Close()
	stderr, err := open(job.Stderr)
	if err != nil {
		return 1
	}
	defer stderr.Close()

	inherited := deps.getenv
	deps.getenv = func(name string) string {
		if value, ok := job.Environment[name]; ok {
			return value
		}
		return inherited(name)
	}
	return runSynchronization(false, stdout, stderr, deps)
}
