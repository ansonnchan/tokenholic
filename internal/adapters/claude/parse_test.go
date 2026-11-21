package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ansonnchan/tokenholic/internal/normalize"
)

func TestParseCompatibilityFixtures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		file         string
		surface      normalize.Surface
		wantRecords  int
		wantWarnings int
	}{
		{name: "CLI", file: "claude-code/basic.jsonl", surface: normalize.SurfaceCLI, wantRecords: 2},
		{name: "desktop code", file: "claude-desktop/code/basic.jsonl", surface: normalize.SurfaceDesktopCode, wantRecords: 1},
		{name: "desktop agent", file: "claude-desktop/agent/basic.jsonl", surface: normalize.SurfaceDesktopAgent, wantRecords: 1},
		{name: "desktop cowork", file: "claude-desktop/cowork/basic.jsonl", surface: normalize.SurfaceDesktopCowork, wantRecords: 1},
		{name: "malformed recovery", file: "claude-code/malformed-middle.jsonl", surface: normalize.SurfaceCLI, wantRecords: 2, wantWarnings: 1},
		{name: "cache-write lifetimes", file: "claude-code/cache-write-lifetimes.jsonl", surface: normalize.SurfaceCLI, wantRecords: 6, wantWarnings: 1},
		{name: "local notices", file: "claude-code/local-notices.jsonl", surface: normalize.SurfaceCLI, wantRecords: 2},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			file, err := os.Open(filepath.Join("..", "..", "..", "fixtures", tt.file))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			result, err := Parse(file, tt.surface, "synthetic-v1")
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Observations) != tt.wantRecords || len(result.Warnings) != tt.wantWarnings {
				t.Fatalf("Parse() records/warnings = %d/%d, want %d/%d", len(result.Observations), len(result.Warnings), tt.wantRecords, tt.wantWarnings)
			}
		})
	}
}

func TestParseDefersPartialTail(t *testing.T) {
	t.Parallel()

	data := `{"type":"assistant","sessionId":"synthetic","requestId":"one","timestamp":"2026-01-02T03:04:05Z","message":{"model":"claude-synthetic","usage":{"input_tokens":1}}}`
	result, err := Parse(strings.NewReader(data), normalize.SurfaceCLI, "synthetic-v1")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Observations) != 0 || len(result.Warnings) != 1 || result.Warnings[0].Code != "partial_tail" {
		t.Fatalf("Parse() = %#v", result)
	}
}

func TestParseCacheWriteLifetimesMatchHandComputedValues(t *testing.T) {
	t.Parallel()

	result := parseFixture(t, "claude-code/cache-write-lifetimes.jsonl")
	// Each row is one fixture line in order: total, 5m, 1h, and the portion
	// whose time-to-live the record does not report. -1 means unavailable.
	want := []struct {
		name                         string
		total, fiveMinute, oneHour   int64
		unknownTTL                   uint64
		wantInconsistentSplitWarning bool
	}{
		{name: "split equals combined", total: 300, fiveMinute: 100, oneHour: 200},
		{name: "combined only", total: 40, fiveMinute: -1, oneHour: -1, unknownTTL: 40},
		{name: "split without combined", total: 7, fiveMinute: 7, oneHour: 0},
		{name: "zero combined", total: 0, fiveMinute: 0, oneHour: 0},
		{name: "one lifetime reported", total: 50, fiveMinute: -1, oneHour: 30, unknownTTL: 20},
		{name: "split exceeds combined", total: 10, fiveMinute: -1, oneHour: -1, unknownTTL: 10, wantInconsistentSplitWarning: true},
	}
	if len(result.Observations) != len(want) {
		t.Fatalf("observations = %d, want %d", len(result.Observations), len(want))
	}
	var total, fiveMinute, oneHour, unknownTTL uint64
	for index, tt := range want {
		tokens := result.Observations[index].Tokens
		if got := optional(tokens.InputCacheWrite); got != tt.total {
			t.Errorf("%s: cache write = %d, want %d", tt.name, got, tt.total)
		}
		if got := optional(tokens.InputCacheWrite5m); got != tt.fiveMinute {
			t.Errorf("%s: 5m cache write = %d, want %d", tt.name, got, tt.fiveMinute)
		}
		if got := optional(tokens.InputCacheWrite1h); got != tt.oneHour {
			t.Errorf("%s: 1h cache write = %d, want %d", tt.name, got, tt.oneHour)
		}
		remainder := tokens.CacheWriteUnknownTTL()
		if remainder == nil || *remainder != tt.unknownTTL {
			t.Errorf("%s: unknown time-to-live = %v, want %d", tt.name, remainder, tt.unknownTTL)
		}
		if got := hasWarning(result.Observations[index].Warnings, "cache_write_split_inconsistent"); got != tt.wantInconsistentSplitWarning {
			t.Errorf("%s: inconsistent-split warning = %t, want %t", tt.name, got, tt.wantInconsistentSplitWarning)
		}
		total += *tokens.InputCacheWrite
		unknownTTL += *remainder
		if tokens.InputCacheWrite5m != nil {
			fiveMinute += *tokens.InputCacheWrite5m
		}
		if tokens.InputCacheWrite1h != nil {
			oneHour += *tokens.InputCacheWrite1h
		}
	}
	if total != 407 || fiveMinute != 107 || oneHour != 230 || unknownTTL != 70 {
		t.Fatalf("cache-write totals = %d/%d/%d/%d, want 407/107/230/70", total, fiveMinute, oneHour, unknownTTL)
	}
	if got := knownTotal(t, result.Observations); got != 482 {
		t.Fatalf("total tokens = %d, want 482", got)
	}
}

