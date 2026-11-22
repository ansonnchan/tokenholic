package adapters

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ansonnchan/tokenholic/internal/normalize"
)

func TestDayLevelRootsFollowEachAgentsDocumentedLocation(t *testing.T) {
	t.Parallel()
	// Real temporary folders, so the paths are absolute on every system.
	home := filepath.Join(t.TempDir(), "home")
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	paths := func(env Environment) map[normalize.Source][]string {
		found := make(map[normalize.Source][]string)
		for _, root := range DayLevelRoots(env) {
			found[root.Source] = root.Paths
		}
		return found
	}
	tests := []struct {
		name string
		env  map[string]string
		want map[normalize.Source][]string
	}{
		{name: "defaults", want: map[normalize.Source][]string{
			normalize.SourceCopilot:  {filepath.Join(home, ".copilot", "session-state"), filepath.Join(home, ".copilot", "otel")},
			normalize.SourceGemini:   {filepath.Join(home, ".gemini", "tmp")},
			normalize.SourceGrok:     {filepath.Join(home, ".grok", "sessions")},
			normalize.SourceOpenCode: {filepath.Join(home, ".local", "share", "opencode")},
			normalize.SourceQwen:     {filepath.Join(home, ".qwen", "projects")},
		}},
		{name: "documented overrides", env: map[string]string{
			"COPILOT_HOME": filepath.Join(elsewhere, "copilot"), "GROK_HOME": filepath.Join(elsewhere, "grok"),
			"XDG_DATA_HOME": filepath.Join(elsewhere, "data"),
			// ccusage's own data-directory variables are not honored.
			"GEMINI_DATA_DIR": elsewhere, "QWEN_DATA_DIR": elsewhere, "OPENCODE_DATA_DIR": elsewhere,
		}, want: map[normalize.Source][]string{
			normalize.SourceCopilot:  {filepath.Join(elsewhere, "copilot", "session-state"), filepath.Join(elsewhere, "copilot", "otel")},
			normalize.SourceGemini:   {filepath.Join(home, ".gemini", "tmp")},
			normalize.SourceGrok:     {filepath.Join(elsewhere, "grok", "sessions")},
			normalize.SourceOpenCode: {filepath.Join(elsewhere, "data", "opencode")},
			normalize.SourceQwen:     {filepath.Join(home, ".qwen", "projects")},
		}},
		{name: "a relative override is ignored", env: map[string]string{"GROK_HOME": "grok", "XDG_DATA_HOME": "data", "COPILOT_HOME": "."},
			want: map[normalize.Source][]string{
				normalize.SourceCopilot:  {filepath.Join(home, ".copilot", "session-state"), filepath.Join(home, ".copilot", "otel")},
				normalize.SourceGemini:   {filepath.Join(home, ".gemini", "tmp")},
				normalize.SourceGrok:     {filepath.Join(home, ".grok", "sessions")},
				normalize.SourceOpenCode: {filepath.Join(home, ".local", "share", "opencode")},
				normalize.SourceQwen:     {filepath.Join(home, ".qwen", "projects")},
			}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := paths(Environment{Home: home, Getenv: func(name string) string { return test.env[name] }})
			if len(got) != len(normalize.DayLevelSources()) {
				t.Fatalf("roots cover %d sources, want every day-level source", len(got))
			}
			for source, want := range test.want {
				if len(got[source]) != len(want) {
					t.Fatalf("%s paths = %q, want %q", source, got[source], want)
				}
				for index := range want {
					if got[source][index] != want[index] {
						t.Errorf("%s path %d = %q, want %q", source, index, got[source][index], want[index])
					}
				}
			}
		})
	}
	// A nil lookup means every variable is unset.
	if got := paths(Environment{Home: home}); got[normalize.SourceGrok][0] != filepath.Join(home, ".grok", "sessions") {
		t.Fatalf("without a lookup, Grok root = %q", got[normalize.SourceGrok])
	}
}

func TestAgentRootStatus(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	present := filepath.Join(directory, "present")
	if err := os.Mkdir(present, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "file")
	if err := os.WriteFile(file, []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	absent := filepath.Join(directory, "absent")
	tests := []struct {
		name  string
		paths []string
		want  normalize.HealthStatus
	}{
		{name: "the folder exists", paths: []string{present}, want: normalize.HealthDetected},
		{name: "one of two folders exists", paths: []string{absent, present}, want: normalize.HealthDetected},
		{name: "no folder exists", paths: []string{absent, filepath.Join(absent, "deeper")}, want: normalize.HealthNotFound},
		{name: "a file where the folder would be", paths: []string{file}, want: normalize.HealthNotFound},
		{name: "no location at all", want: normalize.HealthNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := (AgentRoot{Source: normalize.SourceGemini, Paths: test.paths}).Status(); got != test.want {
				t.Fatalf("Status() = %q, want %q", got, test.want)
			}
		})
	}
}
