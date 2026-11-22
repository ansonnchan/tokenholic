package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/ansonnchan/tokenholic/internal/adapters"
	"github.com/ansonnchan/tokenholic/internal/adapters/ccusage"
	"github.com/ansonnchan/tokenholic/internal/autosync"
	"github.com/ansonnchan/tokenholic/internal/collect"
	"github.com/ansonnchan/tokenholic/internal/preferences"
)

// ccusagePathVariable names the pinned ccusage executable. The npm launcher
// sets it from the package it installed; nobody has to set it by hand. A
// path given here is still held to the pinned digest before anything runs,
// so it cannot be used to make tokenholic run another program.
const ccusagePathVariable = autosync.CCUsagePathVariable

// ccusageRunner returns the pinned runner for this process and whether the
// user turned ccusage off. Preferences that cannot be read count as off:
// when the saved choice is unknown, third-party code does not run.
func (d dependencies) ccusageRunner() (ccusage.Runner, bool) {
	return ccusageRunnerFrom(d.getenv, d.preferences)
}

func ccusageRunnerFrom(getenv func(string) string, preferencesPath func() (string, error)) (ccusage.Runner, bool) {
	runner := ccusage.Runner{Path: strings.TrimSpace(getenv(ccusagePathVariable)), Getenv: getenv}
	if preferencesPath == nil {
		return runner, false
	}
	path, err := preferencesPath()
	if err != nil {
		return runner, true
	}
	saved, err := preferences.Load(path)
	if err != nil {
		return runner, true
	}
	return runner, saved.CCUsageOff()
}

// dayLevelReader is the reader for agents without a native adapter: where
// each keeps its records, the pinned runner, and the user's saved choice.
// Collection and doctor are both given this one, so they cannot disagree.
func dayLevelReader(home string, getenv func(string) string, preferencesPath func() (string, error)) collect.DayLevel {
	runner, off := ccusageRunnerFrom(getenv, preferencesPath)
	return collect.DayLevel{
		Roots:  adapters.DayLevelRoots(adapters.Environment{Home: home, Getenv: getenv}),
		Runner: runner, Off: off,
	}
}

func ccusageAgents() string {
	names := make([]string, 0, len(ccusage.Agents()))
	for _, agent := range ccusage.Agents() {
		names = append(names, agent.Label())
	}
	return strings.Join(names, ", ")
}

// runCCUsageCommand shows or changes whether tokenholic runs the pinned
// ccusage. It is on unless the user turns it off; the choice is saved beside
// the usage database and applies to every later run, scheduled ones included.
func runCCUsageCommand(args []string, stdout, stderr io.Writer, deps dependencies) int {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off" && args[0] != "status") {
		fmt.Fprintln(stderr, "ccusage requires on, off, or status")
		return 2
	}
	if deps.preferences == nil {
		fmt.Fprintln(stderr, "could not locate the saved preferences")
		return 1
	}
	path, err := deps.preferences()
	if err != nil {
		fmt.Fprintln(stderr, "could not locate the saved preferences")
		return 1
	}
	if args[0] == "status" {
		runner, off := deps.ccusageRunner()
		if off {
			fmt.Fprintf(stdout, "ccusage is off. tokenholic does not run it, so these agents are not counted: %s.\n", ccusageAgents())
			fmt.Fprintln(stdout, "Run `tokenholic ccusage on` to count them.")
			return 0
		}
		fmt.Fprintf(stdout, "ccusage is on. tokenholic runs the pinned ccusage %s to count: %s.\n", ccusage.PinnedVersion, ccusageAgents())
		fmt.Fprintln(stdout, "It reads those agents' local session records on this machine and returns token counts per day and model; nothing else leaves it.")
		switch err := runner.Verify(); {
		case err == nil:
			fmt.Fprintln(stdout, "This install has the pinned executable.")
		default:
			fmt.Fprintln(stdout, "This install cannot run it; `tokenholic doctor` says why. Those agents are not counted until it can.")
		}
		fmt.Fprintln(stdout, "Run `tokenholic ccusage off` to stop tokenholic from running it.")
		return 0
	}
	// A file that cannot be read is replaced rather than merged: the user is
	// stating a choice now, and an unreadable file already counted as off.
	saved, _ := preferences.Load(path)
	saved.SetCCUsage(args[0] == "on")
	if err := preferences.Save(path, saved); err != nil {
		fmt.Fprintln(stderr, "could not save the choice; nothing was changed")
		return 1
	}
	if args[0] == "on" {
		fmt.Fprintf(stdout, "Turned ccusage on. tokenholic runs the pinned ccusage %s to count: %s.\n", ccusage.PinnedVersion, ccusageAgents())
		return 0
	}
	fmt.Fprintf(stdout, "Turned ccusage off. tokenholic no longer runs it, so these agents are not counted: %s.\n", ccusageAgents())
	return 0
}
