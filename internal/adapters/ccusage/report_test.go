package ccusage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "fixtures", "ccusage", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Each fixture is the pinned release's own output for hand-built synthetic
// records. The expected numbers are worked out from those records, not read
// back from the report.
func TestAgentFixturesMatchContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		fixture string
		days    []Day
	}{
		{fixture: "empty-daily.json"},
		{fixture: "qwen-empty-daily.json"},
		{fixture: "copilot-daily.json", days: []Day{
			{Date: "2026-01-02", Counts: Counts{Input: 1100, Output: 580, CacheCreation: 200, CacheRead: 100}, Total: 1980, Models: []ModelUsage{
				{Model: "claude-synthetic-4.6", Counts: Counts{Input: 700, Output: 500, CacheCreation: 200, CacheRead: 100}},
				{Model: "gpt-synthetic-5", Counts: Counts{Input: 400, Output: 80}},
			}},
			{Date: "2026-01-03", Counts: Counts{Input: 400, Output: 200, CacheCreation: 50, CacheRead: 50}, Total: 700, Models: []ModelUsage{
				{Model: "claude-synthetic-4.6", Counts: Counts{Input: 400, Output: 200, CacheCreation: 50, CacheRead: 50}},
			}},
		}},
		{fixture: "gemini-daily.json", days: []Day{
			{Date: "2026-01-02", Counts: Counts{Input: 720, Output: 220, CacheRead: 400}, Total: 1440, Models: []ModelUsage{
				{Model: "gemini-synthetic-pro", Counts: Counts{Input: 620, Output: 200, CacheRead: 400}},
				{Model: "gemini-synthetic-flash", Counts: Counts{Input: 100, Output: 20}},
			}},
			{Date: "2026-01-03", Counts: Counts{Input: 400, Output: 50, CacheRead: 100}, Total: 575, Models: []ModelUsage{
				{Model: "gemini-synthetic-pro", Counts: Counts{Input: 400, Output: 50, CacheRead: 100}},
			}},
		}},
		{fixture: "grok-daily.json", days: []Day{
			{Date: "2026-01-02", Counts: Counts{Input: 500, Output: 200, CacheCreation: 100, CacheRead: 400}, Total: 1200, Models: []ModelUsage{
				{Model: "grok-synthetic-build", Counts: Counts{Input: 500, Output: 200, CacheCreation: 100, CacheRead: 400}},
			}},
			{Date: "2026-01-03", Counts: Counts{Input: 300, Output: 60}, Total: 360, Models: []ModelUsage{
				{Model: "grok-synthetic-build", Counts: Counts{Input: 300, Output: 60}},
			}},
		}},
		{fixture: "opencode-daily.json", days: []Day{
			{Date: "2026-01-02", Counts: Counts{Input: 1300, Output: 540, CacheCreation: 200, CacheRead: 100}, Total: 2220, Models: []ModelUsage{
				{Model: "model-synthetic-x", Counts: Counts{Input: 1000, Output: 500, CacheCreation: 200, CacheRead: 100}},
				{Model: "model-synthetic-y", Counts: Counts{Input: 300, Output: 40}},
			}},
			{Date: "2026-01-03", Counts: Counts{Input: 200, Output: 20, CacheRead: 50}, Total: 275, Models: []ModelUsage{
				{Model: "model-synthetic-x", Counts: Counts{Input: 200, Output: 20, CacheRead: 50}},
			}},
		}},
		{fixture: "qwen-daily.json", days: []Day{
			{Date: "2026-01-02", Counts: Counts{Input: 1500, Output: 300, CacheRead: 300}, Total: 2200, Models: []ModelUsage{
				{Model: "qwen-synthetic-a", Counts: Counts{Input: 1000, Output: 200, CacheRead: 300}},
				{Model: "qwen-synthetic-b", Counts: Counts{Input: 500, Output: 100}},
			}},
			{Date: "2026-01-03", Counts: Counts{Input: 50, Output: 10}, Total: 60, Models: []ModelUsage{
				{Model: "qwen-synthetic-a", Counts: Counts{Input: 50, Output: 10}},
			}},
			{Date: "2026-01-04", Counts: Counts{Input: 5, Output: 1}, Total: 6, Models: []ModelUsage{
				{Model: "unknown", Counts: Counts{Input: 5, Output: 1}},
			}},
		}},
	}
	for _, test := range tests {
		t.Run(test.fixture, func(t *testing.T) {
			t.Parallel()
			report, err := ParseDailyReport(readFixture(t, test.fixture))
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Days) != len(test.days) {
				t.Fatalf("days = %d, want %d", len(report.Days), len(test.days))
			}
			for index, want := range test.days {
				got := report.Days[index]
				if got.Date != want.Date || got.Counts != want.Counts || got.Total != want.Total || len(got.Models) != len(want.Models) {
					t.Fatalf("day %d = %+v, want %+v", index, got, want)
				}
				for model, usage := range want.Models {
					if got.Models[model] != usage {
						t.Errorf("day %s model %d = %+v, want %+v", want.Date, model, got.Models[model], usage)
					}
				}
			}
		})
	}
}

