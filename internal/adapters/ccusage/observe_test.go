package ccusage

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/normalize"
)

// show renders a count the way the expectations below are written: a number
// when known, "-" when the agent's records cannot say.
func show(value *uint64) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprint(*value)
}

func describe(observation normalize.Observation) string {
	model := observation.ObservedModelID
	if model == "" {
		model = "(no model)"
	}
	return fmt.Sprintf("%s %s in=%s read=%s write=%s out=%s",
		observation.OccurredAt.Format(time.DateOnly), model,
		show(observation.Tokens.InputUncached), show(observation.Tokens.InputCacheRead),
		show(observation.Tokens.InputCacheWrite), show(observation.Tokens.Output))
}

// Each agent's fixture is the pinned release's output for invented records.
// The expected day totals are worked out by hand from those records and from
// how ccusage maps them, which the README sets out per agent:
//
//	copilot   input excludes both cache buckets; reasoning is inside output.
//	gemini    input excludes cached tokens and includes tool tokens; no cache
//	          writes exist; thought tokens reach only the day's total.
//	grok      input excludes cache reads and writes; reasoning is inside output.
//	opencode  the four categories as recorded; reasoning reaches only the
//	          day's total.
//	qwen      prompt and cached counts as recorded; no cache writes exist;
//	          thought tokens reach only the day's total.
func TestAgentFixturesBecomeHandComputedDayTotals(t *testing.T) {
	t.Parallel()
	tests := []struct {
		agent   Agent
		fixture string
		source  normalize.Source
		want    []string
		// total is every token ccusage counted in the report.
		total uint64
	}{
		{agent: AgentCopilot, fixture: "copilot-daily.json", source: normalize.SourceCopilot, total: 2680, want: []string{
			// Shutdown 1: 1000 input holding 100 read and 200 written.
			"2026-01-02 claude-synthetic-4.6 in=700 read=100 write=200 out=500",
			"2026-01-02 gpt-synthetic-5 in=400 read=0 write=0 out=80",
			// Shutdown 2 is cumulative: 1500-1000 input, less 50 read and 50 written.
			"2026-01-03 claude-synthetic-4.6 in=400 read=50 write=50 out=200",
		}},
		{agent: AgentGemini, fixture: "gemini-daily.json", source: normalize.SourceGemini, total: 2015, want: []string{
			// 1000 input less 400 cached plus 20 tool; 90 thought tokens.
			"2026-01-02 gemini-synthetic-pro in=620 read=400 write=- out=200",
			// 10 thought tokens.
			"2026-01-02 gemini-synthetic-flash in=100 read=0 write=- out=20",
			// Two models shared the day, so its 100 thought tokens have no model.
			"2026-01-02 (no model) in=0 read=0 write=- out=100",
			// One model: its 25 thought tokens join its 50 output.
			"2026-01-03 gemini-synthetic-pro in=400 read=100 write=- out=75",
		}},
		{agent: AgentGrok, fixture: "grok-daily.json", source: normalize.SourceGrok, total: 1560, want: []string{
			// 1000 input holding 400 read and 100 written; 50 reasoning inside 200 output.
			"2026-01-02 grok-synthetic-build in=500 read=400 write=100 out=200",
			"2026-01-03 grok-synthetic-build in=300 read=0 write=0 out=60",
		}},
		{agent: AgentOpenCode, fixture: "opencode-daily.json", source: normalize.SourceOpenCode, total: 2495, want: []string{
			"2026-01-02 model-synthetic-x in=1000 read=100 write=200 out=500",
			"2026-01-02 model-synthetic-y in=300 read=0 write=0 out=40",
			// 80 reasoning tokens on a day two models shared.
			"2026-01-02 (no model) in=0 read=0 write=0 out=80",
			// One model: its 5 reasoning tokens join its 20 output.
			"2026-01-03 model-synthetic-x in=200 read=50 write=0 out=25",
		}},
		{agent: AgentQwen, fixture: "qwen-daily.json", source: normalize.SourceQwen, total: 2266, want: []string{
			"2026-01-02 qwen-synthetic-a in=1000 read=300 write=- out=200",
			"2026-01-02 qwen-synthetic-b in=500 read=0 write=- out=100",
			// 70 and 30 thought tokens on a day two models shared.
			"2026-01-02 (no model) in=0 read=0 write=- out=100",
			"2026-01-03 qwen-synthetic-a in=50 read=0 write=- out=10",
			// A record that named no model.
			"2026-01-04 (no model) in=5 read=0 write=- out=1",
		}},
	}
	if len(tests) != len(Agents()) {
		t.Fatalf("fixtures cover %d agents, the contract covers %d", len(tests), len(Agents()))
	}
	for _, test := range tests {
		t.Run(string(test.agent), func(t *testing.T) {
			t.Parallel()
			report, err := ParseDailyReport(readFixture(t, test.fixture))
			if err != nil {
				t.Fatal(err)
			}
			profile, found := ProfileFor(test.agent)
			if !found || profile.Source != test.source {
				t.Fatalf("ProfileFor(%s) = %+v, %t", test.agent, profile, found)
			}
			if err := profile.Capabilities.Validate(); err != nil {
				t.Fatal(err)
			}
			observations, err := profile.Observations(report)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(observations))
			identities := make(map[string]struct{})
			for _, observation := range observations {
				got = append(got, describe(observation))
				if observation.Source != test.source || observation.Surface != normalize.SurfaceCLI ||
					observation.CountSemantics != normalize.CountDayTotal || observation.Measurement != normalize.MeasurementDerived ||
					observation.Tokens.ReasoningOutput != nil || observation.SessionIDLocal != nil || observation.PromptTokens != nil {
					t.Errorf("observation = %+v", observation)
				}
				if (observation.ObservedModelID == "") != (observation.ModelResolution == normalize.ModelUnknown) {
					t.Errorf("model %q has resolution %q", observation.ObservedModelID, observation.ModelResolution)
				}
				if _, duplicate := identities[observation.SourceRecordID]; duplicate {
					t.Errorf("identity %q is repeated", observation.SourceRecordID)
				}
				identities[observation.SourceRecordID] = struct{}{}
			}
			if strings.Join(got, "\n") != strings.Join(test.want, "\n") {
				t.Fatalf("day totals:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(test.want, "\n"))
			}
			// Every token ccusage counted is counted once, and no more.
			total, err := Total(observations)
			if err != nil || total != test.total {
				t.Fatalf("Total() = %d, %v; want %d", total, err, test.total)
			}
			var reported uint64
			for _, day := range report.Days {
				reported += day.Total
			}
			if reported != test.total {
				t.Fatalf("the report's own total is %d, the hand-computed one %d", reported, test.total)
			}
		})
	}
}

