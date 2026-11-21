package collect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/ansonnchan/tokenholic/internal/adapters"
	"github.com/ansonnchan/tokenholic/internal/adapters/claude"
	"github.com/ansonnchan/tokenholic/internal/adapters/codex"
	"github.com/ansonnchan/tokenholic/internal/normalize"
)

// Result contains deterministic normalized observations and safe warning codes.
type Result struct {
	Observations []normalize.Observation
	Warnings     []normalize.Warning
	SourceIssues []SourceIssue
}

// SourceIssue attributes a safe collection warning without retaining a path.
type SourceIssue struct {
	Source  normalize.Source
	Surface normalize.Surface
	Warning normalize.Warning
}

// Collector reads discovered local candidates using their provider adapters.
type Collector struct {
	SourceVersion string
}

// Collect parses candidates, deduplicates raw records, and reduces cumulative
// snapshots. It returns warnings instead of aborting the entire scan when one
// candidate is unreadable or malformed.
func (c Collector) Collect(ctx context.Context, candidates []adapters.Candidate) Result {
	version := c.SourceVersion
	if version == "" {
		version = "local-jsonl"
	}
	result := Result{}
	rawObservations := []normalize.Observation(nil)
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			result.Warnings = append(result.Warnings, normalize.Warning{Code: "collection_canceled"})
			break
		}
		if !strings.EqualFold(filepath.Ext(candidate.Path), ".jsonl") {
			result.addIssue(candidate, normalize.Warning{Code: "unsupported_record_container"})
			continue
		}
		file, err := os.Open(candidate.Path)
		if err != nil {
			result.addIssue(candidate, normalize.Warning{Code: "source_file_unreadable"})
			continue
		}
		switch candidate.Source {
		case normalize.SourceClaude:
			parsed, parseErr := claude.Parse(file, candidate.Surface, version)
			rawObservations = append(rawObservations, parsed.Observations...)
			for _, warning := range parsed.Warnings {
				result.addIssue(candidate, warning)
			}
			if parseErr != nil {
				result.addIssue(candidate, normalize.Warning{Code: "source_parse_failed"})
			}
		case normalize.SourceCodex:
			parsed, parseErr := codex.Parse(file, candidate.Surface, version, logicalFileID(candidate))
			rawObservations = append(rawObservations, parsed.Observations...)
			for _, warning := range parsed.Warnings {
				result.addIssue(candidate, warning)
			}
			if parseErr != nil {
				result.addIssue(candidate, normalize.Warning{Code: "source_parse_failed"})
			}
		default:
			result.addIssue(candidate, normalize.Warning{Code: "unsupported_source"})
		}
		_ = file.Close()
	}
	// Overlapping roots must collapse before snapshots become deltas; reversing
	// this order would count each copy as an independent cumulative stream.
	rawObservations, result.Warnings = Deduplicate(rawObservations, result.Warnings)
	result.Observations, result.Warnings = incrementalizeWithWarnings(rawObservations, result.Warnings)
	sort.SliceStable(result.Warnings, func(i, j int) bool {
		if result.Warnings[i].Code != result.Warnings[j].Code {
			return result.Warnings[i].Code < result.Warnings[j].Code
		}
		return result.Warnings[i].Message < result.Warnings[j].Message
	})
	sort.SliceStable(result.SourceIssues, func(i, j int) bool {
		left, right := result.SourceIssues[i], result.SourceIssues[j]
		if left.Source != right.Source {
			return left.Source < right.Source
		}
		if left.Surface != right.Surface {
			return left.Surface < right.Surface
		}
		if left.Warning.Code != right.Warning.Code {
			return left.Warning.Code < right.Warning.Code
		}
		return left.Warning.Message < right.Warning.Message
	})
	return result
}

func (r *Result) addIssue(candidate adapters.Candidate, warning normalize.Warning) {
	r.Warnings = append(r.Warnings, warning)
	r.SourceIssues = append(r.SourceIssues, SourceIssue{
		Source: candidate.Source, Surface: candidate.Surface, Warning: warning,
	})
}

func incrementalizeWithWarnings(observations []normalize.Observation, warnings []normalize.Warning) ([]normalize.Observation, []normalize.Warning) {
	incremental, cumulativeWarnings := Incrementalize(observations)
	return incremental, append(warnings, cumulativeWarnings...)
}

func logicalFileID(candidate adapters.Candidate) string {
	// Relative identity is stable across equivalent roots. The absolute-path
	// fallback is hashed immediately and remains local to cumulative-state isolation.
	identity := candidate.RelativePath
	if identity == "" {
		identity = candidate.Path
	}
	sum := sha256.Sum256([]byte(identity))
	return "logical-file-" + hex.EncodeToString(sum[:16])
}

