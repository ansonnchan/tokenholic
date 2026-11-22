package ccusage

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ansonnchan/tokenholic/internal/normalize"
)

const (
	// AdapterVersion identifies how reports become observations. Changing
	// what a stored day total means requires a new value.
	AdapterVersion = "ccusage-day-v1"
	// SourceVersion names the release whose output the observations hold.
	SourceVersion = "ccusage-" + PinnedVersion
	// unknownModel is the name ccusage writes when a record names no model.
	unknownModel = "unknown"
)

// Profile is what the pinned daily report can state about one agent: which
// source its usage belongs to and how far each token category is covered.
//
// ccusage writes zero for a category an agent's records do not carry. The
// capabilities are what tell that zero from a real one: an unsupported
// category is recorded as unknown, never as none.
type Profile struct {
	Agent        Agent
	Source       normalize.Source
	Capabilities normalize.Capabilities
}

// Profiles returns every agent's profile in the order of Agents. Each is
// taken from how the pinned release reads that agent's records; the README
// lists the evidence and the limits.
func Profiles() []Profile {
	const (
		reported    = normalize.CapabilityReported
		derived     = normalize.CapabilityDerived
		unsupported = normalize.CapabilityUnsupported
	)
	// Reasoning is unsupported throughout: the daily report never gives it
	// per model. Where an agent counts it inside output it is already
	// there; where an agent reports it apart, see Observations.
	return []Profile{
		// Copilot's input includes both cache buckets; ccusage subtracts them.
		{Agent: AgentCopilot, Source: normalize.SourceCopilot, Capabilities: normalize.Capabilities{
			Model: reported, InputUncached: derived, InputCacheRead: reported, InputCacheWrite: reported,
			Output: reported, ReasoningOutput: unsupported,
		}},
		// Gemini records no cache writes. Its input includes cached and tool
		// tokens, which ccusage separates.
		{Agent: AgentGemini, Source: normalize.SourceGemini, Capabilities: normalize.Capabilities{
			Model: reported, InputUncached: derived, InputCacheRead: reported, InputCacheWrite: unsupported,
			Output: derived, ReasoningOutput: unsupported,
		}},
		// Grok's input includes cache reads and writes; ccusage subtracts them.
		{Agent: AgentGrok, Source: normalize.SourceGrok, Capabilities: normalize.Capabilities{
			Model: reported, InputUncached: derived, InputCacheRead: reported, InputCacheWrite: reported,
			Output: reported, ReasoningOutput: unsupported,
		}},
		{Agent: AgentOpenCode, Source: normalize.SourceOpenCode, Capabilities: normalize.Capabilities{
			Model: reported, InputUncached: reported, InputCacheRead: reported, InputCacheWrite: reported,
			Output: derived, ReasoningOutput: unsupported,
		}},
		// Qwen records no cache writes.
		{Agent: AgentQwen, Source: normalize.SourceQwen, Capabilities: normalize.Capabilities{
			Model: reported, InputUncached: reported, InputCacheRead: reported, InputCacheWrite: unsupported,
			Output: derived, ReasoningOutput: unsupported,
		}},
	}
}

// ProfileFor returns one agent's profile.
func ProfileFor(agent Agent) (Profile, bool) {
	for _, profile := range Profiles() {
		if profile.Agent == agent {
			return profile, true
		}
	}
	return Profile{}, false
}

