package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/ansonnchan/tokenholic/internal/autosync"
)

func runAutosyncCommand(args []string, stdout, stderr io.Writer, deps dependencies) int {
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
	apiURL := strings.TrimSpace(deps.getenv("TOKENHOLIC_API_URL"))
	codexHome := strings.TrimSpace(deps.getenv("CODEX_HOME"))
	if args[0] == "enable" {
		if apiURL == "" {
			fmt.Fprintln(stderr, "TOKENHOLIC_API_URL is required to enable autosync")
			return 1
		}
		if codexHome != "" && !filepath.IsAbs(codexHome) {
			fmt.Fprintln(stderr, "CODEX_HOME must be an absolute directory")
			return 1
		}
	} else {
		apiURL = ""
		codexHome = ""
	}
	manager, err := deps.autosync(autosync.Config{
		Home: home, Executable: executable, APIURL: apiURL, CodexHome: codexHome, Getenv: deps.getenv,
	})
	if err != nil {
		fmt.Fprintln(stderr, "autosync configuration is invalid")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	switch args[0] {
	case "enable":
		if err := manager.Enable(ctx); err != nil {
			if !reportSchedulerProblem(stderr, manager, err) {
				fmt.Fprintln(stderr, "could not enable autosync; no schedule was activated")
			}
			return 1
		}
		fmt.Fprintln(stdout, "Enabled autosync at login and hourly. Re-enable it after upgrading tokenholic.")
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