func TestDayExtraAndUnattributed(t *testing.T) {
	t.Parallel()
	// Reasoning tokens an agent reports apart from output reach ccusage's
	// total without reaching any category. Gemini's first fixture day holds
	// 90 and 10 of them; Grok and Copilot count reasoning inside output.
	for fixture, want := range map[string][]uint64{
		"gemini-daily.json": {100, 25}, "qwen-daily.json": {100, 0, 0}, "opencode-daily.json": {80, 5},
		"grok-daily.json": {0, 0}, "copilot-daily.json": {0, 0},
	} {
		report, err := ParseDailyReport(readFixture(t, fixture))
		if err != nil {
			t.Fatal(err)
		}
		for index, day := range report.Days {
			if day.Extra() != want[index] {
				t.Errorf("%s day %s Extra() = %d, want %d", fixture, day.Date, day.Extra(), want[index])
			}
			if rest := day.Unattributed(); rest != (Counts{}) {
				t.Errorf("%s day %s Unattributed() = %+v, want none", fixture, day.Date, rest)
			}
		}
	}
	day := Day{
		Counts: Counts{Input: 10, Output: 6, CacheCreation: 2, CacheRead: 4}, Total: 25,
		Models: []ModelUsage{{Model: "model-synthetic", Counts: Counts{Input: 7, Output: 6, CacheRead: 1}}},
	}
	if day.Extra() != 3 || day.Unattributed() != (Counts{Input: 3, CacheCreation: 2, CacheRead: 3}) {
		t.Fatalf("Extra() = %d, Unattributed() = %+v", day.Extra(), day.Unattributed())
	}
}

func TestParseDailyReportIgnoresCostFigures(t *testing.T) {
	t.Parallel()
	plain := `{"daily":[{"date":"2026-01-02","inputTokens":5,"outputTokens":3,"cacheCreationTokens":0,"cacheReadTokens":0,"totalTokens":8,
		"modelsUsed":["model-synthetic"],"modelBreakdowns":[{"modelName":"model-synthetic","inputTokens":5,"outputTokens":3,"cacheCreationTokens":0,"cacheReadTokens":0}]}],
		"totals":{"inputTokens":5,"outputTokens":3,"cacheCreationTokens":0,"cacheReadTokens":0,"totalTokens":8}}`
	priced := `{"daily":[{"date":"2026-01-02","inputTokens":5,"outputTokens":3,"cacheCreationTokens":0,"cacheReadTokens":0,"totalTokens":8,"totalCost":12.5,"credits":3.25,"messageCount":2,
		"modelsUsed":["model-synthetic"],"modelBreakdowns":[{"modelName":"model-synthetic","inputTokens":5,"outputTokens":3,"cacheCreationTokens":0,"cacheReadTokens":0,"cost":12.5,"storedCost":1,"missingPricing":true}]}],
		"totals":{"inputTokens":5,"outputTokens":3,"cacheCreationTokens":0,"cacheReadTokens":0,"totalTokens":8,"totalCost":12.5,"credits":3.25,"unpricedModels":["model-synthetic"]}}`
	want, err := ParseDailyReport([]byte(plain))
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseDailyReport([]byte(priced))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Days) != 1 || got.Days[0].Counts != want.Days[0].Counts || got.Days[0].Total != want.Days[0].Total || got.Days[0].Models[0] != want.Days[0].Models[0] {
		t.Fatalf("cost figures changed the report: %+v, want %+v", got, want)
	}
}

