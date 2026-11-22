package doctor

import (
	"context"
	"errors"

	"github.com/ansonnchan/tokenholic/internal/adapters"
	"github.com/ansonnchan/tokenholic/internal/adapters/ccusage"
	"github.com/ansonnchan/tokenholic/internal/collect"
	"github.com/ansonnchan/tokenholic/internal/normalize"
)

// RunnerState says whether the pinned ccusage can be run at all.
type RunnerState string

const (
	// RunnerReady means the executable is the pinned release.
	RunnerReady RunnerState = "ready"
	// RunnerOff means the user turned ccusage off. Nothing was read or run.
	RunnerOff RunnerState = "off"
	// RunnerUnavailable means no ccusage executable came with this install.
	RunnerUnavailable RunnerState = "unavailable"
	// RunnerUnpinned means an executable exists but is not the pinned
	// release, so it was not run.
	RunnerUnpinned RunnerState = "unpinned"
)

// AgentReport is one day-level agent's health as collection would record it:
// whether its records are here and, if ccusage read them, how many days it
// reported. It holds a count and a reason code, never report content.
type AgentReport struct {
	Source       normalize.Source
	Status       normalize.HealthStatus
	Reason       string
	Days         uint64
	Capabilities normalize.Capabilities
}

// ParserStatus is the health of the pinned ccusage runner and of each agent
// read through it. It retains no ccusage output.
type ParserStatus struct {
	State  RunnerState
	Agents []AgentReport
}

// CCUsageProbe checks the pinned runner and reads each agent exactly as a
// scan does, through the same reader, so the diagnosis and the collection
// cannot disagree. It stores nothing.
type CCUsageProbe struct {
	Roots  []adapters.AgentRoot
	Runner collect.DayRunner
	// Off is the user's choice not to run ccusage.
	Off bool
}

// Check verifies the executable, then reads each agent that is present. An
// agent whose records are not here is reported as not found without running
// anything for it.
func (p CCUsageProbe) Check(ctx context.Context) ParserStatus {
	status := ParserStatus{State: RunnerReady}
	switch {
	case p.Off:
		status.State = RunnerOff
	case p.Runner == nil:
		status.State = RunnerUnavailable
	default:
		if err := p.Runner.Verify(); errors.Is(err, ccusage.ErrUnavailable) {
			status.State = RunnerUnavailable
		} else if err != nil {
			status.State = RunnerUnpinned
		}
	}
	for _, reading := range (collect.DayLevel{Roots: p.Roots, Runner: p.Runner, Off: p.Off}).Read(ctx) {
		report := AgentReport{Source: reading.Health.Source, Status: reading.Health.Status, Capabilities: reading.Health.Capabilities}
		if len(reading.Health.Warnings) > 0 {
			report.Reason = reading.Health.Warnings[0].Code
		}
		if reading.Health.RecordCount != nil {
			report.Days = *reading.Health.RecordCount
		}
		status.Agents = append(status.Agents, report)
	}
	return status
}
