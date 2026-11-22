package doctor

import (
	"context"
	"errors"
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
	report := Report{GOOS: "darwin", GOARCH: "arm64", Parser: ParserStatus{Found: true, Compatible: true}, Findings: findings}
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
	report := Diagnose(context.Background(), discovery, fakeParser{status: ParserStatus{Found: true, Compatible: true}}, nil)
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

func TestCCUsageProbeValidatesPinnedVersion(t *testing.T) {
	t.Parallel()

	probe := CCUsageProbe{
		LookPath: func(string) (string, error) { return "synthetic-ccusage", nil },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("ccusage " + ccusage.PinnedVersion), nil
		},
	}
	if status := probe.Check(context.Background()); !status.Found || !status.Compatible {
		t.Fatalf("Check() = %#v", status)
	}
	probe.Run = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("synthetic failure") }
	if status := probe.Check(context.Background()); !status.Found || status.Compatible || status.Issue != "execution_failed" {
		t.Fatalf("failed Check() = %#v", status)
	}
}

func TestCCUsageProbeReportsTimeout(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	probe := CCUsageProbe{
		LookPath: func(string) (string, error) { return "synthetic-ccusage", nil },
		Run:      func(context.Context, string, ...string) ([]byte, error) { return nil, context.DeadlineExceeded },
	}
	status := probe.Check(ctx)
	if status.Issue != "timeout" {
		t.Fatalf("Check() = %#v", status)
	}
}

func TestUnsupportedParserVersionIsAnIssue(t *testing.T) {
	t.Parallel()

	report := Report{GOOS: "darwin", GOARCH: "arm64", Parser: ParserStatus{Found: true}}
	if report.ExitCode() != 1 {
		t.Fatalf("ExitCode() = %d, want 1", report.ExitCode())
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
