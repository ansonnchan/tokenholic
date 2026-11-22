package collect

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/adapters"
	"github.com/ansonnchan/tokenholic/internal/adapters/ccusage"
	"github.com/ansonnchan/tokenholic/internal/normalize"
	"github.com/ansonnchan/tokenholic/internal/store"
)

// fakeDayRunner answers from the committed contract fixtures. No ccusage
// runs and no home directory is read.
type fakeDayRunner struct {
	verify error
	fail   map[ccusage.Agent]error
	// reports replaces a fixture for an agent.
	reports map[ccusage.Agent]ccusage.Report
	ran     []ccusage.Agent
}

func (f *fakeDayRunner) Verify() error { return f.verify }

func (f *fakeDayRunner) Daily(_ context.Context, agent ccusage.Agent) (ccusage.Report, error) {
	f.ran = append(f.ran, agent)
	if err := f.fail[agent]; err != nil {
		return ccusage.Report{}, err
	}
	if report, replaced := f.reports[agent]; replaced {
		return report, nil
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "fixtures", "ccusage", string(agent)+"-daily.json"))
	if err != nil {
		return ccusage.Report{}, err
	}
	return ccusage.ParseDailyReport(data)
}

// agentRoots makes a record folder for each listed source and none for the
// rest, so the reader sees some agents installed and others not.
func agentRoots(t *testing.T, present ...normalize.Source) []adapters.AgentRoot {
	t.Helper()
	home := t.TempDir()
	roots := make([]adapters.AgentRoot, 0, len(normalize.DayLevelSources()))
	for _, source := range normalize.DayLevelSources() {
		path := filepath.Join(home, string(source))
		for _, installed := range present {
			if installed == source {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
		}
		roots = append(roots, adapters.AgentRoot{Source: source, Paths: []string{path}})
	}
	return roots
}

func readingFor(t *testing.T, readings []DayReading, source normalize.Source) DayReading {
	t.Helper()
	for _, reading := range readings {
		if reading.Health.Source == source {
			return reading
		}
	}
	t.Fatalf("no reading for %s", source)
	return DayReading{}
}

func TestDayLevelReportsEachAgentOnItsOwn(t *testing.T) {
	t.Parallel()
	everything := normalize.DayLevelSources()
	type want struct {
		status  normalize.HealthStatus
		warning string
		days    int
		records int
	}
	tests := []struct {
		name    string
		present []normalize.Source
		runner  *fakeDayRunner
		off     bool
		noRun   bool
		ran     []ccusage.Agent
		want    map[normalize.Source]want
	}{
		{name: "every agent installed and read", present: everything, runner: &fakeDayRunner{},
			ran: []ccusage.Agent{ccusage.AgentCopilot, ccusage.AgentGemini, ccusage.AgentGrok, ccusage.AgentOpenCode, ccusage.AgentQwen},
			want: map[normalize.Source]want{
				normalize.SourceCopilot:  {status: normalize.HealthDetected, days: 2, records: 3},
				normalize.SourceGemini:   {status: normalize.HealthDetected, days: 2, records: 4},
				normalize.SourceGrok:     {status: normalize.HealthDetected, days: 2, records: 2},
				normalize.SourceOpenCode: {status: normalize.HealthDetected, days: 2, records: 4},
				normalize.SourceQwen:     {status: normalize.HealthDetected, days: 3, records: 5},
			}},
		{name: "ccusage never runs for an agent that is not installed", present: []normalize.Source{normalize.SourceGemini}, runner: &fakeDayRunner{},
			ran: []ccusage.Agent{ccusage.AgentGemini},
			want: map[normalize.Source]want{
				normalize.SourceCopilot:  {status: normalize.HealthNotFound},
				normalize.SourceGemini:   {status: normalize.HealthDetected, days: 2, records: 4},
				normalize.SourceGrok:     {status: normalize.HealthNotFound},
				normalize.SourceOpenCode: {status: normalize.HealthNotFound},
				normalize.SourceQwen:     {status: normalize.HealthNotFound},
			}},
		{name: "one failing agent degrades only that source", present: everything, runner: &fakeDayRunner{fail: map[ccusage.Agent]error{
			ccusage.AgentGemini: ccusage.ErrMalformed, ccusage.AgentGrok: ccusage.ErrTimeout, ccusage.AgentOpenCode: ccusage.ErrFailed,
		}},
			ran: []ccusage.Agent{ccusage.AgentCopilot, ccusage.AgentGemini, ccusage.AgentGrok, ccusage.AgentOpenCode, ccusage.AgentQwen},
			want: map[normalize.Source]want{
				normalize.SourceCopilot:  {status: normalize.HealthDetected, days: 2, records: 3},
				normalize.SourceGemini:   {status: normalize.HealthError, warning: WarningCCUsageMalformed},
				normalize.SourceGrok:     {status: normalize.HealthError, warning: WarningCCUsageTimeout},
				normalize.SourceOpenCode: {status: normalize.HealthError, warning: WarningCCUsageFailed},
				normalize.SourceQwen:     {status: normalize.HealthDetected, days: 3, records: 5},
			}},
		{name: "turned off: installed agents are unavailable and nothing runs", present: []normalize.Source{normalize.SourceGemini, normalize.SourceQwen},
			runner: &fakeDayRunner{}, off: true, noRun: true,
			want: map[normalize.Source]want{
				normalize.SourceCopilot:  {status: normalize.HealthNotFound},
				normalize.SourceGemini:   {status: normalize.HealthUnavailable, warning: WarningCCUsageOff},
				normalize.SourceGrok:     {status: normalize.HealthNotFound},
				normalize.SourceOpenCode: {status: normalize.HealthNotFound},
				normalize.SourceQwen:     {status: normalize.HealthUnavailable, warning: WarningCCUsageOff},
			}},
		{name: "no executable in this install", present: []normalize.Source{normalize.SourceCopilot},
			runner: &fakeDayRunner{verify: ccusage.ErrUnavailable}, noRun: true,
			want: map[normalize.Source]want{
				normalize.SourceCopilot:  {status: normalize.HealthUnavailable, warning: WarningCCUsageNotInstalled},
				normalize.SourceGemini:   {status: normalize.HealthNotFound},
				normalize.SourceGrok:     {status: normalize.HealthNotFound},
				normalize.SourceOpenCode: {status: normalize.HealthNotFound},
				normalize.SourceQwen:     {status: normalize.HealthNotFound},
			}},
		{name: "an executable that is not the pinned release is never run", present: everything,
			runner: &fakeDayRunner{verify: ccusage.ErrUnpinned}, noRun: true,
			want: map[normalize.Source]want{
				normalize.SourceCopilot:  {status: normalize.HealthUnavailable, warning: WarningCCUsageUnpinned},
				normalize.SourceGemini:   {status: normalize.HealthUnavailable, warning: WarningCCUsageUnpinned},
				normalize.SourceGrok:     {status: normalize.HealthUnavailable, warning: WarningCCUsageUnpinned},
				normalize.SourceOpenCode: {status: normalize.HealthUnavailable, warning: WarningCCUsageUnpinned},
				normalize.SourceQwen:     {status: normalize.HealthUnavailable, warning: WarningCCUsageUnpinned},
			}},
		{name: "an installed agent with no usage yet", present: []normalize.Source{normalize.SourceGrok},
			runner: &fakeDayRunner{reports: map[ccusage.Agent]ccusage.Report{ccusage.AgentGrok: {}}}, ran: []ccusage.Agent{ccusage.AgentGrok},
			want: map[normalize.Source]want{
				normalize.SourceCopilot:  {status: normalize.HealthNotFound},
				normalize.SourceGemini:   {status: normalize.HealthNotFound},
				normalize.SourceGrok:     {status: normalize.HealthDetected},
				normalize.SourceOpenCode: {status: normalize.HealthNotFound},
				normalize.SourceQwen:     {status: normalize.HealthNotFound},
			}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			reader := DayLevel{Roots: agentRoots(t, test.present...), Runner: test.runner, Off: test.off}
			readings := reader.Read(t.Context())
			if len(readings) != len(normalize.DayLevelSources()) {
				t.Fatalf("readings = %d, want one per day-level source", len(readings))
			}
			if test.noRun && len(test.runner.ran) != 0 {
				t.Fatalf("ccusage ran for %v", test.runner.ran)
			}
			if !test.noRun && !reflect.DeepEqual(test.runner.ran, test.ran) {
				t.Fatalf("ccusage ran for %v, want %v", test.runner.ran, test.ran)
			}
			for source, want := range test.want {
				reading := readingFor(t, readings, source)
				if reading.Health.Status != want.status || len(reading.Observations) != want.records {
					t.Errorf("%s: status %q with %d records, want %q with %d", source, reading.Health.Status, len(reading.Observations), want.status, want.records)
				}
				if (want.warning == "") != (len(reading.Health.Warnings) == 0) || want.warning != "" && reading.Health.Warnings[0].Code != want.warning {
					t.Errorf("%s: warnings = %+v, want %q", source, reading.Health.Warnings, want.warning)
				}
				read := want.status == normalize.HealthDetected
				if reading.Read != read {
					t.Errorf("%s: Read = %t, want %t", source, reading.Read, read)
				}
				// A count of days is stated only when a report was read. An
				// agent that could not be read has no count, not a zero.
				if read != (reading.Health.RecordCount != nil) || read && int(*reading.Health.RecordCount) != want.days {
					t.Errorf("%s: record count = %v, want %d days when read and none otherwise", source, reading.Health.RecordCount, want.days)
				}
				if err := reading.Health.Capabilities.Validate(); err != nil {
					t.Errorf("%s: %v", source, err)
				}
			}
		})
	}
}

