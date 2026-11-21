package claude

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/ansonnchan/tokenholic/internal/adapters/jsonl"
	"github.com/ansonnchan/tokenholic/internal/normalize"
)

// AdapterVersion identifies the Claude-to-normalized mapping contract. Version
// 2 excludes locally generated lines and reads cache-write lifetimes; version
// 3 reads each request's service tier and prompt size.
const AdapterVersion = "3"

// syntheticModel marks assistant lines Claude Code writes itself, such as
// interruption and API-failure notices. They never reached a model.
const syntheticModel = "<synthetic>"

// Result contains valid observations and safe warning codes from one stream.
type Result struct {
	Observations []normalize.Observation
	Warnings     []normalize.Warning
}

type record struct {
	Type              string    `json:"type"`
	SessionID         string    `json:"sessionId"`
	RequestID         string    `json:"requestId"`
	Timestamp         time.Time `json:"timestamp"`
	IsAPIErrorMessage bool      `json:"isApiErrorMessage"`
	Message           struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens         *uint64        `json:"input_tokens"`
			CacheReadTokens     *uint64        `json:"cache_read_input_tokens"`
			CacheCreationTokens *uint64        `json:"cache_creation_input_tokens"`
			CacheCreation       *cacheCreation `json:"cache_creation"`
			OutputTokens        *uint64        `json:"output_tokens"`
			ServiceTier         string         `json:"service_tier"`
			Speed               string         `json:"speed"`
		} `json:"usage"`
	} `json:"message"`
}

// cacheCreation is Claude's optional per-lifetime split of the combined
// cache-creation counter.
type cacheCreation struct {
	Ephemeral5m *uint64 `json:"ephemeral_5m_input_tokens"`
	Ephemeral1h *uint64 `json:"ephemeral_1h_input_tokens"`
}

// Parse reads Claude assistant usage records and skips all non-usage content.
func Parse(reader io.Reader, surface normalize.Surface, sourceVersion string) (Result, error) {
	lines, err := jsonl.Read(reader, 0)
	if err != nil {
		return Result{}, err
	}
	result := Result{}
	for _, code := range lines.Warnings {
		result.Warnings = append(result.Warnings, normalize.Warning{Code: code})
	}
	for _, line := range lines.Records {
		var parsed record
		if err := json.Unmarshal(line.Data, &parsed); err != nil {
			result.Warnings = append(result.Warnings, normalize.Warning{Code: "malformed_record"})
			continue
		}
		if parsed.Type != "assistant" {
			continue
		}
		if parsed.Message.Model == syntheticModel || parsed.IsAPIErrorMessage {
			// Not model usage: counting these would add a phantom model and
			// tokens no provider would bill.
			continue
		}
		tokens := normalize.TokenCounts{
			InputUncached: parsed.Message.Usage.InputTokens, InputCacheRead: parsed.Message.Usage.CacheReadTokens,
			Output: parsed.Message.Usage.OutputTokens,
		}
		warnings := []normalize.Warning(nil)
		var consistent bool
		tokens.InputCacheWrite, tokens.InputCacheWrite5m, tokens.InputCacheWrite1h, consistent = cacheWrite(
			parsed.Message.Usage.CacheCreationTokens, parsed.Message.Usage.CacheCreation)
		if !consistent {
			warning := normalize.Warning{Code: "cache_write_split_inconsistent"}
			warnings = append(warnings, warning)
			result.Warnings = append(result.Warnings, warning)
		}
		if !anyKnown(tokens) || parsed.Timestamp.IsZero() {
			result.Warnings = append(result.Warnings, normalize.Warning{Code: "unsupported_record"})
			continue
		}
		resolution := normalize.ModelReported
		if parsed.Message.Model == "" {
			resolution = normalize.ModelUnknown
			warnings = append(warnings, normalize.Warning{Code: "model_unavailable"})
		}
		session := optionalString(parsed.SessionID)
		observation := normalize.Observation{
			Source: normalize.SourceClaude, Surface: surface, SourceVersion: sourceVersion,
			SourceRecordID: recordID(parsed, line.Number), SessionIDLocal: session, OccurredAt: parsed.Timestamp,
			ObservedModelID: parsed.Message.Model, ModelResolution: resolution, Tokens: tokens,
			ServiceTier:    serviceTier(parsed.Message.Usage.ServiceTier, parsed.Message.Usage.Speed),
			PromptTokens:   promptTokens(tokens),
			CountSemantics: normalize.CountIncremental, Measurement: normalize.MeasurementReported,
			AdapterVersion: AdapterVersion, NormalizationVersion: normalize.SchemaVersion, Warnings: warnings,
		}
		if err := observation.Validate(); err != nil {
			result.Warnings = append(result.Warnings, normalize.Warning{Code: "invalid_normalized_record"})
			continue
		}
		result.Observations = append(result.Observations, observation)
	}
	return result, nil
}

