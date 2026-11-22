package adapters

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ansonnchan/tokenholic/internal/normalize"
)

// AgentRoot is where one day-level agent keeps its session records. These
// agents have no native reader: tokenholic only looks whether a folder is
// there, to tell an agent that is not installed from one whose usage cannot
// be read, and leaves the reading to the pinned ccusage.
//
// The folders are the ones the pinned ccusage release reads for each agent,
// so "found here" and "ccusage will read it" mean the same thing. The README
// cites them.
type AgentRoot struct {
	Source normalize.Source
	// Paths are the folders that hold the agent's records. The agent is
	// present when any of them is a directory.
	Paths []string
}

// DayLevelRoots returns the record folders of every day-level agent. Each
// agent uses the same layout under the home directory on every system, so
// there is one list rather than one per platform.
//
// Three locations follow a variable the agent itself documents:
// COPILOT_HOME, GROK_HOME, and XDG_DATA_HOME for OpenCode. Anything but an
// absolute path selects the default.
func DayLevelRoots(env Environment) []AgentRoot {
	under := func(variable string, fallback ...string) string {
		if configured := env.lookup(variable); filepath.IsAbs(configured) {
			return configured
		}
		return filepath.Join(append([]string{env.Home}, fallback...)...)
	}
	copilot := under("COPILOT_HOME", ".copilot")
	return []AgentRoot{
		{Source: normalize.SourceCopilot, Paths: []string{
			filepath.Join(copilot, "session-state"), filepath.Join(copilot, "otel"),
		}},
		{Source: normalize.SourceGemini, Paths: []string{filepath.Join(env.Home, ".gemini", "tmp")}},
		{Source: normalize.SourceGrok, Paths: []string{filepath.Join(under("GROK_HOME", ".grok"), "sessions")}},
		{Source: normalize.SourceOpenCode, Paths: []string{filepath.Join(under("XDG_DATA_HOME", ".local", "share"), "opencode")}},
		{Source: normalize.SourceQwen, Paths: []string{filepath.Join(env.Home, ".qwen", "projects")}},
	}
}

// Status reports whether the agent's records are here: detected when any
// folder exists, not found when none does, and permission denied when a
// folder cannot be examined. Nothing inside a folder is opened or listed.
func (r AgentRoot) Status() normalize.HealthStatus {
	status := normalize.HealthNotFound
	for _, path := range r.Paths {
		info, err := os.Stat(path)
		switch {
		case err == nil && info.IsDir():
			return normalize.HealthDetected
		case err == nil, errors.Is(err, fs.ErrNotExist):
		case errors.Is(err, fs.ErrPermission):
			status = normalize.HealthPermissionDenied
		default:
			status = normalize.HealthError
		}
	}
	return status
}
