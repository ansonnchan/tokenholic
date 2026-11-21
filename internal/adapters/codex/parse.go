package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ansonnchan/tokenholic/internal/adapters/jsonl"
	"github.com/ansonnchan/tokenholic/internal/normalize"
)

// AdapterVersion identifies the Codex-to-normalized mapping contract. Version
// 2 reads per-request usage and identifies each request by its session, turn,
// and running total, so a replayed history is recognized as a copy. Version 3
// reads the service tier in effect and each request's prompt size.
const AdapterVersion = "3"

// Result contains valid observations and safe warning codes from one rollout.
type Result struct {
	Observations []normalize.Observation
	Warnings     []normalize.Warning
}

type record struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Payload   struct {
		Type      string `json:"type"`
		Model     string `json:"model"`
		ID        string `json:"id"`
		SessionID string `json:"session_id"`
		TurnID    string `json:"turn_id"`
		// ThreadSettings is read for its service tier alone. Its other
		// members describe the user's workspace and are never decoded.
		ThreadSettings *struct {
			ServiceTier json.RawMessage `json:"service_tier"`
		} `json:"thread_settings"`
		Info struct {
			Total tokenUsage  `json:"total_token_usage"`
			Last  *tokenUsage `json:"last_token_usage"`
		} `json:"info"`
	} `json:"payload"`
}

// rollout is the attribution state carried through one file.
type rollout struct {
	// session is shared by a conversation and every thread forked from it.
	session string
	// thread is this file's own identity, used only to keep the running
	// counters of legacy rollouts apart.
	thread string
	model  string
	turn   string
	// tier is the service tier of the last settings event that named one. It
	// stays unknown until then: Codex writes no settings event before a
	// thread's first turn, and the configuration file is never consulted.
	tier normalize.ServiceTier
	// previousTotal detects an event that repeats the running total: it
	// reports no new usage.
	previousTotal string
}

type tokenUsage struct {
	InputTokens           *uint64 `json:"input_tokens"`
	CachedInputTokens     *uint64 `json:"cached_input_tokens"`
	OutputTokens          *uint64 `json:"output_tokens"`
	ReasoningOutputTokens *uint64 `json:"reasoning_output_tokens"`
}

// Parse follows session, turn, and token events in source order. logicalFileID
// stands in for the session when the rollout does not name one.
//
// A token event normally carries both the usage of the request that just
// finished and the session's running total. The request usage is emitted
// directly as an incremental observation whose identity is the session, turn,
// and running total. That identity is what makes replays safe: a forked or
// resumed rollout copies its parent's history under new timestamps, and each
// copy maps to the same identity as its original, so deduplication keeps one.
//
// Rollouts that predate per-request usage carry only the running total. Those
// are emitted as cumulative snapshots for the collector to reduce.
func Parse(reader io.Reader, surface normalize.Surface, sourceVersion, logicalFileID string) (Result, error) {
	lines, err := jsonl.Read(reader, 0)
	if err != nil {
		return Result{}, err
	}
	result := Result{}
	for _, code := range lines.Warnings {
		result.Warnings = append(result.Warnings, normalize.Warning{Code: code})
	}
	state := rollout{session: logicalFileID, thread: logicalFileID}
	named := false
	for _, line := range lines.Records {
		var parsed record
		if err := json.Unmarshal(line.Data, &parsed); err != nil {
			result.Warnings = append(result.Warnings, normalize.Warning{Code: "malformed_record"})
			continue
		}
		switch parsed.Type {
		case "session_meta":
			// Only the first header describes this file. A fork follows it with
			// its parent's header, which must not rename the thread.
			if named {
				continue
			}
			named = true
			if parsed.Payload.ID != "" {
				state.thread = parsed.Payload.ID
				state.session = parsed.Payload.ID
			}
			if parsed.Payload.SessionID != "" {
				state.session = parsed.Payload.SessionID
			}
		case "turn_context":
			// Token events omit the model, so the active turn context makes later
			// attribution inferred rather than directly reported.
			state.model = parsed.Payload.Model
			state.turn = parsed.Payload.TurnID
			if !named && parsed.Payload.SessionID != "" {
				// Legacy rollouts name the session on the turn context.
				state.session, state.thread = parsed.Payload.SessionID, parsed.Payload.SessionID
			}
		case "event_msg":
			if parsed.Payload.Type == "thread_settings_applied" {
				state.applySettings(parsed)
				continue
			}
			if parsed.Payload.Type != "token_count" {
				continue
			}
			observation, warning, ok := state.usageObservation(parsed, surface, sourceVersion)
			if warning.Code != "" {
				result.Warnings = append(result.Warnings, warning)
			}
			if ok {
				result.Observations = append(result.Observations, observation)
			}
		}
	}
	return result, nil
}

// applySettings follows the service tier through settings events. An event
// that omits the tier leaves the previous one in effect. An event that names a
// tier this adapter does not know clears it: the earlier tier no longer
// applies, and the new one cannot be priced as something else.
func (state *rollout) applySettings(parsed record) {
	settings := parsed.Payload.ThreadSettings
	if settings == nil || len(settings.ServiceTier) == 0 {
		return
	}
	var name string
	if err := json.Unmarshal(settings.ServiceTier, &name); err != nil {
		state.tier = normalize.ServiceTierUnknown
		return
	}
	switch name {
	case "priority", "fast": // "fast" is the earlier spelling of priority
		state.tier = normalize.ServiceTierPriority
	case "flex":
		state.tier = normalize.ServiceTierFlex
	case "default", "standard": // Codex Desktop writes "standard"
		state.tier = normalize.ServiceTierStandard
	default:
		state.tier = normalize.ServiceTierUnknown
	}
}

