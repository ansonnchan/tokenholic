package doctor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ansonnchan/tokenholic/internal/adapters"
	"github.com/ansonnchan/tokenholic/internal/adapters/ccusage"
	"github.com/ansonnchan/tokenholic/internal/normalize"
)

type fakeParser struct{ status ParserStatus }

func (f fakeParser) Check(context.Context) ParserStatus { return f.status }

func TestReportRendersAllSurfacesWithoutPaths(t *testing.T) {
	t.Parallel()

	privateRoot := filepath.Join(t.TempDir(), "private-root")
	kinds := []adapters.RootKind{
		adapters.RootClaudeCLI, adapters.RootClaudeDesktopCode, adapters.RootClaudeDesktopAgent,
		adapters.RootClaudeDesktopCowork, adapters.RootCodexCLI, adapters.RootCodexDesktop, adapters.RootCodexShared,
	}
	findings := make([]adapters.Finding, 0, len(kinds))
	for _, kind := range kinds {
		findings = append(findings, adapters.Finding{
			Kind: kind, Status: normalize.HealthDetected, RecordCount: 1, Format: "synthetic-jsonl",
			Candidates: []adapters.Candidate{{Path: privateRoot}}, Capabilities: normalize.Capabilities{
				Model: normalize.CapabilityReported, InputCacheWrite: normalize.CapabilityUnsupported,
			},
		})
	}
	findings[len(findings)-1].SharedRoot = true
	report := Report{GOOS: "darwin", GOARCH: "arm64", Parser: ParserStatus{State: RunnerReady}, Findings: findings}
	output := report.Render()
	for _, expected := range []string{"Claude Code CLI", "Claude Desktop Code", "Claude Desktop Agent", "Claude Desktop Cowork", "Codex CLI", "Codex Desktop", "Codex CLI/Desktop shared"} {
		if !strings.Contains(output, expected) {
			t.Errorf("output missing %q:\n%s", expected, output)
		}
	}
	if strings.Contains(output, privateRoot) {
		t.Fatalf("output leaked source path: %s", output)
	}
	if !strings.Contains(output, "candidate files") {
		t.Fatalf("output overstates unparsed candidates: %s", output)
	}
	if report.ExitCode() != 0 {
		t.Fatalf("ExitCode() = %d", report.ExitCode())
	}
}

