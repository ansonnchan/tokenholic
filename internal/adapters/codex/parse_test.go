package codex

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
		// Legacy rollouts carry only running totals; current ones report
		// each request's usage.
		wantSemantics normalize.CountSemantics
	}{
		{name: "CLI cumulative", file: "codex-cli/cumulative.jsonl", surface: normalize.SurfaceCLI, wantRecords: 3, wantSemantics: normalize.CountCumulative},
		{name: "CLI reset", file: "codex-cli/counter-reset.jsonl", surface: normalize.SurfaceCLI, wantRecords: 2, wantSemantics: normalize.CountCumulative},
		{name: "desktop", file: "codex-desktop/basic.jsonl", surface: normalize.SurfaceDesktop, wantRecords: 1, wantSemantics: normalize.CountCumulative},
		{name: "malformed recovery", file: "codex-cli/malformed-middle.jsonl", surface: normalize.SurfaceCLI, wantRecords: 1, wantWarnings: 1, wantSemantics: normalize.CountCumulative},
		// Three requests; two events repeat an unchanged running total.
		{name: "per-request parent", file: "codex-cli/fork-parent.jsonl", surface: normalize.SurfaceCLI, wantRecords: 3, wantSemantics: normalize.CountIncremental},
		// Two replayed requests plus two of the fork's own.
		{name: "per-request fork", file: "codex-cli/fork-child.jsonl", surface: normalize.SurfaceCLI, wantRecords: 4, wantSemantics: normalize.CountIncremental},
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
			result, err := Parse(file, tt.surface, "synthetic-v1", "synthetic-file")
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Observations) != tt.wantRecords || len(result.Warnings) != tt.wantWarnings {
				t.Fatalf("Parse() records/warnings = %d/%d, want %d/%d", len(result.Observations), len(result.Warnings), tt.wantRecords, tt.wantWarnings)
			}
			for _, observation := range result.Observations {
				if observation.CountSemantics != tt.wantSemantics {
					t.Fatalf("count semantics = %q, want %q", observation.CountSemantics, tt.wantSemantics)
				}
			}
		})
	}
}

func TestParseRetainsTokensWithUnknownModel(t *testing.T) {
	t.Parallel()

	data := `{"timestamp":"2026-01-04T03:01:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"cached_input_tokens":2,"output_tokens":3}}}}` + "\n"
	result, err := Parse(strings.NewReader(data), normalize.SurfaceCLI, "synthetic-v1", "synthetic-file")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Observations) != 1 || result.Observations[0].ModelResolution != normalize.ModelUnknown || result.Observations[0].Tokens.Output == nil {
		t.Fatalf("Parse() = %#v", result)
	}
}

func TestParseIdentifiesAReplayedRequestAsItsOriginal(t *testing.T) {
	t.Parallel()

	parent := parseCodexFixture(t, "codex-cli/fork-parent.jsonl", "logical-parent")
	child := parseCodexFixture(t, "codex-cli/fork-child.jsonl", "logical-child")
	// The fork's first two observations are the parent's first two requests,
	// read from another file at another time.
	for index := 0; index < 2; index++ {
		original, replay := parent.Observations[index], child.Observations[index]
		if original.SourceRecordID != replay.SourceRecordID {
			t.Fatalf("request %d: replay identity %q differs from its original %q", index, replay.SourceRecordID, original.SourceRecordID)
		}
		if original.OccurredAt.Equal(replay.OccurredAt) {
			t.Fatalf("request %d: fixture replay kept the original timestamp, so the test proves nothing", index)
		}
	}
	// The fork continues the parent's counter from the same point the parent
	// does, so their next requests must stay distinct.
	seen := map[string]bool{}
	for _, observation := range append(parent.Observations[2:], child.Observations[2:]...) {
		if seen[observation.SourceRecordID] {
			t.Fatalf("separate requests share identity %q", observation.SourceRecordID)
		}
		seen[observation.SourceRecordID] = true
	}
	for _, observation := range child.Observations {
		if observation.CountSemantics != normalize.CountIncremental || observation.Measurement != normalize.MeasurementReported {
			t.Fatalf("per-request observation = %q/%q", observation.CountSemantics, observation.Measurement)
		}
		// Both threads belong to one session; the fork's parent header must
		// not rename it, and no raw identifier is exposed in the record ID.
		if observation.SessionIDLocal == nil || *observation.SessionIDLocal != "synthetic-codex-root" || strings.Contains(observation.SourceRecordID, "synthetic") {
			t.Fatalf("session/identity = %v/%q", observation.SessionIDLocal, observation.SourceRecordID)
		}
	}
	// Request usage, not the running total: 150 input of which 140 cached.
	first := child.Observations[2].Tokens
	if *first.InputUncached != 10 || *first.InputCacheRead != 140 || *first.Output != 10 || *first.ReasoningOutput != 3 {
		t.Fatalf("fork request tokens = %d/%d/%d/%d, want 10/140/10/3", *first.InputUncached, *first.InputCacheRead, *first.Output, *first.ReasoningOutput)
	}
}