func (state *rollout) usageObservation(parsed record, surface normalize.Surface, sourceVersion string) (normalize.Observation, normalize.Warning, bool) {
	total, last := parsed.Payload.Info.Total, parsed.Payload.Info.Last
	if total.empty() && (last == nil || last.empty()) {
		return normalize.Observation{}, normalize.Warning{Code: "unsupported_record"}, false
	}
	if parsed.Timestamp.IsZero() {
		return normalize.Observation{}, normalize.Warning{Code: "unsupported_record"}, false
	}
	usage, session, semantics := total, state.thread, normalize.CountCumulative
	identity := ""
	// Pricing evidence belongs to one request, so only per-request usage
	// carries it. A legacy running total spans many requests.
	tier, prompt := normalize.ServiceTierUnknown, (*uint64)(nil)
	if last != nil && !last.empty() {
		if !total.empty() {
			if state.previousTotal == total.key() {
				// The running total did not move, so this event repeats the
				// previous request instead of reporting a new one.
				return normalize.Observation{}, normalize.Warning{}, false
			}
			state.previousTotal = total.key()
		}
		usage, session, semantics = *last, state.session, normalize.CountIncremental
		identity = requestID(state.session, state.turn, total, *last, parsed.Timestamp)
		// Codex input counts the whole prompt, cached part included.
		tier, prompt = state.tier, last.InputTokens
	}
	uncached, valid := uncachedInput(usage.InputTokens, usage.CachedInputTokens)
	if !valid {
		return normalize.Observation{}, normalize.Warning{Code: "invalid_token_relationship"}, false
	}
	tokens := normalize.TokenCounts{
		InputUncached: uncached, InputCacheRead: usage.CachedInputTokens,
		Output: usage.OutputTokens, ReasoningOutput: usage.ReasoningOutputTokens,
	}
	if identity == "" {
		identity = recordID(session, parsed.Timestamp, state.model, tokens)
	}
	resolution := normalize.ModelInferred
	warnings := []normalize.Warning{{Code: "model_inferred"}}
	if state.model == "" {
		resolution = normalize.ModelUnknown
		warnings = []normalize.Warning{{Code: "model_unavailable"}}
	}
	observation := normalize.Observation{
		Source: normalize.SourceCodex, Surface: surface, SourceVersion: sourceVersion,
		SourceRecordID: identity, SessionIDLocal: optionalString(session), OccurredAt: parsed.Timestamp,
		ObservedModelID: state.model, ModelResolution: resolution, Tokens: tokens,
		ServiceTier: tier, PromptTokens: prompt,
		CountSemantics: semantics, Measurement: normalize.MeasurementReported,
		AdapterVersion: AdapterVersion, NormalizationVersion: normalize.SchemaVersion, Warnings: warnings,
	}
	if err := observation.Validate(); err != nil {
		return normalize.Observation{}, normalize.Warning{Code: "invalid_normalized_record"}, false
	}
	return observation, normalize.Warning{}, true
}

func (usage tokenUsage) empty() bool {
	return usage.InputTokens == nil && usage.CachedInputTokens == nil && usage.OutputTokens == nil && usage.ReasoningOutputTokens == nil
}

// key renders the counters for comparison by value. An absent counter is
// distinct from zero, and a non-empty usage never renders as the empty string.
func (usage tokenUsage) key() string {
	return strings.Join([]string{
		pointerValue(usage.InputTokens), pointerValue(usage.CachedInputTokens),
		pointerValue(usage.OutputTokens), pointerValue(usage.ReasoningOutputTokens),
	}, ",")
}

func uncachedInput(total, cached *uint64) (*uint64, bool) {
	// Codex input includes cached input. Normalize to non-overlapping categories
	// before any total is calculated.
	if total == nil {
		return nil, true
	}
	if cached == nil {
		value := *total
		return &value, true
	}
	if *cached > *total {
		return nil, false
	}
	value := *total - *cached
	return &value, true
}

// requestID identifies one model request by where it sits in its session:
// the turn it belongs to and the running total after it. Neither the event's
// timestamp nor the file it was read from takes part, because a replay
// rewrites the first and changes the second. Only a rollout without a running
// total falls back to the timestamp.
func requestID(session, turn string, total, last tokenUsage, timestamp time.Time) string {
	parts := []string{"codex-request-v2", session, turn}
	if total.empty() {
		parts = append(parts, "at", timestamp.UTC().Format(time.RFC3339Nano),
			pointerValue(last.InputTokens), pointerValue(last.CachedInputTokens),
			pointerValue(last.OutputTokens), pointerValue(last.ReasoningOutputTokens))
	} else {
		parts = append(parts, "total", total.key())
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "codex-request-" + hex.EncodeToString(sum[:16])
}

func recordID(session string, timestamp time.Time, model string, tokens normalize.TokenCounts) string {
	// Overlapping CLI/Desktop roots produce the same identity because only source
	// semantics participate; storage paths and raw event content do not.
	parts := []string{
		"codex-rollout-v1", session, timestamp.UTC().Format(time.RFC3339Nano), model,
		pointerValue(tokens.InputUncached), pointerValue(tokens.InputCacheRead),
		pointerValue(tokens.Output), pointerValue(tokens.ReasoningOutput),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "codex-snapshot-" + hex.EncodeToString(sum[:16])
}

func pointerValue(value *uint64) string {
	if value == nil {
		return "null"
	}
	return fmt.Sprint(*value)
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