func TestEmptyReportsYieldNoDayTotals(t *testing.T) {
	t.Parallel()
	for _, fixture := range []string{"empty-daily.json", "qwen-empty-daily.json"} {
		report, err := ParseDailyReport(readFixture(t, fixture))
		if err != nil {
			t.Fatal(err)
		}
		for _, profile := range Profiles() {
			if observations, err := profile.Observations(report); err != nil || len(observations) != 0 {
				t.Fatalf("%s from %s = %d observations, %v", profile.Agent, fixture, len(observations), err)
			}
		}
	}
}

// The identity of a day total is the agent, the day, and the model. A day
// that grew must produce the same identities, or storage would add to the
// day instead of replacing it.
func TestAGrownDayKeepsItsIdentities(t *testing.T) {
	t.Parallel()
	profile, _ := ProfileFor(AgentGemini)
	day := func(input, output, extra uint64) Report {
		return Report{Days: []Day{{
			Date: "2026-01-02", Counts: Counts{Input: input, Output: output}, Total: input + output + extra,
			Models: []ModelUsage{{Model: "model-synthetic", Counts: Counts{Input: input, Output: output}}},
		}}}
	}
	morning, err := profile.Observations(day(100, 10, 5))
	if err != nil {
		t.Fatal(err)
	}
	evening, err := profile.Observations(day(900, 80, 40))
	if err != nil {
		t.Fatal(err)
	}
	if len(morning) != 1 || len(evening) != 1 || morning[0].SourceRecordID != evening[0].SourceRecordID {
		t.Fatalf("identities differ across the day: %+v / %+v", morning, evening)
	}
	if *morning[0].Tokens.Output != 15 || *evening[0].Tokens.Output != 120 || *evening[0].Tokens.InputUncached != 900 {
		t.Fatalf("values = %s / %s", describe(morning[0]), describe(evening[0]))
	}
	// Another agent, day, or model is another identity.
	other, _ := ProfileFor(AgentQwen)
	elsewhere, err := other.Observations(day(100, 10, 5))
	if err != nil {
		t.Fatal(err)
	}
	if elsewhere[0].SourceRecordID == morning[0].SourceRecordID {
		t.Fatal("two agents share an identity")
	}
}

