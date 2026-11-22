package adapters

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ansonnchan/tokenholic/internal/normalize"
)

type wantRoot struct {
	kind     RootKind
	surface  normalize.Surface
	path     string
	optional bool
}

func TestPlatformRootLayouts(t *testing.T) {
	t.Parallel()

	home := filepath.Join(t.TempDir(), "home")
	custom := filepath.Join(t.TempDir(), "custom")
	at := func(base string, parts ...string) string { return filepath.Join(append([]string{base}, parts...)...) }
	claudeDesktop := func(dataFolder string) []wantRoot {
		return []wantRoot{
			{RootClaudeDesktopCode, normalize.SurfaceDesktopCode, at(dataFolder, "claude-code-sessions"), false},
			{RootClaudeDesktopAgent, normalize.SurfaceDesktopAgent, at(dataFolder, "local-agent-mode-sessions"), false},
			{RootClaudeDesktopCowork, normalize.SurfaceDesktopCowork, at(dataFolder, "local-agent-mode-sessions"), false},
		}
	}
	codex := func(kind RootKind, surface normalize.Surface, codexHome string) []wantRoot {
		return []wantRoot{
			{kind, surface, at(codexHome, "sessions"), false},
			{kind, surface, at(codexHome, "archived_sessions"), true},
		}
	}
	join := func(groups ...[]wantRoot) []wantRoot {
		var all []wantRoot
		for _, group := range groups {
			all = append(all, group...)
		}
		return all
	}
	claudeCLI := wantRoot{RootClaudeCLI, normalize.SurfaceCLI, at(home, ".claude", "projects"), false}
	sharedCodex := codex(RootCodexShared, normalize.SurfaceSharedLocal, at(home, ".codex"))

	for _, test := range []struct {
		name  string
		roots []RootSpec
		want  []wantRoot
	}{
		{
			name:  "macOS defaults",
			roots: MacRoots(home, ""),
			want:  join([]wantRoot{claudeCLI}, claudeDesktop(at(home, "Library", "Application Support", "Claude")), sharedCodex),
		},
		{
			name:  "macOS with CODEX_HOME naming the default home",
			roots: MacRoots(home, at(home, ".codex")+string(filepath.Separator)),
			want:  join([]wantRoot{claudeCLI}, claudeDesktop(at(home, "Library", "Application Support", "Claude")), sharedCodex),
		},
		{
			name:  "macOS with a separate CODEX_HOME",
			roots: MacRoots(home, custom),
			want: join([]wantRoot{claudeCLI}, claudeDesktop(at(home, "Library", "Application Support", "Claude")),
				codex(RootCodexCLI, normalize.SurfaceCLI, custom), codex(RootCodexDesktop, normalize.SurfaceDesktop, at(home, ".codex"))),
		},
		{
			name:  "Linux defaults",
			roots: LinuxRoots(home, "", ""),
			want: join([]wantRoot{claudeCLI, {RootClaudeCLI, normalize.SurfaceCLI, at(home, ".config", "claude", "projects"), true}},
				claudeDesktop(at(home, ".config", "Claude")), sharedCodex),
		},
		{
			name:  "Linux with XDG_CONFIG_HOME",
			roots: LinuxRoots(home, "", custom),
			want: join([]wantRoot{claudeCLI, {RootClaudeCLI, normalize.SurfaceCLI, at(custom, "claude", "projects"), true}},
				claudeDesktop(at(custom, "Claude")), sharedCodex),
		},
		{
			name:  "Linux ignores a relative XDG_CONFIG_HOME",
			roots: LinuxRoots(home, "", "relative/config"),
			want: join([]wantRoot{claudeCLI, {RootClaudeCLI, normalize.SurfaceCLI, at(home, ".config", "claude", "projects"), true}},
				claudeDesktop(at(home, ".config", "Claude")), sharedCodex),
		},
		{
			// Nothing proves which Codex surface writes the default home on
			// Linux, so it stays unattributed beside the CLI's own home.
			name:  "Linux with a separate CODEX_HOME",
			roots: LinuxRoots(home, custom, ""),
			want: join([]wantRoot{claudeCLI, {RootClaudeCLI, normalize.SurfaceCLI, at(home, ".config", "claude", "projects"), true}},
				claudeDesktop(at(home, ".config", "Claude")),
				codex(RootCodexCLI, normalize.SurfaceCLI, custom), sharedCodex),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if len(test.roots) != len(test.want) {
				t.Fatalf("roots = %d, want %d", len(test.roots), len(test.want))
			}
			for index, want := range test.want {
				got := test.roots[index]
				if got.Kind != want.kind || got.Surface != want.surface || got.Path != want.path || got.Optional != want.optional {
					t.Errorf("root %d = %s/%s optional=%t at %q, want %s/%s optional=%t at %q", index,
						got.Kind, got.Surface, got.Optional, got.Path, want.kind, want.surface, want.optional, want.path)
				}
				if got.Format == "" || len(got.Extensions) == 0 || got.Capabilities.Model == "" {
					t.Errorf("root %d is missing its format, extensions, or capabilities", index)
				}
			}
		})
	}
}