func TestDayLevelWithoutARunnerReportsWhy(t *testing.T) {
	t.Parallel()
	readings := DayLevel{Roots: agentRoots(t, normalize.SourceQwen)}.Read(t.Context())
	reading := readingFor(t, readings, normalize.SourceQwen)
	if reading.Health.Status != normalize.HealthUnavailable || reading.Health.Warnings[0].Code != WarningCCUsageNotInstalled || reading.Read {
		t.Fatalf("reading = %+v", reading)
	}
}

func TestDayLevelStopsWhenTheScanIsCanceled(t *testing.T) {
	t.Parallel()
	runner := &fakeDayRunner{fail: map[ccusage.Agent]error{ccusage.AgentGemini: context.Canceled}}
	readings := DayLevel{Roots: agentRoots(t, normalize.SourceGemini), Runner: runner}.Read(t.Context())
	reading := readingFor(t, readings, normalize.SourceGemini)
	if reading.Health.Status != normalize.HealthError || reading.Health.Warnings[0].Code != "collection_canceled" || reading.Read {
		t.Fatalf("reading = %+v", reading)
	}
}

type dayScan struct {
	database *store.DB
	scan     func(*fakeDayRunner, bool)
}

func newDayScan(t *testing.T, native []adapters.Finding, present ...normalize.Source) dayScan {
	t.Helper()
	database := openCollectionStore(t)
	roots := agentRoots(t, present...)
	now := time.Date(2026, time.January, 5, 12, 0, 0, 0, time.UTC)
	return dayScan{database: database, scan: func(runner *fakeDayRunner, off bool) {
		t.Helper()
		orchestrator := Orchestrator{
			Discovery: staticDiscovery(native), Collector: Collector{SourceVersion: "synthetic-v1"},
			Store: database, AdapterVersion: "native-test-v1", Now: func() time.Time { return now },
			DayLevel: DayLevel{Roots: roots, Runner: runner, Off: off},
		}
		if _, err := orchestrator.Scan(t.Context()); err != nil {
			t.Fatal(err)
		}
		revision, err := database.NextUsageBucketRevision(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.RebuildUsageBuckets(t.Context(), revision, nil); err != nil {
			t.Fatal(err)
		}
	}}
}

func sourceTotals(t *testing.T, database *store.DB) map[string]uint64 {
	t.Helper()
	summary, err := database.QueryUsage(t.Context(), store.PeriodAllTime, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	totals := make(map[string]uint64)
	for _, source := range summary.Sources {
		if total := source.Tokens.Total(); total.Value != nil {
			totals[source.Name] = *total.Value
		}
	}
	return totals
}

// A user of every listed agent sees its tokens, and scanning again, with the
// same reports or with a day that grew, never counts a day twice.
func TestScanStoresEveryAgentsTokensOnce(t *testing.T) {
	t.Parallel()
	scan := newDayScan(t, nil, normalize.DayLevelSources()...)
	// The fixtures' hand-computed totals, as in the adapter's own tests.
	want := map[string]uint64{"copilot": 2680, "gemini": 2015, "grok": 1560, "opencode": 2495, "qwen": 2266}
	for round := 0; round < 3; round++ {
		scan.scan(&fakeDayRunner{}, false)
		if got := sourceTotals(t, scan.database); !reflect.DeepEqual(got, want) {
			t.Fatalf("scan %d totals = %v, want %v", round, got, want)
		}
	}
	// Gemini's first day grows: its two models now share 2,000 more input.
	grown, err := (&fakeDayRunner{}).Daily(t.Context(), ccusage.AgentGemini)
	if err != nil {
		t.Fatal(err)
	}
	grown.Days[0].Input += 2000
	grown.Days[0].Total += 2000
	grown.Days[0].Models[0].Input += 2000
	scan.scan(&fakeDayRunner{reports: map[ccusage.Agent]ccusage.Report{ccusage.AgentGemini: grown}}, false)
	want["gemini"] = 4015
	if got := sourceTotals(t, scan.database); !reflect.DeepEqual(got, want) {
		t.Fatalf("totals after the day grew = %v, want %v", got, want)
	}
	health, err := scan.database.AdapterHealthRecords(t.Context())
	if err != nil || len(health) != len(normalize.DayLevelSources()) {
		t.Fatalf("health records = %d, %v", len(health), err)
	}
	for _, record := range health {
		if record.Health.Status != normalize.HealthDetected || record.LastSuccessAt == nil || record.Health.Format != ccusage.SchemaID {
			t.Errorf("health for %s = %+v", record.Health.Source, record)
		}
	}
}

// When an agent cannot be read, what was stored for it stays, and its health
// says why. The usage is never replaced by zero.
func TestUnreadableAgentKeepsItsStoredUsage(t *testing.T) {
	t.Parallel()
	for name, second := range map[string]struct {
		runner *fakeDayRunner
		off    bool
		status normalize.HealthStatus
		reason string
	}{
		"turned off":       {runner: &fakeDayRunner{}, off: true, status: normalize.HealthUnavailable, reason: WarningCCUsageOff},
		"runner removed":   {runner: &fakeDayRunner{verify: ccusage.ErrUnavailable}, status: normalize.HealthUnavailable, reason: WarningCCUsageNotInstalled},
		"runner replaced":  {runner: &fakeDayRunner{verify: ccusage.ErrUnpinned}, status: normalize.HealthUnavailable, reason: WarningCCUsageUnpinned},
		"malformed output": {runner: &fakeDayRunner{fail: map[ccusage.Agent]error{ccusage.AgentGemini: ccusage.ErrMalformed}}, status: normalize.HealthError, reason: WarningCCUsageMalformed},
		"timed out":        {runner: &fakeDayRunner{fail: map[ccusage.Agent]error{ccusage.AgentGemini: ccusage.ErrTimeout}}, status: normalize.HealthError, reason: WarningCCUsageTimeout},
		"a report that cannot become day totals": {runner: &fakeDayRunner{reports: map[ccusage.Agent]ccusage.Report{
			ccusage.AgentGemini: {Days: []ccusage.Day{{Date: "not-a-date", Counts: ccusage.Counts{Input: 1}, Total: 1}}},
		}}, status: normalize.HealthError, reason: WarningCCUsageMalformed},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			scan := newDayScan(t, nil, normalize.SourceGemini)
			scan.scan(&fakeDayRunner{}, false)
			if got := sourceTotals(t, scan.database)["gemini"]; got != 2015 {
				t.Fatalf("stored Gemini total = %d", got)
			}
			scan.scan(second.runner, second.off)
			if got := sourceTotals(t, scan.database)["gemini"]; got != 2015 {
				t.Fatalf("Gemini total after an unreadable scan = %d, want the stored 2015", got)
			}
			health, err := scan.database.AdapterHealthRecords(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range health {
				if record.Health.Source != normalize.SourceGemini {
					continue
				}
				if record.Health.Status != second.status || len(record.Health.Warnings) != 1 || record.Health.Warnings[0].Code != second.reason {
					t.Fatalf("Gemini health = %+v, want %s because %s", record.Health, second.status, second.reason)
				}
				if record.Health.RecordCount != nil || record.LastSuccessAt == nil {
					t.Fatalf("Gemini health = %+v with last success %v; want no count now and the earlier success kept", record.Health, record.LastSuccessAt)
				}
			}
		})
	}
}

// Claude and Codex are read by their native adapters only. Scanning with the
// day-level reader present, absent, failing, and reporting usage must leave
// their observations, buckets, and totals exactly the same.
func TestDayLevelAgentsDoNotChangeClaudeOrCodex(t *testing.T) {
	t.Parallel()
	native := syntheticFindings(t)
	now := time.Date(2026, time.January, 5, 12, 0, 0, 0, time.UTC)
	baseline := openCollectionStore(t)
	alone := Orchestrator{
		Discovery: staticDiscovery(native), Collector: Collector{SourceVersion: "synthetic-v1"},
		Store: baseline, AdapterVersion: "native-test-v1", Now: func() time.Time { return now },
	}
	if _, err := alone.Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	revision, err := baseline.NextUsageBucketRevision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := baseline.RebuildUsageBuckets(t.Context(), revision, nil); err != nil {
		t.Fatal(err)
	}
	nativeOnly := func(database *store.DB) ([]store.StoredObservation, []store.OutboxRecord, map[string]uint64) {
		t.Helper()
		all, err := database.Observations(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var observations []store.StoredObservation
		for _, observation := range all {
			if observation.Source == normalize.SourceClaude || observation.Source == normalize.SourceCodex {
				observations = append(observations, observation)
			}
		}
		pending, err := database.PendingOutbox(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var buckets []store.OutboxRecord
		for _, record := range pending {
			if !strings.Contains(string(record.Payload), `"granularity":"day"`) {
				buckets = append(buckets, record)
			}
		}
		totals := sourceTotals(t, database)
		return observations, buckets, map[string]uint64{"claude": totals["claude"], "codex": totals["codex"]}
	}
	wantObservations, wantBuckets, wantTotals := nativeOnly(baseline)
	if len(wantObservations) == 0 || len(wantBuckets) == 0 || wantTotals["claude"] == 0 || wantTotals["codex"] == 0 {
		t.Fatalf("the native fixtures produced nothing to compare: %d observations, %d buckets, %v", len(wantObservations), len(wantBuckets), wantTotals)
	}

	scan := newDayScan(t, native, normalize.DayLevelSources()...)
	for step, runner := range []*fakeDayRunner{
		{},
		{},
		{fail: map[ccusage.Agent]error{ccusage.AgentGemini: ccusage.ErrMalformed, ccusage.AgentQwen: errors.New("synthetic failure")}},
		{verify: ccusage.ErrUnpinned},
		{},
	} {
		scan.scan(runner, step == 3)
		observations, buckets, totals := nativeOnly(scan.database)
		if !reflect.DeepEqual(observations, wantObservations) {
			t.Fatalf("step %d changed the Claude and Codex observations", step)
		}
		if !reflect.DeepEqual(buckets, wantBuckets) {
			t.Fatalf("step %d changed the Claude and Codex buckets or their upload bytes", step)
		}
		if !reflect.DeepEqual(totals, wantTotals) {
			t.Fatalf("step %d: Claude and Codex totals = %v, want %v", step, totals, wantTotals)
		}
	}
	if got := sourceTotals(t, scan.database); got["gemini"] != 2015 || got["copilot"] != 2680 {
		t.Fatalf("the day-level agents were not stored beside them: %v", got)
	}
}