// Observations turns a validated report into day totals: one record per
// day and model, filed at the start of that UTC day, holding the day's whole
// value so far. A later report for the same day yields records with the same
// identities, which is what lets storage replace them.
//
// Three things in a row have no model to belong to and are kept, together,
// as one record with an unknown model, so every token ccusage counted is
// counted here and none is credited to a model on a guess:
//
//   - usage under ccusage's placeholder name for a record with no model;
//   - any part of the day's categories no model breakdown accounts for;
//   - the day's extra tokens, which the report gives per day only. When the
//     day has exactly one model and nothing else unattributed they are that
//     model's, and are added to its output, where reasoning belongs.
func (p Profile) Observations(report Report) ([]normalize.Observation, error) {
	var observations []normalize.Observation
	for _, day := range report.Days {
		start, err := time.Parse(time.DateOnly, day.Date)
		if err != nil {
			return nil, fmt.Errorf("day %q: %w", day.Date, err)
		}
		unattributed := day.Unattributed()
		var named []ModelUsage
		for _, model := range day.Models {
			if model.Model == unknownModel {
				if err := unattributed.add(model.Counts); err != nil {
					return nil, fmt.Errorf("day %s: %w", day.Date, err)
				}
				continue
			}
			named = append(named, model)
		}
		extra := day.Extra()
		if len(named) == 1 && unattributed == (Counts{}) {
			if named[0].Output, err = addChecked(named[0].Output, extra); err != nil {
				return nil, fmt.Errorf("day %s: %w", day.Date, err)
			}
			extra = 0
		}
		if unattributed.Output, err = addChecked(unattributed.Output, extra); err != nil {
			return nil, fmt.Errorf("day %s: %w", day.Date, err)
		}
		for _, model := range named {
			if model.Counts == (Counts{}) {
				continue
			}
			observations = append(observations, p.observation(start, model.Model, model.Counts, nil))
		}
		if unattributed != (Counts{}) {
			observations = append(observations, p.observation(start, "", unattributed,
				[]normalize.Warning{{Code: "model_unattributed"}}))
		}
	}
	for _, observation := range observations {
		if err := observation.Validate(); err != nil {
			return nil, fmt.Errorf("day total for %s: %w", p.Source, err)
		}
	}
	return observations, nil
}

func (p Profile) observation(day time.Time, model string, counts Counts, warnings []normalize.Warning) normalize.Observation {
	resolution := normalize.ModelReported
	if model == "" {
		resolution = normalize.ModelUnknown
	}
	return normalize.Observation{
		Source: p.Source, Surface: normalize.SurfaceCLI, SourceVersion: SourceVersion,
		// The identity is the agent, the day, and the model. Nothing about
		// the day's value is in it, so a grown day is the same record.
		SourceRecordID: strings.Join([]string{"ccusage-day", string(p.Agent), day.Format(time.DateOnly), model}, "\x00"),
		OccurredAt:     day.UTC(), ObservedModelID: model, ModelResolution: resolution,
		Tokens: normalize.TokenCounts{
			InputUncached:   p.count(p.Capabilities.InputUncached, counts.Input),
			InputCacheRead:  p.count(p.Capabilities.InputCacheRead, counts.CacheRead),
			InputCacheWrite: p.count(p.Capabilities.InputCacheWrite, counts.CacheCreation),
			Output:          p.count(p.Capabilities.Output, counts.Output),
		},
		CountSemantics: normalize.CountDayTotal,
		// A day total is a sum ccusage computed from the agent's records,
		// never a number the agent wrote.
		Measurement:    normalize.MeasurementDerived,
		AdapterVersion: AdapterVersion, NormalizationVersion: normalize.SchemaVersion,
		Warnings: warnings,
	}
}

// count keeps a category the agent's records carry, zero included. For a
// category they do not carry, ccusage's zero means "not reported" and is
// recorded as unknown; a value that is somehow present is kept, because a
// reported number is never discarded.
func (p Profile) count(support normalize.CapabilitySupport, value uint64) *uint64 {
	if support == normalize.CapabilityUnsupported && value == 0 {
		return nil
	}
	return &value
}

// Total returns the tokens a report's observations hold: the four
// categories that are not subsets of another. It exists for tests and
// diagnostics that compare against ccusage's own totals.
func Total(observations []normalize.Observation) (uint64, error) {
	var total uint64
	for _, observation := range observations {
		known, err := observation.Tokens.KnownTotal()
		if err != nil {
			return 0, err
		}
		if known == nil {
			continue
		}
		if math.MaxUint64-total < *known {
			return 0, errors.New("token total overflows")
		}
		total += *known
	}
	return total, nil
}