func TestParseExcludesLocalNotices(t *testing.T) {
	t.Parallel()

	result := parseFixture(t, "claude-code/local-notices.jsonl")
	if len(result.Warnings) != 0 {
		t.Fatalf("warnings = %#v, want none", result.Warnings)
	}
	for _, observation := range result.Observations {
		if observation.ObservedModelID != "claude-sonnet-synthetic" {
			t.Fatalf("observed model = %q, want only the real model", observation.ObservedModelID)
		}
	}
	// Lines 1 and 5 are usage; the synthetic model and both error notices,
	// including one that names a real model with non-zero counts, are not.
	if got := knownTotal(t, result.Observations); got != 12+3+8+6+4 {
		t.Fatalf("total tokens = %d, want 33", got)
	}
}

func TestCacheWriteMapping(t *testing.T) {
	t.Parallel()

	value := func(v uint64) *uint64 { return &v }
	maximum := value(^uint64(0))
	tests := []struct {
		name                       string
		combined                   *uint64
		split                      *cacheCreation
		total, fiveMinute, oneHour int64
		consistent                 bool
	}{
		{name: "nothing reported", total: -1, fiveMinute: -1, oneHour: -1, consistent: true},
		{name: "empty split object", combined: value(9), split: &cacheCreation{}, total: 9, fiveMinute: -1, oneHour: -1, consistent: true},
		{name: "split below combined", combined: value(9), split: &cacheCreation{Ephemeral5m: value(2), Ephemeral1h: value(3)}, total: 9, fiveMinute: 2, oneHour: 3, consistent: true},
		{name: "split overflow", combined: value(9), split: &cacheCreation{Ephemeral5m: maximum, Ephemeral1h: value(1)}, total: 9, fiveMinute: -1, oneHour: -1},
		{name: "one lifetime without combined", split: &cacheCreation{Ephemeral1h: value(4)}, total: 4, fiveMinute: -1, oneHour: 4, consistent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total, fiveMinute, oneHour, consistent := cacheWrite(tt.combined, tt.split)
			if optional(total) != tt.total || optional(fiveMinute) != tt.fiveMinute || optional(oneHour) != tt.oneHour || consistent != tt.consistent {
				t.Fatalf("cacheWrite() = %d/%d/%d/%t, want %d/%d/%d/%t",
					optional(total), optional(fiveMinute), optional(oneHour), consistent, tt.total, tt.fiveMinute, tt.oneHour, tt.consistent)
			}
		})
	}
}

func parseFixture(t *testing.T, name string) Result {
	t.Helper()
	file, err := os.Open(filepath.Join("..", "..", "..", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	result, err := Parse(file, normalize.SurfaceCLI, "synthetic-v1")
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func knownTotal(t *testing.T, observations []normalize.Observation) uint64 {
	t.Helper()
	var sum uint64
	for _, observation := range observations {
		total, err := observation.Tokens.KnownTotal()
		if err != nil || total == nil {
			t.Fatalf("KnownTotal() = %v, %v", total, err)
		}
		sum += *total
	}
	return sum
}

// optional renders an unavailable category as -1 so tables can tell it from zero.
func optional(value *uint64) int64 {
	if value == nil {
		return -1
	}
	return int64(*value)
}

func hasWarning(warnings []normalize.Warning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

func TestParseReadsServiceTierAndPromptSize(t *testing.T) {
	t.Parallel()

	file, err := os.Open(filepath.Join("..", "..", "..", "fixtures", "claude-code", "service-tiers.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	result, err := Parse(file, normalize.SurfaceCLI, "synthetic-v1")
	if err != nil {
		t.Fatal(err)
	}
	prompt := func(v uint64) *uint64 { return &v }
	// Each prompt is input + cache read + cache write: 10 + 20 + 30.
	want := []struct {
		reason string
		tier   normalize.ServiceTier
		prompt *uint64
	}{
		{"standard tier at standard speed", normalize.ServiceTierStandard, prompt(60)},
		{"a record without speed is taken at its tier", normalize.ServiceTierStandard, prompt(60)},
		{"fast mode on the standard tier", normalize.ServiceTierFast, prompt(60)},
		{"priority", normalize.ServiceTierPriority, prompt(60)},
		{"batch", normalize.ServiceTierBatch, prompt(60)},
		{"neither field", normalize.ServiceTierUnknown, prompt(60)},
		{"an unrecognized speed proves nothing", normalize.ServiceTierUnknown, prompt(60)},
		{"an unrecognized tier proves nothing", normalize.ServiceTierUnknown, prompt(60)},
		{"cached input counts toward the prompt", normalize.ServiceTierStandard, prompt(100_001)},
		{"a missing cache counter leaves the prompt size unknown", normalize.ServiceTierStandard, nil},
	}
	if len(result.Observations) != len(want) || len(result.Warnings) != 0 {
		t.Fatalf("Parse() records/warnings = %d/%d, want %d/0", len(result.Observations), len(result.Warnings), len(want))
	}
	for index, observation := range result.Observations {
		if observation.ServiceTier != want[index].tier {
			t.Errorf("record %d (%s): tier = %q, want %q", index, want[index].reason, observation.ServiceTier, want[index].tier)
		}
		got, expected := observation.PromptTokens, want[index].prompt
		if (got == nil) != (expected == nil) || got != nil && *got != *expected {
			t.Errorf("record %d (%s): prompt = %v, want %v", index, want[index].reason, got, expected)
		}
	}
	if band := normalize.PromptBandFor(*result.Observations[8].PromptTokens); band != normalize.PromptBandUpTo200K {
		t.Fatalf("band = %q, want the band above 100K", band)
	}
}