func TestDayTotalsKeepUnattributedUsageApartFromModels(t *testing.T) {
	t.Parallel()
	profile, _ := ProfileFor(AgentOpenCode)
	tests := []struct {
		name string
		day  Day
		want []string
	}{
		{name: "usage no breakdown accounts for", day: Day{
			Date: "2026-01-02", Counts: Counts{Input: 10, Output: 6, CacheRead: 4}, Total: 20,
			Models: []ModelUsage{{Model: "model-synthetic", Counts: Counts{Input: 7, Output: 6, CacheRead: 1}}},
		}, want: []string{
			"2026-01-02 model-synthetic in=7 read=1 write=0 out=6",
			"2026-01-02 (no model) in=3 read=3 write=0 out=0",
		}},
		{name: "extra tokens beside usage no breakdown accounts for stay unattributed", day: Day{
			Date: "2026-01-02", Counts: Counts{Input: 10, Output: 6}, Total: 21,
			Models: []ModelUsage{{Model: "model-synthetic", Counts: Counts{Input: 7, Output: 6}}},
		}, want: []string{
			"2026-01-02 model-synthetic in=7 read=0 write=0 out=6",
			"2026-01-02 (no model) in=3 read=0 write=0 out=5",
		}},
		{name: "the placeholder model beside a real one", day: Day{
			Date: "2026-01-02", Counts: Counts{Input: 12, Output: 3}, Total: 15,
			Models: []ModelUsage{{Model: "unknown", Counts: Counts{Input: 2, Output: 1}}, {Model: "model-synthetic", Counts: Counts{Input: 10, Output: 2}}},
		}, want: []string{
			"2026-01-02 model-synthetic in=10 read=0 write=0 out=2",
			"2026-01-02 (no model) in=2 read=0 write=0 out=1",
		}},
		{name: "a model with no tokens leaves no record", day: Day{
			Date: "2026-01-02", Counts: Counts{Input: 4}, Total: 4,
			Models: []ModelUsage{{Model: "unknown"}, {Model: "model-synthetic", Counts: Counts{Input: 4}}, {Model: "model-idle"}},
		}, want: []string{"2026-01-02 model-synthetic in=4 read=0 write=0 out=0"}},
		{name: "a day with nothing counted", day: Day{Date: "2026-01-02", Models: []ModelUsage{{Model: "unknown"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			observations, err := profile.Observations(Report{Days: []Day{test.day}})
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(observations))
			for _, observation := range observations {
				got = append(got, describe(observation))
			}
			if strings.Join(got, "\n") != strings.Join(test.want, "\n") {
				t.Fatalf("day totals:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(test.want, "\n"))
			}
			total, err := Total(observations)
			if err != nil || total != test.day.Total {
				t.Fatalf("Total() = %d, %v; want the day's own %d", total, err, test.day.Total)
			}
		})
	}
}