// cacheWrite maps the combined cache-creation counter and its optional
// lifetime object to the normalized total and split. A record with only the
// combined counter keeps a nil split: its time-to-live is unknown. consistent
// is false when the lifetimes exceed the combined counter; the combined value
// is then kept alone so an unreliable split cannot inflate the total.
func cacheWrite(combined *uint64, split *cacheCreation) (total, fiveMinute, oneHour *uint64, consistent bool) {
	if split == nil || split.Ephemeral5m == nil && split.Ephemeral1h == nil {
		if combined != nil && *combined == 0 {
			// Nothing was written, so both lifetimes are known to be zero.
			return combined, new(uint64), new(uint64), true
		}
		return combined, nil, nil, true
	}
	var sum uint64
	for _, lifetime := range []*uint64{split.Ephemeral5m, split.Ephemeral1h} {
		if lifetime == nil {
			continue
		}
		if math.MaxUint64-sum < *lifetime {
			return combined, nil, nil, false
		}
		sum += *lifetime
	}
	if combined == nil {
		// The total is then the sum of the lifetimes the record does report.
		return &sum, split.Ephemeral5m, split.Ephemeral1h, true
	}
	if sum > *combined {
		return combined, nil, nil, false
	}
	return combined, split.Ephemeral5m, split.Ephemeral1h, true
}

// serviceTier maps the two fields Claude records about how a request was
// processed. service_tier names the tier; speed is a later addition that marks
// fast mode on the standard tier. A record without speed predates fast mode
// and is taken at its service_tier. Any other combination, and any value this
// adapter does not know, proves nothing and stays unknown.
func serviceTier(tier, speed string) normalize.ServiceTier {
	fast := speed == "fast"
	if !fast && speed != "" && speed != "standard" {
		return normalize.ServiceTierUnknown
	}
	switch {
	case tier == "standard" && fast:
		return normalize.ServiceTierFast
	case tier == "standard":
		return normalize.ServiceTierStandard
	case tier == "priority" && !fast:
		return normalize.ServiceTierPriority
	case tier == "batch" && !fast:
		return normalize.ServiceTierBatch
	default:
		return normalize.ServiceTierUnknown
	}
}

// promptTokens is the size of the request's whole prompt: uncached input plus
// everything read from or written to the cache. It is unknown unless the
// record reports all three, because a missing counter would understate it.
func promptTokens(tokens normalize.TokenCounts) *uint64 {
	var prompt uint64
	for _, part := range []*uint64{tokens.InputUncached, tokens.InputCacheRead, tokens.InputCacheWrite} {
		if part == nil || math.MaxUint64-prompt < *part {
			return nil
		}
		prompt += *part
	}
	return &prompt
}

func recordID(parsed record, ordinal int) string {
	// Provider identifiers are stable enough for deduplication but are hashed so
	// normalized state never retains a raw source identifier.
	switch {
	case parsed.RequestID != "":
		return "claude-request-" + safeIdentifier(parsed.RequestID)
	case parsed.Message.ID != "":
		return "claude-message-" + safeIdentifier(parsed.Message.ID)
	default:
		// Cache-write lifetimes are deliberately absent: adding them would
		// change the identity of records already stored and count them twice.
		parts := []string{
			parsed.SessionID, parsed.Timestamp.UTC().Format(time.RFC3339Nano), parsed.Message.Model,
			pointerValue(parsed.Message.Usage.InputTokens), pointerValue(parsed.Message.Usage.CacheReadTokens),
			pointerValue(parsed.Message.Usage.CacheCreationTokens), pointerValue(parsed.Message.Usage.OutputTokens), fmt.Sprint(ordinal),
		}
		return "claude-fallback-" + digest(strings.Join(parts, "\x00"))
	}
}

func safeIdentifier(value string) string {
	return digest(value)
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
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

func anyKnown(tokens normalize.TokenCounts) bool {
	return tokens.InputUncached != nil || tokens.InputCacheRead != nil || tokens.InputCacheWrite != nil || tokens.Output != nil
}