// Deduplicate collapses source identities and chooses a deterministic winner for
// conflicting copies. The key and tie-breakers exclude raw content and paths.
func Deduplicate(observations []normalize.Observation, warnings []normalize.Warning) ([]normalize.Observation, []normalize.Warning) {
	byID := make(map[string]normalize.Observation, len(observations))
	for _, observation := range observations {
		key := string(observation.Source) + "\x00" + observation.SourceRecordID
		existing, found := byID[key]
		if !found {
			byID[key] = observation
			continue
		}
		if !existing.OccurredAt.Equal(observation.OccurredAt) && sameUsageAtAnotherTime(existing, observation) {
			// A replayed or re-read copy of the same usage is not a conflict.
			// The event happened when it was first recorded, so the earlier
			// copy wins whatever order the files were read in.
			if observation.OccurredAt.Before(existing.OccurredAt) {
				byID[key] = observation
			}
			continue
		}
		winner := preferred(existing, observation)
		if !sameUsage(existing, observation) {
			warning := normalize.Warning{Code: "duplicate_conflict"}
			winner.Warnings = append(winner.Warnings, warning)
			warnings = append(warnings, warning)
		}
		byID[key] = winner
	}
	result := make([]normalize.Observation, 0, len(byID))
	for _, observation := range byID {
		result = append(result, observation)
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].OccurredAt.Equal(result[j].OccurredAt) {
			return result[i].OccurredAt.Before(result[j].OccurredAt)
		}
		if result[i].Source != result[j].Source {
			return result[i].Source < result[j].Source
		}
		// Running totals only grow, so ascending magnitude restores the source
		// order of snapshots written under one timestamp. Ordering them by
		// record identity would look like counter resets to the reducer.
		if result[i].CountSemantics == normalize.CountCumulative && result[j].CountSemantics == normalize.CountCumulative {
			_, left := usageQuality(result[i].Tokens)
			_, right := usageQuality(result[j].Tokens)
			if left != right {
				return left < right
			}
		}
		return result[i].SourceRecordID < result[j].SourceRecordID
	})
	return result, warnings
}

// sameUsageAtAnotherTime reports copies that agree on everything except when
// they were recorded.
func sameUsageAtAnotherTime(a, b normalize.Observation) bool {
	return a.Source == b.Source && a.SourceRecordID == b.SourceRecordID &&
		a.ObservedModelID == b.ObservedModelID && a.ModelResolution == b.ModelResolution &&
		a.CountSemantics == b.CountSemantics && a.Measurement == b.Measurement && reflect.DeepEqual(a.Tokens, b.Tokens)
}

func sameUsage(a, b normalize.Observation) bool {
	return a.Source == b.Source && a.SourceRecordID == b.SourceRecordID && a.OccurredAt.Equal(b.OccurredAt) &&
		a.ObservedModelID == b.ObservedModelID && a.ModelResolution == b.ModelResolution &&
		a.CountSemantics == b.CountSemantics && a.Measurement == b.Measurement && reflect.DeepEqual(a.Tokens, b.Tokens)
}

func preferred(a, b normalize.Observation) normalize.Observation {
	// Prefer completeness and magnitude before timestamps so a later partial copy
	// cannot displace a more informative record of the same source identity.
	aKnown, aTotal := usageQuality(a.Tokens)
	bKnown, bTotal := usageQuality(b.Tokens)
	if aKnown != bKnown {
		if aKnown > bKnown {
			return a
		}
		return b
	}
	if aTotal != bTotal {
		if aTotal > bTotal {
			return a
		}
		return b
	}
	if !a.OccurredAt.Equal(b.OccurredAt) {
		if a.OccurredAt.After(b.OccurredAt) {
			return a
		}
		return b
	}
	aRank, bRank := surfaceRank(a.Surface), surfaceRank(b.Surface)
	if aRank != bRank {
		if aRank < bRank {
			return a
		}
		return b
	}
	if safeSignature(a) <= safeSignature(b) {
		return a
	}
	return b
}

func usageQuality(tokens normalize.TokenCounts) (int, uint64) {
	known := 0
	var total uint64
	for _, value := range []*uint64{tokens.InputUncached, tokens.InputCacheRead, tokens.InputCacheWrite, tokens.Output} {
		if value == nil {
			continue
		}
		known++
		if math.MaxUint64-total < *value {
			total = math.MaxUint64
			continue
		}
		total += *value
	}
	// A reported cache-write lifetime makes a copy more complete without
	// changing its total, which already includes every lifetime.
	for _, value := range []*uint64{tokens.InputCacheWrite5m, tokens.InputCacheWrite1h} {
		if value != nil {
			known++
		}
	}
	return known, total
}

func surfaceRank(surface normalize.Surface) int {
	// Surface rank is only a deterministic duplicate tie-breaker. It does not
	// claim that one product surface is more accurate than another.
	switch surface {
	case normalize.SurfaceDesktopCode:
		return 0
	case normalize.SurfaceDesktopAgent:
		return 1
	case normalize.SurfaceDesktopCowork:
		return 2
	case normalize.SurfaceSharedLocal:
		return 3
	case normalize.SurfaceDesktop:
		return 4
	case normalize.SurfaceCLI:
		return 5
	default:
		return 6
	}
}

func safeSignature(observation normalize.Observation) string {
	// This final tie-breaker contains only normalized usage metadata. Never add a
	// path, session identifier, or raw-record fragment here.
	return strings.Join([]string{
		string(observation.Surface), observation.ObservedModelID, tokenValue(observation.Tokens.InputUncached),
		tokenValue(observation.Tokens.InputCacheRead), tokenValue(observation.Tokens.InputCacheWrite),
		tokenValue(observation.Tokens.InputCacheWrite5m), tokenValue(observation.Tokens.InputCacheWrite1h),
		tokenValue(observation.Tokens.Output), tokenValue(observation.Tokens.ReasoningOutput),
		string(observation.ServiceTier), tokenValue(observation.PromptTokens),
	}, "\x00")
}

func tokenValue(value *uint64) string {
	if value == nil {
		return "null"
	}
	return fmt.Sprint(*value)
}
