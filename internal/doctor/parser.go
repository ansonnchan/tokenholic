package doctor

import (
	"context"
	"errors"

	"github.com/ansonnchan/tokenholic/internal/adapters/ccusage"
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

// AgentState is the outcome of one agent's pinned report.
type AgentState string

const (
	// AgentValid means the report satisfied the pinned contract.
	AgentValid AgentState = "valid"
	// AgentMalformed means ccusage answered with something the contract
	// does not describe.
	AgentMalformed AgentState = "malformed"
	// AgentTimedOut means the report did not finish within its time limit.
	AgentTimedOut AgentState = "timed_out"
	// AgentFailed means ccusage could not be started or exited with an error.
	AgentFailed AgentState = "failed"
)

// AgentReport is one agent's result. It holds a count, never report content.
type AgentReport struct {
	Agent ccusage.Agent
	State AgentState
	// Days is how many daily rows a valid report held.
	Days int
}

// ParserStatus is the health of the pinned ccusage runner and of each
// agent's report. It retains no ccusage output.
type ParserStatus struct {
	State  RunnerState
	Agents []AgentReport
}

// AgentRunner runs one agent's pinned daily report.
type AgentRunner interface {
	Verify() error
	Daily(context.Context, ccusage.Agent) (ccusage.Report, error)
}

// CCUsageProbe checks the pinned runner and every agent's report.
type CCUsageProbe struct {
	Runner AgentRunner
	// Off is the user's choice not to run ccusage.
	Off bool
}

// Check verifies the executable, then runs each agent's report on its own so
// one failing agent does not hide the others.
func (p CCUsageProbe) Check(ctx context.Context) ParserStatus {
	if p.Off {
		return ParserStatus{State: RunnerOff}
	}
	if p.Runner == nil {
		return ParserStatus{State: RunnerUnavailable}
	}
	if err := p.Runner.Verify(); err != nil {
		if errors.Is(err, ccusage.ErrUnavailable) {
			return ParserStatus{State: RunnerUnavailable}
		}
		return ParserStatus{State: RunnerUnpinned}
	}
	status := ParserStatus{State: RunnerReady}
	for _, agent := range ccusage.Agents() {
		report, err := p.Runner.Daily(ctx, agent)
		result := AgentReport{Agent: agent, State: AgentValid, Days: len(report.Days)}
		switch {
		case err == nil:
		case errors.Is(err, ccusage.ErrMalformed):
			result.State = AgentMalformed
		case errors.Is(err, ccusage.ErrTimeout), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
			result.State = AgentTimedOut
		default:
			result.State = AgentFailed
		}
		status.Agents = append(status.Agents, result)
	}
	return status
}