func TestDiagnoseDiscoversSyntheticHome(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	paths := []string{
		filepath.Join(home, ".claude", "projects", "synthetic", "record.jsonl"),
		filepath.Join(home, "Library", "Application Support", "Claude", "claude-code-sessions", "synthetic", "record.jsonl"),
		filepath.Join(home, "Library", "Application Support", "Claude", "local-agent-mode-sessions", "synthetic", "rpm", "agent.jsonl"),
		filepath.Join(home, "Library", "Application Support", "Claude", "local-agent-mode-sessions", "synthetic", "local_synthetic", "audit.jsonl"),
		filepath.Join(home, ".codex", "sessions", "2026", "01", "02", "rollout-synthetic.jsonl"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	discovery := adapters.Discoverer{Roots: adapters.MacRoots(home, "")}
	report := Diagnose(context.Background(), discovery, fakeParser{status: ParserStatus{State: RunnerReady}}, nil)
	if len(report.Findings) != 5 {
		t.Fatalf("findings = %d, want 5", len(report.Findings))
	}
	for _, finding := range report.Findings {
		if finding.Status != normalize.HealthDetected || finding.RecordCount != 1 {
			t.Errorf("finding = %#v", finding)
		}
	}
	if strings.Contains(report.Render(), home) {
		t.Fatal("doctor output leaked the synthetic home path")
	}
}

func TestReportMakesPermissionIssueActionable(t *testing.T) {
	t.Parallel()

	report := Report{GOOS: "darwin", GOARCH: "arm64", Findings: []adapters.Finding{{
		Kind: adapters.RootCodexCLI, Status: normalize.HealthPermissionDenied,
		Warnings: []normalize.Warning{{Code: "source_root_unreadable"}},
	}}}
	output := report.Render()
	if report.ExitCode() != 1 || !strings.Contains(output, "check its file permissions") {
		t.Fatalf("report = %q, code = %d", output, report.ExitCode())
	}
}

func TestReportExplainsWhenNoSupportedSourceIsAvailable(t *testing.T) {
	t.Parallel()
	report := Report{GOOS: "darwin", GOARCH: "arm64", Findings: []adapters.Finding{
		{Kind: adapters.RootClaudeCLI, Status: normalize.HealthNotFound},
		{Kind: adapters.RootCodexShared, Status: normalize.HealthNotFound},
	}}
	output := report.Render()
	for _, want := range []string{"no supported coding-agent usage found", "install or use a supported coding agent", "missing optional sources are not errors"} {
		if !strings.Contains(output, want) {
			t.Fatalf("report = %q, want %q", output, want)
		}
	}
	if report.ExitCode() != 0 {
		t.Fatalf("ExitCode() = %d, want 0", report.ExitCode())
	}
}

type fakeRunner struct {
	verify  error
	results map[ccusage.Agent]error
	days    int
	ran     []ccusage.Agent
}

func (f *fakeRunner) Verify() error { return f.verify }

func (f *fakeRunner) Daily(_ context.Context, agent ccusage.Agent) (ccusage.Report, error) {
	f.ran = append(f.ran, agent)
	if err := f.results[agent]; err != nil {
		return ccusage.Report{}, err
	}
	report := ccusage.Report{}
	for day := 1; day <= f.days; day++ {
		report.Days = append(report.Days, ccusage.Day{
			Date: fmt.Sprintf("2026-01-%02d", day), Counts: ccusage.Counts{Input: 5}, Total: 5,
			Models: []ccusage.ModelUsage{{Model: "model-synthetic", Counts: ccusage.Counts{Input: 5}}},
		})
	}
	return report, nil
}

// probeRoots makes a record folder for each listed source and none for the
// others.
func probeRoots(t *testing.T, present ...normalize.Source) []adapters.AgentRoot {
	t.Helper()
	home := t.TempDir()
	var roots []adapters.AgentRoot
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

func TestCCUsageProbeReportsTheRunnerAndEachAgent(t *testing.T) {
	t.Parallel()
	everything := normalize.DayLevelSources()
	tests := []struct {
		name     string
		present  []normalize.Source
		runner   *fakeRunner
		off      bool
		state    RunnerState
		runs     int
		exitCode int
		want     []string
	}{
		{name: "nothing installed", runner: &fakeRunner{}, state: RunnerReady, want: []string{
			"ccusage: ready (pinned " + ccusage.PinnedVersion + ", executable verified)",
			"GitHub Copilot CLI            not found", "Gemini CLI                    not found", "Grok Build CLI                not found",
			"OpenCode                      not found", "Qwen Code                     not found",
		}},
		{name: "every agent read", present: everything, runner: &fakeRunner{days: 3}, state: RunnerReady, runs: 5, want: []string{
			"Gemini CLI                    detected (3 days of usage); by day through ccusage; model reported; cache-write unsupported",
			"OpenCode                      detected (3 days of usage); by day through ccusage; model reported; cache-write reported",
		}},
		{name: "an agent in use with nothing reported yet", present: []normalize.Source{normalize.SourceGrok}, runner: &fakeRunner{}, state: RunnerReady, runs: 1,
			want: []string{"Grok Build CLI                detected (no usage reported)", "Gemini CLI                    not found"}},
		{name: "turned off", present: []normalize.Source{normalize.SourceGemini}, runner: &fakeRunner{}, off: true, state: RunnerOff, want: []string{
			"ccusage: turned off; run `tokenholic ccusage on`",
			"Gemini CLI                    unavailable: ccusage is turned off; not counted",
			"Qwen Code                     not found",
		}},
		{name: "no runner in this install", present: []normalize.Source{normalize.SourceCopilot}, state: RunnerUnavailable, want: []string{
			"ccusage: not available in this install", "npm install -g @tokenholic/cli",
			"GitHub Copilot CLI            unavailable: this install has no ccusage; not counted",
		}},
		{name: "runner missing on disk", present: []normalize.Source{normalize.SourceQwen}, runner: &fakeRunner{verify: ccusage.ErrUnavailable}, state: RunnerUnavailable,
			want: []string{"Qwen Code                     unavailable: this install has no ccusage; not counted"}},
		{name: "an unpinned executable", present: []normalize.Source{normalize.SourceQwen}, runner: &fakeRunner{verify: ccusage.ErrUnpinned}, state: RunnerUnpinned, exitCode: 1, want: []string{
			"ccusage: refused", "not the pinned " + ccusage.PinnedVersion + " build and was not run",
			"Qwen Code                     unavailable: the ccusage here is not the pinned build; not counted",
		}},
		{name: "an unpinned executable is an issue even with no agent installed", runner: &fakeRunner{verify: ccusage.ErrUnpinned}, state: RunnerUnpinned, exitCode: 1,
			want: []string{"ccusage: refused"}},
		{name: "one agent degrades alone", present: everything, runner: &fakeRunner{days: 1, results: map[ccusage.Agent]error{
			ccusage.AgentGemini:   fmt.Errorf("%w: /synthetic/private/location", ccusage.ErrMalformed),
			ccusage.AgentGrok:     ccusage.ErrTimeout,
			ccusage.AgentOpenCode: ccusage.ErrFailed,
		}}, state: RunnerReady, runs: 5, exitCode: 1, want: []string{
			"Gemini CLI                    error: ccusage answered outside the pinned contract; not counted",
			"Grok Build CLI                error: ccusage timed out; not counted",
			"OpenCode                      error: ccusage could not run; not counted",
			"GitHub Copilot CLI            detected (1 days of usage)",
			"Qwen Code                     detected (1 days of usage)",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			probe := CCUsageProbe{Roots: probeRoots(t, test.present...), Off: test.off}
			if test.runner != nil {
				probe.Runner = test.runner
			}
			status := probe.Check(context.Background())
			if status.State != test.state || len(status.Agents) != len(normalize.DayLevelSources()) {
				t.Fatalf("Check() = %q with %d agents, want %q with every agent", status.State, len(status.Agents), test.state)
			}
			if test.runner != nil && len(test.runner.ran) != test.runs {
				t.Fatalf("reports run = %v, want %d", test.runner.ran, test.runs)
			}
			report := Report{GOOS: "darwin", GOARCH: "arm64", Parser: status}
			output := report.Render()
			for _, want := range test.want {
				if !strings.Contains(output, want) {
					t.Errorf("report is missing %q:\n%s", want, output)
				}
			}
			if strings.Contains(output, "/synthetic/private") || strings.Contains(output, filepath.Dir(probe.Roots[0].Paths[0])) {
				t.Fatalf("report repeats ccusage output or a local path:\n%s", output)
			}
			if report.ExitCode() != test.exitCode {
				t.Fatalf("ExitCode() = %d, want %d\n%s", report.ExitCode(), test.exitCode, output)
			}
		})
	}
}

// With no native source and no day-level agent found, the report says so.
// One day-level agent in use is enough for it not to.
func TestNoSourceSummaryCountsDayLevelAgents(t *testing.T) {
	t.Parallel()
	missing := []adapters.Finding{{Kind: adapters.RootClaudeCLI, Status: normalize.HealthNotFound}}
	none := Report{GOOS: "darwin", GOARCH: "arm64", Findings: missing, Parser: ParserStatus{State: RunnerReady, Agents: []AgentReport{{Source: normalize.SourceGemini, Status: normalize.HealthNotFound}}}}
	if !strings.Contains(none.Render(), "no supported coding-agent usage found") {
		t.Fatalf("report = %q", none.Render())
	}
	some := none
	some.Parser.Agents = []AgentReport{{Source: normalize.SourceGemini, Status: normalize.HealthDetected, Days: 2}}
	if strings.Contains(some.Render(), "no supported coding-agent usage found") {
		t.Fatalf("report = %q", some.Render())
	}
}

func TestReportSaysWhereTheAPIAddressComesFromWithoutShowingIt(t *testing.T) {
	t.Parallel()
	for source, want := range map[APISource]string{
		APIBuiltIn: "API address: built in", APIOverridden: "set by TOKENHOLIC_API_URL", APINone: "development build",
	} {
		report := Report{GOOS: "darwin", GOARCH: "arm64", API: source}
		if output := report.Render(); !strings.Contains(output, want) || strings.Contains(output, "https://") {
			t.Errorf("%s: %q", source, output)
		}
		// Where the address comes from is information, never a failure.
		if report.ExitCode() != 0 {
			t.Errorf("%s: exit %d", source, report.ExitCode())
		}
	}
	if output := (Report{GOOS: "darwin", GOARCH: "arm64"}).Render(); strings.Contains(output, "API address") {
		t.Errorf("a report without a source mentions the API: %q", output)
	}
}

func TestUnsupportedPlatformIsNamed(t *testing.T) {
	t.Parallel()
	for goos, supported := range map[string]bool{"darwin": true, "linux": true, "windows": true, "plan9": false} {
		report := Report{GOOS: goos, GOARCH: "amd64"}
		output := report.Render()
		if strings.Contains(output, "not supported") == supported || (report.ExitCode() == 0) != supported {
			t.Errorf("%s: code %d, output %q", goos, report.ExitCode(), output)
		}
	}
	// The doctor's list and default discovery must not drift apart.
	roots := adapters.DefaultRoots(adapters.Environment{Home: t.TempDir()})
	if platformSupported(runtime.GOOS) != (len(roots) > 0) {
		t.Fatalf("platformSupported(%s) disagrees with %d default roots", runtime.GOOS, len(roots))
	}
}