func TestParseTokenEventShapes(t *testing.T) {
	t.Parallel()

	const header = `{"timestamp":"2026-01-08T09:00:00Z","type":"session_meta","payload":{"id":"synthetic-thread","session_id":"synthetic-session"}}` + "\n" +
		`{"timestamp":"2026-01-08T09:00:00Z","type":"turn_context","payload":{"model":"gpt-synthetic","turn_id":"synthetic-turn"}}` + "\n"
	event := func(second int, info string) string {
		return `{"timestamp":"2026-01-08T09:00:0` + string(rune('0'+second)) + `Z","type":"event_msg","payload":{"type":"token_count","info":` + info + `}}` + "\n"
	}
	tests := []struct {
		name          string
		data          string
		wantSemantics []normalize.CountSemantics
		wantWarning   string
	}{
		{
			name: "request usage without a running total is kept apart by time",
			data: header + event(1, `{"last_token_usage":{"input_tokens":10,"output_tokens":1}}`) +
				event(2, `{"last_token_usage":{"input_tokens":10,"output_tokens":1}}`),
			wantSemantics: []normalize.CountSemantics{normalize.CountIncremental, normalize.CountIncremental},
		},
		{
			name: "running total alone is a cumulative snapshot",
			data: header + event(1, `{"total_token_usage":{"input_tokens":10,"output_tokens":1}}`) +
				event(2, `{"total_token_usage":{"input_tokens":25,"output_tokens":3}}`),
			wantSemantics: []normalize.CountSemantics{normalize.CountCumulative, normalize.CountCumulative},
		},
		{
			name: "an unchanged running total reports no new request",
			data: header + event(1, `{"total_token_usage":{"input_tokens":10,"output_tokens":1},"last_token_usage":{"input_tokens":10,"output_tokens":1}}`) +
				event(2, `{"total_token_usage":{"input_tokens":10,"output_tokens":1},"last_token_usage":{"input_tokens":10,"output_tokens":1}}`),
			wantSemantics: []normalize.CountSemantics{normalize.CountIncremental},
		},
		{
			name: "a zero counter is not an absent counter",
			data: header + event(1, `{"total_token_usage":{"input_tokens":10,"output_tokens":0},"last_token_usage":{"input_tokens":10,"output_tokens":0}}`) +
				event(2, `{"total_token_usage":{"input_tokens":10},"last_token_usage":{"input_tokens":10}}`),
			wantSemantics: []normalize.CountSemantics{normalize.CountIncremental, normalize.CountIncremental},
		},
		{
			name:        "cached input above total input is rejected",
			data:        header + event(1, `{"total_token_usage":{"input_tokens":5,"cached_input_tokens":9},"last_token_usage":{"input_tokens":5,"cached_input_tokens":9}}`),
			wantWarning: "invalid_token_relationship",
		},
		{name: "an event without usage is unsupported", data: header + event(1, `{}`), wantWarning: "unsupported_record"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := Parse(strings.NewReader(tt.data), normalize.SurfaceCLI, "synthetic-v1", "logical-file")
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Observations) != len(tt.wantSemantics) {
				t.Fatalf("observations = %d, want %d (%#v)", len(result.Observations), len(tt.wantSemantics), result.Warnings)
			}
			identities := map[string]bool{}
			for index, observation := range result.Observations {
				if observation.CountSemantics != tt.wantSemantics[index] {
					t.Fatalf("observation %d semantics = %q, want %q", index, observation.CountSemantics, tt.wantSemantics[index])
				}
				if identities[observation.SourceRecordID] {
					t.Fatalf("observation %d repeats identity %q", index, observation.SourceRecordID)
				}
				identities[observation.SourceRecordID] = true
			}
			if tt.wantWarning == "" && len(result.Warnings) != 0 {
				t.Fatalf("warnings = %#v, want none", result.Warnings)
			}
			if tt.wantWarning != "" && (len(result.Warnings) != 1 || result.Warnings[0].Code != tt.wantWarning) {
				t.Fatalf("warnings = %#v, want %q", result.Warnings, tt.wantWarning)
			}
		})
	}
}

