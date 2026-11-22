package collect

import (
	"context"
	"errors"

	"github.com/ansonnchan/tokenholic/internal/adapters"
	"github.com/ansonnchan/tokenholic/internal/adapters/ccusage"
	"github.com/ansonnchan/tokenholic/internal/normalize"
)

// Warning codes that say why a day-level source could not be read. They are
// stored with the source's health and shown by doctor and the SOURCES view.
const (
	// WarningCCUsageOff: the user turned ccusage off.
	WarningCCUsageOff = "ccusage_off"
	// WarningCCUsageNotInstalled: this install has no ccusage executable.
	WarningCCUsageNotInstalled = "ccusage_not_installed"
	// WarningCCUsageUnpinned: an executable exists but is not the pinned
	// release, so it was not run.
	WarningCCUsageUnpinned = "ccusage_unpinned"
	// WarningCCUsageMalformed: ccusage answered outside the pinned contract.
	WarningCCUsageMalformed = "ccusage_malformed_output"
	// WarningCCUsageTimeout: the report did not finish in time.
	WarningCCUsageTimeout = "ccusage_timeout"
	// WarningCCUsageFailed: ccusage could not be started or exited in error.
	WarningCCUsageFailed = "ccusage_failed"
)

// DayRunner runs the pinned ccusage report for one agent.
type DayRunner interface {
	Verify() error
	Daily(context.Context, ccusage.Agent) (ccusage.Report, error)
}

// DayLevel reads the agents that have no native adapter.
//
// It looks for each agent's records first and runs ccusage only for agents
// that are present, so third-party code never runs for an agent the user
// does not have. Every agent is read on its own: one that fails, times out,
// or answers outside the contract is reported and contributes nothing, and
// the others are unaffected.
type DayLevel struct {
	Roots  []adapters.AgentRoot
	Runner DayRunner
	// Off is the user's choice not to run ccusage.
	Off bool
}

// DayReading is one agent's result: its health, and its day totals when the
// report was read. An agent that was not read has no observations, which
// leaves whatever was stored for it untouched. It is never read as zero.
type DayReading struct {
	Health       normalize.SourceHealth
	Observations []normalize.Observation
	// Read reports that ccusage answered with a valid report.
	Read bool
}

// Read returns one reading per configured agent, in the order of Roots.
func (d DayLevel) Read(ctx context.Context) []DayReading {
	readings := make([]DayReading, 0, len(d.Roots))
	// The executable is verified once, and only if some agent needs it.
	var verified bool
	var verifyErr error
	for _, root := range d.Roots {
		reading := DayReading{Health: normalize.SourceHealth{
			Source: root.Source, Surface: normalize.SurfaceCLI, Status: root.Status(),
			Format: ccusage.SchemaID, Capabilities: unsupportedCapabilities(),
		}}
		profile, known := profileForSource(root.Source)
		if known {
			reading.Health.Capabilities = profile.Capabilities
		}
		if reading.Health.Status != normalize.HealthDetected {
			if reading.Health.Status != normalize.HealthNotFound {
				reading.Health.Warnings = []normalize.Warning{{Code: "source_root_unreadable"}}
			}
			readings = append(readings, reading)
			continue
		}
		unavailable := func(code string) {
			reading.Health.Status = normalize.HealthUnavailable
			reading.Health.Warnings = []normalize.Warning{{Code: code}}
		}
		failed := func(code string) {
			reading.Health.Status = normalize.HealthError
			reading.Health.Warnings = []normalize.Warning{{Code: code}}
		}
		switch {
		case d.Off:
			unavailable(WarningCCUsageOff)
		case d.Runner == nil || !known:
			unavailable(WarningCCUsageNotInstalled)
		default:
			if !verified {
				verifyErr, verified = d.Runner.Verify(), true
			}
			switch {
			case errors.Is(verifyErr, ccusage.ErrUnavailable):
				unavailable(WarningCCUsageNotInstalled)
			case verifyErr != nil:
				unavailable(WarningCCUsageUnpinned)
			default:
				report, err := d.Runner.Daily(ctx, profile.Agent)
				var observations []normalize.Observation
				if err == nil {
					// A report that cannot become valid day totals is as
					// unusable as one that failed the contract.
					if observations, err = profile.Observations(report); err != nil {
						err = ccusage.ErrMalformed
					}
				}
				switch {
				case err == nil:
					count := uint64(len(report.Days))
					reading.Health.RecordCount = &count
					reading.Observations, reading.Read = observations, true
				case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
					failed("collection_canceled")
				case errors.Is(err, ccusage.ErrMalformed):
					failed(WarningCCUsageMalformed)
				case errors.Is(err, ccusage.ErrTimeout):
					failed(WarningCCUsageTimeout)
				case errors.Is(err, ccusage.ErrUnavailable):
					unavailable(WarningCCUsageNotInstalled)
				case errors.Is(err, ccusage.ErrUnpinned):
					unavailable(WarningCCUsageUnpinned)
				default:
					failed(WarningCCUsageFailed)
				}
			}
		}
		readings = append(readings, reading)
	}
	return readings
}

func profileForSource(source normalize.Source) (ccusage.Profile, bool) {
	for _, profile := range ccusage.Profiles() {
		if profile.Source == source {
			return profile, true
		}
	}
	return ccusage.Profile{}, false
}

func unsupportedCapabilities() normalize.Capabilities {
	return normalize.Capabilities{
		Model: normalize.CapabilityUnsupported, InputUncached: normalize.CapabilityUnsupported,
		InputCacheRead: normalize.CapabilityUnsupported, InputCacheWrite: normalize.CapabilityUnsupported,
		Output: normalize.CapabilityUnsupported, ReasoningOutput: normalize.CapabilityUnsupported,
	}
}
