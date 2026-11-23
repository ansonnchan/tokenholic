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
	claudeDesktopAt := func(dataFolder string, optional bool) []wantRoot {
		return []wantRoot{
			{RootClaudeDesktopCode, normalize.SurfaceDesktopCode, at(dataFolder, "claude-code-sessions"), optional},
			{RootClaudeDesktopAgent, normalize.SurfaceDesktopAgent, at(dataFolder, "local-agent-mode-sessions"), optional},
			{RootClaudeDesktopCowork, normalize.SurfaceDesktopCowork, at(dataFolder, "local-agent-mode-sessions"), optional},
		}
	}
	claudeDesktop := func(dataFolder string) []wantRoot { return claudeDesktopAt(dataFolder, false) }
	packaged := func(localAppData string) []wantRoot {
		return claudeDesktopAt(at(localAppData, "Packages", "Claude_pzs8sxrjxfjjc", "LocalCache", "Roaming", "Claude"), true)
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
		{
			name:  "Windows defaults",
			roots: WindowsRoots(home, "", "", ""),
			want: join([]wantRoot{claudeCLI}, claudeDesktop(at(home, "AppData", "Roaming", "Claude")),
				packaged(at(home, "AppData", "Local")), sharedCodex),
		},
		{
			name:  "Windows with redirected application data",
			roots: WindowsRoots(home, "", at(custom, "Roaming"), at(custom, "Local")),
			want: join([]wantRoot{claudeCLI}, claudeDesktop(at(custom, "Roaming", "Claude")),
				packaged(at(custom, "Local")), sharedCodex),
		},
		{
			name:  "Windows ignores relative application data variables",
			roots: WindowsRoots(home, "", "Roaming", "Local"),
			want: join([]wantRoot{claudeCLI}, claudeDesktop(at(home, "AppData", "Roaming", "Claude")),
				packaged(at(home, "AppData", "Local")), sharedCodex),
		},
		{
			// The Codex app shares the default home with the native CLI.
			name:  "Windows with a separate CODEX_HOME",
			roots: WindowsRoots(home, custom, "", ""),
			want: join([]wantRoot{claudeCLI}, claudeDesktop(at(home, "AppData", "Roaming", "Claude")),
				packaged(at(home, "AppData", "Local")),
				codex(RootCodexCLI, normalize.SurfaceCLI, custom), codex(RootCodexDesktop, normalize.SurfaceDesktop, at(home, ".codex"))),
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

func TestWindowsRootsDiscoverBothClaudeDesktopInstalls(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	roaming := filepath.Join(home, "AppData", "Roaming", "Claude")
	packaged := filepath.Join(home, "AppData", "Local", "Packages", "Claude_pzs8sxrjxfjjc", "LocalCache", "Roaming", "Claude")
	discover := func() []Finding {
		return (Discoverer{Roots: WindowsRoots(home, "", "", "")}).Discover(context.Background())
	}
	count := func(findings []Finding, kind RootKind, status normalize.HealthStatus) int {
		total := 0
		for _, finding := range findings {
			if finding.Kind == kind && finding.Status == status {
				total++
			}
		}
		return total
	}

	// Nothing installed: each source answers once, as not found.
	findings := discover()
	if len(findings) != 5 || count(findings, RootClaudeDesktopCowork, normalize.HealthNotFound) != 1 {
		t.Fatalf("empty profile findings = %d", len(findings))
	}

	writeSyntheticFile(t, filepath.Join(home, ".claude", "projects", "synthetic", "session.jsonl"))
	writeSyntheticFile(t, filepath.Join(home, ".codex", "sessions", "2026", "01", "02", "rollout-synthetic.jsonl"))
	writeSyntheticFile(t, filepath.Join(roaming, "local-agent-mode-sessions", "synthetic", "local_synthetic", "audit.jsonl"))
	writeSyntheticFile(t, filepath.Join(packaged, "local-agent-mode-sessions", "synthetic", "local_synthetic", "audit.jsonl"))
	writeSyntheticFile(t, filepath.Join(packaged, "claude-code-sessions", "synthetic", "session.json"))
	// Chat storage beside them is never a candidate.
	chat := filepath.Join(packaged, "conversations", "ordinary-chat.jsonl")
	writeSyntheticFile(t, chat)

	findings = discover()
	if got := count(findings, RootClaudeDesktopCowork, normalize.HealthDetected); got != 2 {
		t.Errorf("Cowork roots detected = %d, want both installs", got)
	}
	if got := count(findings, RootClaudeDesktopCode, normalize.HealthDetected); got != 1 {
		t.Errorf("Code roots detected = %d, want the packaged install", got)
	}
	if count(findings, RootClaudeCLI, normalize.HealthDetected) != 1 || count(findings, RootCodexShared, normalize.HealthDetected) != 1 {
		t.Errorf("Claude Code or Codex was not detected under the user profile")
	}
	for _, finding := range findings {
		for _, candidate := range finding.Candidates {
			if candidate.Path == chat {
				t.Errorf("%s discovered ordinary desktop chat", finding.Kind)
			}
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
	case "windows":
		want = WindowsRoots(home, "", "", "")
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

func TestClaudeConfigDirMovesTheClaudeCodeRoot(t *testing.T) {
	t.Parallel()

	home := filepath.Join(t.TempDir(), "home")
	moved := filepath.Join(t.TempDir(), "moved")
	earlier := filepath.Join(home, ".config", "claude")
	projects := func(directory string) string { return filepath.Join(directory, "projects") }
	type root struct {
		path     string
		optional bool
	}
	defaultOnly := []root{{projects(filepath.Join(home, ".claude")), false}}
	for _, test := range []struct {
		name  string
		roots []RootSpec
		value string
		want  []root
	}{
		{name: "unset", roots: MacRoots(home, ""), want: defaultOnly},
		{name: "relative", roots: MacRoots(home, ""), value: filepath.Join("claude", "moved"), want: defaultOnly},
		{name: "the default itself", roots: MacRoots(home, ""), value: filepath.Join(home, ".claude") + string(filepath.Separator), want: defaultOnly},
		{
			name: "moved", roots: WindowsRoots(home, "", "", ""), value: moved,
			want: []root{{projects(moved), false}, {projects(filepath.Join(home, ".claude")), true}},
		},
		{
			// Linux already reads this directory as optional; chosen by the
			// user it is the source, and it is listed once.
			name: "moved to a directory already read", roots: LinuxRoots(home, "", ""), value: earlier,
			want: []root{{projects(earlier), false}, {projects(filepath.Join(home, ".claude")), true}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := withClaudeConfigDir(test.roots, home, test.value)
			var cli []root
			others := 0
			for _, spec := range got {
				if spec.Kind == RootClaudeCLI {
					cli = append(cli, root{spec.Path, spec.Optional})
				} else {
					others++
				}
			}
			if len(cli) != len(test.want) {
				t.Fatalf("Claude Code roots = %v, want %v", cli, test.want)
			}
			for index, want := range test.want {
				if cli[index] != want {
					t.Errorf("Claude Code root %d = %v, want %v", index, cli[index], want)
				}
			}
			// No other source moves with it.
			before := 0
			for _, spec := range test.roots {
				if spec.Kind != RootClaudeCLI {
					before++
				}
			}
			if others != before {
				t.Errorf("other roots = %d, want %d", others, before)
			}
		})
	}
}

func TestDefaultRootsFindSessionsInAMovedClaudeConfigDir(t *testing.T) {
	t.Parallel()
	if len(DefaultRoots(Environment{Home: t.TempDir()})) == 0 {
		t.Skip("this system has no default roots")
	}

	home, moved := t.TempDir(), t.TempDir()
	// The variable names the configuration directory; the second variable
	// Claude Code documents only names a directory inside its projects.
	writeSyntheticFile(t, filepath.Join(moved, "projects", "synthetic-name", "session.jsonl"))
	discover := func() []Finding {
		roots := DefaultRoots(Environment{Home: home, Getenv: func(name string) string {
			if name == ClaudeConfigDirVariable {
				return moved
			}
			return ""
		}})
		var cli []Finding
		for _, finding := range (Discoverer{Roots: roots}).Discover(context.Background()) {
			if finding.Kind == RootClaudeCLI {
				cli = append(cli, finding)
			}
		}
		return cli
	}

	// The default directory does not exist, and that is not a missing source.
	cli := discover()
	if len(cli) != 1 || cli[0].Status != normalize.HealthDetected || cli[0].RecordCount != 1 {
		t.Fatalf("Claude Code findings = %#v, want the moved directory alone", cli)
	}
	// Sessions left in the default directory are still read.
	writeSyntheticFile(t, filepath.Join(home, ".claude", "projects", "synthetic", "earlier.jsonl"))
	cli = discover()
	if len(cli) != 2 || cli[1].Status != normalize.HealthDetected || cli[1].RecordCount != 1 {
		t.Fatalf("Claude Code findings with the default directory = %#v", cli)
	}
}