func TestParseDailyReportRejectsWhatTheContractDoesNotDescribe(t *testing.T) {
	t.Parallel()
	const model = `{"modelName":"model-synthetic","inputTokens":5,"outputTokens":3,"cacheCreationTokens":0,"cacheReadTokens":0}`
	const zeroTotals = `"totals":{"inputTokens":0,"outputTokens":0,"cacheCreationTokens":0,"cacheReadTokens":0,"totalTokens":0}`
	const totals = `"totals":{"inputTokens":5,"outputTokens":3,"cacheCreationTokens":0,"cacheReadTokens":0,"totalTokens":8}`
	day := func(fields, breakdowns string) string {
		return `{"daily":[{` + fields + `,"modelsUsed":["model-synthetic"],"modelBreakdowns":[` + breakdowns + `]}],` + totals + `}`
	}
	const counts = `"date":"2026-01-02","inputTokens":5,"outputTokens":3,"cacheCreationTokens":0,"cacheReadTokens":0,"totalTokens":8`
	tests := []struct {
		name string
		data string
	}{
		{name: "not JSON", data: `No usage data found.`},
		{name: "empty output", data: ``},
		{name: "an array", data: `[]`},
		{name: "trailing value", data: `{"daily":[],` + zeroTotals + `} {}`},
		{name: "missing totals", data: `{"daily":[]}`},
		{name: "missing daily", data: `{` + zeroTotals + `}`},
		{name: "null daily", data: `{"daily":null,` + zeroTotals + `}`},
		{name: "unknown top-level field", data: `{"daily":[],"projects":{},` + zeroTotals + `}`},
		{name: "duplicate field", data: `{"daily":[],"daily":[],` + zeroTotals + `}`},
		{name: "another report shape", data: `{"type":"daily","data":[],"summary":{}}`},
		{name: "null totals beside rows", data: `{"daily":[{` + counts + `,"modelsUsed":["model-synthetic"],"modelBreakdowns":[` + model + `]}],"totals":null}`},
		{name: "totals differ from rows", data: `{"daily":[{` + counts + `,"modelsUsed":["model-synthetic"],"modelBreakdowns":[` + model + `]}],` + zeroTotals + `}`},
		{name: "missing date", data: day(`"inputTokens":5,"outputTokens":3,"cacheCreationTokens":0,"cacheReadTokens":0,"totalTokens":8`, model)},
		{name: "a month instead of a date", data: day(strings.Replace(counts, "2026-01-02", "2026-01", 1), model)},
		{name: "an impossible date", data: day(strings.Replace(counts, "2026-01-02", "2026-02-30", 1), model)},
		{name: "a date with a time", data: day(strings.Replace(counts, "2026-01-02", "2026-01-02T00:00:00Z", 1), model)},
		{name: "missing count", data: day(`"date":"2026-01-02","inputTokens":5,"outputTokens":3,"cacheReadTokens":0,"totalTokens":8`, model)},
		{name: "null count", data: day(strings.Replace(counts, `"cacheReadTokens":0`, `"cacheReadTokens":null`, 1), model)},
		{name: "negative count", data: day(strings.Replace(counts, `"inputTokens":5`, `"inputTokens":-5`, 1), model)},
		{name: "fractional count", data: day(strings.Replace(counts, `"inputTokens":5`, `"inputTokens":5.5`, 1), model)},
		{name: "count as text", data: day(strings.Replace(counts, `"inputTokens":5`, `"inputTokens":"5"`, 1), model)},
		{name: "missing total", data: day(`"date":"2026-01-02","inputTokens":5,"outputTokens":3,"cacheCreationTokens":0,"cacheReadTokens":0`, model)},
		{name: "total smaller than categories", data: day(strings.Replace(counts, `"totalTokens":8`, `"totalTokens":7`, 1), model)},
		{name: "unknown row field", data: day(counts+`,"projectPath":"x"`, model)},
		{name: "unknown breakdown field", data: day(counts, strings.Replace(model, `}`, `,"sessionId":"x"}`, 1))},
		{name: "breakdown without a name", data: day(counts, strings.Replace(model, `"modelName":"model-synthetic",`, ``, 1))},
		{name: "breakdown with an empty name", data: day(counts, strings.Replace(model, `model-synthetic`, ``, 1))},
		{name: "repeated breakdown", data: day(counts, model+","+model)},
		{name: "breakdown exceeds the day", data: day(counts, strings.Replace(model, `"outputTokens":3`, `"outputTokens":4`, 1))},
		{name: "1-hour cache write exceeds its total", data: day(counts, strings.Replace(model, `}`, `,"cacheCreation1hTokens":1}`, 1))},
		{name: "model missing from modelsUsed", data: `{"daily":[{` + counts + `,"modelsUsed":[],"modelBreakdowns":[` + model + `]}],` + totals + `}`},
		{name: "modelsUsed names another model", data: `{"daily":[{` + counts + `,"modelsUsed":["other-synthetic"],"modelBreakdowns":[` + model + `]}],` + totals + `}`},
		{name: "null modelBreakdowns", data: `{"daily":[{` + counts + `,"modelsUsed":[],"modelBreakdowns":null}],` + totals + `}`},
		{name: "repeated date", data: `{"daily":[{` + counts + `,"modelsUsed":["model-synthetic"],"modelBreakdowns":[` + model + `]},{` + counts + `,"modelsUsed":["model-synthetic"],"modelBreakdowns":[` + model + `]}],"totals":{"inputTokens":10,"outputTokens":6,"cacheCreationTokens":0,"cacheReadTokens":0,"totalTokens":16}}`},
		{name: "count overflow", data: day(strings.Replace(strings.Replace(counts, `"inputTokens":5`, `"inputTokens":18446744073709551615`, 1), `"totalTokens":8`, `"totalTokens":18446744073709551615`, 1), model)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if report, err := ParseDailyReport([]byte(test.data)); err == nil {
				t.Fatalf("ParseDailyReport() accepted %+v", report)
			}
		})
	}
	// The baseline the cases above are edits of must itself be valid.
	if _, err := ParseDailyReport([]byte(day(counts, model))); err != nil {
		t.Fatalf("baseline report was rejected: %v", err)
	}
}