func parseCodexFixture(t *testing.T, name, logicalFileID string) Result {
	t.Helper()
	file, err := os.Open(filepath.Join("..", "..", "..", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	result, err := Parse(file, normalize.SurfaceCLI, "synthetic-v1", logicalFileID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("warnings = %#v, want none", result.Warnings)
	}
	return result
}

// The fixture walks one thread through every way Codex writes its service
// tier. Prompt sizes are each request's whole input, cached part included.
func TestParseFollowsServiceTierAndPromptSize(t *testing.T) {
	t.Parallel()

	result := parseCodexFixture(t, "codex-cli/service-tiers.jsonl", "logical-tiers")
	want := []struct {
		reason string
		tier   normalize.ServiceTier
		prompt uint64
		band   normalize.PromptBand
	}{
		{"no settings event before the first turn", normalize.ServiceTierUnknown, 100, normalize.PromptBandUpTo100K},
		{"priority", normalize.ServiceTierPriority, 200, normalize.PromptBandUpTo100K},
		{"a settings event without a tier keeps the previous one", normalize.ServiceTierPriority, 300, normalize.PromptBandUpTo100K},
		{"default is the standard tier", normalize.ServiceTierStandard, 280_000, normalize.PromptBandOver272K},
		{"fast is the earlier spelling of priority", normalize.ServiceTierPriority, 100_001, normalize.PromptBandUpTo200K},
		{"flex, exactly on the 272K limit", normalize.ServiceTierFlex, 272_000, normalize.PromptBandUpTo272K},
		{"Codex Desktop writes standard", normalize.ServiceTierStandard, 500, normalize.PromptBandUpTo100K},
		{"an unrecognized tier clears the previous one", normalize.ServiceTierUnknown, 600, normalize.PromptBandUpTo100K},
		{"nothing to keep after a cleared tier", normalize.ServiceTierUnknown, 700, normalize.PromptBandUpTo100K},
		{"an explicit null is not an omitted tier", normalize.ServiceTierUnknown, 800, normalize.PromptBandUpTo100K},
	}
	if len(result.Observations) != len(want) {
		t.Fatalf("observations = %d, want %d", len(result.Observations), len(want))
	}
	for index, observation := range result.Observations {
		if observation.ServiceTier != want[index].tier {
			t.Errorf("request %d (%s): tier = %q, want %q", index, want[index].reason, observation.ServiceTier, want[index].tier)
		}
		if observation.PromptTokens == nil || *observation.PromptTokens != want[index].prompt {
			t.Errorf("request %d (%s): prompt = %v, want %d", index, want[index].reason, observation.PromptTokens, want[index].prompt)
			continue
		}
		if band := normalize.PromptBandFor(*observation.PromptTokens); band != want[index].band {
			t.Errorf("request %d (%s): band = %q, want %q", index, want[index].reason, band, want[index].band)
		}
	}
}

// A running total spans many requests, so a legacy rollout proves neither a
// tier nor a prompt size even when a settings event precedes it.
func TestParseLeavesLegacySnapshotsWithoutPricingEvidence(t *testing.T) {
	t.Parallel()

	data := `{"timestamp":"2026-01-08T09:00:00Z","type":"turn_context","payload":{"model":"gpt-synthetic","turn_id":"synthetic-turn"}}` + "\n" +
		`{"timestamp":"2026-01-08T09:00:01Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"service_tier":"priority"}}}` + "\n" +
		`{"timestamp":"2026-01-08T09:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"output_tokens":1}}}}` + "\n"
	result, err := Parse(strings.NewReader(data), normalize.SurfaceCLI, "synthetic-v1", "logical-file")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Observations) != 1 || len(result.Warnings) != 0 {
		t.Fatalf("Parse() = %#v", result)
	}
	if got := result.Observations[0]; got.ServiceTier != normalize.ServiceTierUnknown || got.PromptTokens != nil {
		t.Fatalf("legacy snapshot evidence = %q/%v, want none", got.ServiceTier, got.PromptTokens)
	}
}
