package normalize

import (
	"errors"
	"fmt"
	"time"
)

// SchemaVersion identifies the normalized observation contract.
const SchemaVersion = "1"

// Source identifies the coding-agent provider that produced local evidence.
type Source string

const (
	// SourceClaude identifies Claude coding-agent evidence.
	SourceClaude Source = "claude"
	// SourceCodex identifies Codex coding-agent evidence.
	SourceCodex Source = "codex"
	// SourceCopilot identifies GitHub Copilot CLI evidence.
	SourceCopilot Source = "copilot"
	// SourceGemini identifies Gemini CLI evidence.
	SourceGemini Source = "gemini"
	// SourceGrok identifies Grok Build CLI evidence.
	SourceGrok Source = "grok"
	// SourceOpenCode identifies OpenCode evidence.
	SourceOpenCode Source = "opencode"
	// SourceQwen identifies Qwen Code evidence.
	SourceQwen Source = "qwen"
)

// DayLevelSources lists the sources tokenholic has no reader of its own for.
// Their usage arrives as one total per UTC day and model, never per request.
// Claude and Codex are not among them and never will be read that way: they
// have native adapters, and a second path would count their usage twice.
func DayLevelSources() []Source {
	return []Source{SourceCopilot, SourceGemini, SourceGrok, SourceOpenCode, SourceQwen}
}

// DayLevel reports whether the source's usage is known only per UTC day.
func (s Source) DayLevel() bool {
	for _, source := range DayLevelSources() {
		if s == source {
			return true
		}
	}
	return false
}

// Surface identifies the product context in which evidence was recorded.
type Surface string

const (
	// SurfaceCLI identifies a provider command-line client.
	SurfaceCLI Surface = "cli"
	// SurfaceDesktopCode identifies Claude Desktop Code sessions.
	SurfaceDesktopCode Surface = "desktop_code"
	// SurfaceDesktopAgent identifies Claude Desktop Agent sessions.
	SurfaceDesktopAgent Surface = "desktop_agent"
	// SurfaceDesktopCowork identifies Claude Desktop Cowork sessions.
	SurfaceDesktopCowork Surface = "desktop_cowork"
	// SurfaceDesktop identifies evidence attributable to a desktop client.
	SurfaceDesktop Surface = "desktop"
	// SurfaceSharedLocal preserves ambiguity when CLI and desktop share storage.
	SurfaceSharedLocal Surface = "shared_local"
)

// ModelResolution records the evidence quality behind model attribution.
type ModelResolution string

const (
	// ModelReported means the source record directly names the observed model.
	ModelReported ModelResolution = "reported"
	// ModelResolved means canonical metadata was mapped from an observed model.
	ModelResolved ModelResolution = "resolved"
	// ModelInferred means surrounding source context supplies the observed model.
	ModelInferred ModelResolution = "inferred"
	// ModelUnknown means available evidence cannot identify the model safely.
	ModelUnknown ModelResolution = "unknown"
)

// CountSemantics distinguishes individual usage from running snapshots.
type CountSemantics string

const (
	// CountIncremental means the record contributes its values once.
	CountIncremental CountSemantics = "incremental"
	// CountCumulative means the record is a running counter that requires reduction.
	CountCumulative CountSemantics = "cumulative_snapshot"
	// CountDayTotal means the record is the whole of one UTC day for one
	// model, as known when it was read. The day's total grows until the day
	// ends, so a later reading replaces this record; it is never added to it.
	CountDayTotal CountSemantics = "day_total"
)

// Granularity says how coarse a usage value's time is. The zero value is the
// ordinary case: usage placed at the minute it happened.
type Granularity string

const (
	// GranularityMinute is usage placed at the minute it happened. It is
	// stored and uploaded as an absent field.
	GranularityMinute Granularity = ""
	// GranularityDay is a total for one UTC calendar day, filed at the start
	// of that day. Where it falls against a local midnight is approximate.
	GranularityDay Granularity = "day"
)

// Valid reports whether the granularity is one tokenholic records.
func (g Granularity) Valid() bool {
	return g == GranularityMinute || g == GranularityDay
}

