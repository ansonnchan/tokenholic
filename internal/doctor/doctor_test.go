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
	return ccusage.Report{Days: make([]ccusage.Day, f.days)}, nil
}

func TestCCUsageProbeReportsTheRunner(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		probe    CCUsageProbe
		state    RunnerState
		runs     int
		exitCode int
		want     []string
	}{
		{name: "turned off", probe: CCUsageProbe{Off: true, Runner: &fakeRunner{}}, state: RunnerOff,
			want: []string{"ccusage: turned off", "tokenholic ccusage on", "Gemini CLI"}},
		{name: "no runner in this install", probe: CCUsageProbe{}, state: RunnerUnavailable,
			want: []string{"ccusage: not available in this install", "not counted: GitHub Copilot CLI, Gemini CLI, Grok Build CLI, OpenCode, Qwen Code", "npm install -g @tokenholic/cli"}},
		{name: "runner missing on disk", probe: CCUsageProbe{Runner: &fakeRunner{verify: ccusage.ErrUnavailable}}, state: RunnerUnavailable,
			want: []string{"ccusage: not available in this install"}},
		{name: "an unpinned executable", probe: CCUsageProbe{Runner: &fakeRunner{verify: ccusage.ErrUnpinned}}, state: RunnerUnpinned, exitCode: 1,
			want: []string{"ccusage: refused", "not the pinned " + ccusage.PinnedVersion + " build and was not run"}},
		{name: "every report valid", probe: CCUsageProbe{Runner: &fakeRunner{days: 3}}, state: RunnerReady, runs: 5,
			want: []string{"ccusage: ready (pinned " + ccusage.PinnedVersion + ", executable verified)", "report valid (3 days of usage)"}},
		{name: "no usage is not an issue", probe: CCUsageProbe{Runner: &fakeRunner{}}, state: RunnerReady, runs: 5,
			want: []string{"report valid (no usage found)"}},
		{name: "one agent degrades alone", probe: CCUsageProbe{Runner: &fakeRunner{days: 1, results: map[ccusage.Agent]error{
			ccusage.AgentGemini:   fmt.Errorf("%w: /synthetic/private/location", ccusage.ErrMalformed),
			ccusage.AgentGrok:     ccusage.ErrTimeout,
			ccusage.AgentOpenCode: ccusage.ErrFailed,
		}}}, state: RunnerReady, runs: 5, exitCode: 1,
			want: []string{
				"Gemini CLI                    malformed output; this agent is not counted",
				"Grok Build CLI                timed out; this agent is not counted",
				"OpenCode                      could not run; this agent is not counted",
				"GitHub Copilot CLI            report valid (1 days of usage)",
				"Qwen Code                     report valid (1 days of usage)",
			}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			status := test.probe.Check(context.Background())
			if status.State != test.state {
				t.Fatalf("Check() state = %q, want %q", status.State, test.state)
			}
			if runner, ok := test.probe.Runner.(*fakeRunner); ok && len(runner.ran) != test.runs {
				t.Fatalf("reports run = %d, want %d", len(runner.ran), test.runs)
			}
			report := Report{GOOS: "darwin", GOARCH: "arm64", Parser: status}
			output := report.Render()
			for _, want := range test.want {
				if !strings.Contains(output, want) {
					t.Errorf("report is missing %q:\n%s", want, output)
				}
			}
			if strings.Contains(output, "/synthetic/private") {
				t.Fatalf("report repeats ccusage output:\n%s", output)
			}
			if report.ExitCode() != test.exitCode {
				t.Fatalf("ExitCode() = %d, want %d", report.ExitCode(), test.exitCode)
			}
		})
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