func TestLinuxRootsDiscoverSupportedSurfaces(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	config := filepath.Join(home, ".config")
	for _, path := range []string{
		filepath.Join(home, ".claude", "projects", "synthetic", "session.jsonl"),
		filepath.Join(config, "Claude", "claude-code-sessions", "synthetic", "session.json"),
		filepath.Join(config, "Claude", "local-agent-mode-sessions", "synthetic", "rpm", "agent.jsonl"),
		filepath.Join(config, "Claude", "local-agent-mode-sessions", "synthetic", "local_synthetic", "audit.jsonl"),
		filepath.Join(home, ".codex", "sessions", "2026", "01", "02", "rollout-synthetic.jsonl"),
	} {
		writeSyntheticFile(t, path)
	}
	// Files a narrow root must never pick up: chat storage in the desktop
	// data folder, and Claude Code's own settings beside its transcripts.
	ignored := []string{
		filepath.Join(config, "Claude", "conversations", "ordinary-chat.jsonl"),
		filepath.Join(config, "Claude", "claude_desktop_config.json"),
		filepath.Join(home, ".claude", "history.jsonl"),
		filepath.Join(home, ".codex", "history.jsonl"),
	}
	for _, path := range ignored {
		writeSyntheticFile(t, path)
	}

	discover := func() []Finding {
		return (Discoverer{Roots: LinuxRoots(home, "", "")}).Discover(context.Background())
	}
	findings := discover()
	// The directory Claude Code 1.0.28 to 1.0.30 used is absent, and silent.
	if len(findings) != 5 {
		t.Fatalf("Discover() findings = %d, want 5", len(findings))
	}
	for _, finding := range findings {
		if finding.Status != normalize.HealthDetected || finding.RecordCount != 1 {
			t.Errorf("%s = %s with %d records, want one detected", finding.Kind, finding.Status, finding.RecordCount)
		}
		for _, candidate := range finding.Candidates {
			for _, path := range ignored {
				if candidate.Path == path {
					t.Errorf("%s discovered a file outside its contract", finding.Kind)
				}
			}
		}
	}

	writeSyntheticFile(t, filepath.Join(config, "claude", "projects", "synthetic", "earlier.jsonl"))
	var cli []Finding
	for _, finding := range discover() {
		if finding.Kind == RootClaudeCLI {
			cli = append(cli, finding)
		}
	}
	if len(cli) != 2 || cli[1].Status != normalize.HealthDetected || cli[1].RecordCount != 1 {
		t.Fatalf("Claude Code findings with the earlier directory = %#v", cli)
	}
}

func TestMissingLinuxSourcesAreReportedNotFound(t *testing.T) {
	t.Parallel()

	findings := (Discoverer{Roots: LinuxRoots(t.TempDir(), "", "")}).Discover(context.Background())
	// Every primary root answers; only the two optional ones stay silent.
	if len(findings) != 5 {
		t.Fatalf("Discover() findings = %d, want 5", len(findings))
	}
	for _, finding := range findings {
		if finding.Status != normalize.HealthNotFound || len(finding.Candidates) != 0 {
			t.Errorf("%s = %s, want not found", finding.Kind, finding.Status)
		}
	}
}

func TestDefaultRootsMatchThePlatform(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	custom := t.TempDir()
	roots := DefaultRoots(Environment{Home: home, Getenv: func(name string) string {
		if name == "XDG_CONFIG_HOME" {
			return custom
		}
		return ""
	}})
	var want []RootSpec
	switch runtime.GOOS {
	case "darwin":
		want = MacRoots(home, "")
	case "linux":
		want = LinuxRoots(home, "", custom)
	}
	if len(roots) != len(want) {
		t.Fatalf("DefaultRoots() = %d roots, want %d", len(roots), len(want))
	}
	for index := range want {
		if roots[index].Kind != want[index].Kind || roots[index].Path != want[index].Path {
			t.Errorf("root %d = %s at %q", index, roots[index].Kind, roots[index].Path)
		}
	}
	// A nil lookup reads as an empty environment rather than panicking.
	if got := DefaultRoots(Environment{Home: home}); len(got) != len(want) {
		t.Fatalf("DefaultRoots() without a lookup = %d roots", len(got))
	}
}