// ValidateGranularity holds a usage value's granularity to its source, in
// both directions: a day-level source reports whole UTC days and nothing
// finer, and no other source may report a day. A day is filed at its start
// and carries no pricing evidence, because it spans many requests. The local
// store and the sync protocol both apply this one rule.
func ValidateGranularity(granularity Granularity, source Source, start time.Time, pricingEvidence bool) error {
	if !granularity.Valid() {
		return fmt.Errorf("granularity %q is invalid", granularity)
	}
	if source.DayLevel() != (granularity == GranularityDay) {
		return fmt.Errorf("granularity %q does not belong to source %q", granularity, source)
	}
	if granularity != GranularityDay {
		return nil
	}
	if !StartOfDay(start) {
		return errors.New("a day-level value is filed at the start of its UTC day")
	}
	if pricingEvidence {
		return errors.New("a day-level value cannot carry a service tier or a prompt band")
	}
	return nil
}

// StartOfDay reports whether a time is exactly midnight UTC, where a
// day-level value is filed.
func StartOfDay(moment time.Time) bool {
	return moment.Equal(moment.UTC().Truncate(24 * time.Hour))
}

// Measurement records how a normalized value was obtained.
type Measurement string

const (
	// MeasurementReported means the source provided the normalized value directly.
	MeasurementReported Measurement = "reported"
	// MeasurementDerived means tokenholic computed the value deterministically.
	MeasurementDerived Measurement = "derived"
	// MeasurementEstimated means the value relies on an explicit approximation.
	MeasurementEstimated Measurement = "estimated"
)

// Warning is a stable machine-readable condition with optional safe context.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// TokenCounts stores independent token categories; nil means unavailable, not zero.
//
// InputCacheWrite is the total written at every cache lifetime. The 5m and 1h
// fields split that total only when the source reports a time-to-live; nil
// means the lifetime is unknown, never that nothing was written at it. Their
// sum cannot exceed the total, and any remainder has an unknown lifetime.
type TokenCounts struct {
	InputUncached     *uint64 `json:"input_uncached_tokens"`
	InputCacheRead    *uint64 `json:"input_cache_read_tokens"`
	InputCacheWrite   *uint64 `json:"input_cache_write_tokens"`
	InputCacheWrite5m *uint64 `json:"input_cache_write_5m_tokens"`
	InputCacheWrite1h *uint64 `json:"input_cache_write_1h_tokens"`
	Output            *uint64 `json:"output_tokens"`
	ReasoningOutput   *uint64 `json:"reasoning_output_tokens"`
}

// Observation is one validated, source-neutral unit of local usage evidence.
type Observation struct {
	Source           Source          `json:"source"`
	Surface          Surface         `json:"surface"`
	SourceVersion    string          `json:"source_version"`
	SourceRecordID   string          `json:"source_record_id"`
	SessionIDLocal   *string         `json:"session_id_local"`
	OccurredAt       time.Time       `json:"occurred_at"`
	ObservedModelID  string          `json:"observed_model_id,omitempty"`
	CanonicalModelID *string         `json:"canonical_model_id"`
	ModelProvider    *string         `json:"model_provider"`
	PricingKey       *string         `json:"pricing_key"`
	ModelResolution  ModelResolution `json:"model_resolution"`
	Tokens           TokenCounts     `json:"tokens"`
	// ServiceTier and PromptTokens are pricing evidence: facts about one
	// request that decide which of a model's prices applies. PromptTokens is
	// the whole prompt, cached or not, and is set only when the record is
	// exactly one request. Neither is ever guessed.
	ServiceTier          ServiceTier    `json:"service_tier,omitempty"`
	PromptTokens         *uint64        `json:"prompt_tokens,omitempty"`
	CountSemantics       CountSemantics `json:"count_semantics"`
	Measurement          Measurement    `json:"measurement"`
	AdapterVersion       string         `json:"adapter_version"`
	NormalizationVersion string         `json:"normalization_version"`
	Warnings             []Warning      `json:"warnings,omitempty"`
}
