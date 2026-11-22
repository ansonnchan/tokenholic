package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ansonnchan/tokenholic/internal/adapters/ccusage"
	"github.com/ansonnchan/tokenholic/internal/doctor"
	"github.com/ansonnchan/tokenholic/internal/preferences"
)

func ccusageDependencies(t *testing.T, environment map[string]string) (dependencies, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tokenholic", "preferences.json")
	return dependencies{
		homeDir:     func() (string, error) { return syntheticHome, nil },
		getenv:      func(name string) string { return environment[name] },
		timeout:     time.Second,
		preferences: func() (string, error) { return path, nil },
	}, path
}

func TestCCUsageIsOnUntilTurnedOff(t *testing.T) {
	t.Parallel()
	deps, path := ccusageDependencies(t, nil)
	runCommand := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := run(append([]string{"ccusage"}, args...), &stdout, &stderr, deps)
		return code, stdout.String(), stderr.String()
	}
	if _, off := deps.ccusageRunner(); off {
		t.Fatal("ccusage is off before the user chose anything")
	}
	if code, stdout, _ := runCommand("status"); code != 0 || !strings.Contains(stdout, "ccusage is on") ||
		!strings.Contains(stdout, "Gemini CLI") || !strings.Contains(stdout, "tokenholic ccusage off") {
		t.Fatalf("status = %d/%q", code, stdout)
	}
	if code, stdout, stderr := runCommand("off"); code != 0 || !strings.Contains(stdout, "Turned ccusage off") {
		t.Fatalf("off = %d/%q/%q", code, stdout, stderr)
	}
	if _, off := deps.ccusageRunner(); !off {
		t.Fatal("ccusage is still on after `ccusage off`")
	}
	if code, stdout, _ := runCommand("status"); code != 0 || !strings.Contains(stdout, "ccusage is off") || !strings.Contains(stdout, "tokenholic ccusage on") {
		t.Fatalf("status after off = %d/%q", code, stdout)
	}
	// The choice must not disturb another saved preference.
	saved, err := preferences.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	saved.Theme = "dark"
	if err := preferences.Save(path, saved); err != nil {
		t.Fatal(err)
	}
	if code, stdout, stderr := runCommand("on"); code != 0 || !strings.Contains(stdout, "Turned ccusage on") {
		t.Fatalf("on = %d/%q/%q", code, stdout, stderr)
	}
	if _, off := deps.ccusageRunner(); off {
		t.Fatal("ccusage is still off after `ccusage on`")
	}
	if saved, err := preferences.Load(path); err != nil || saved.Theme != "dark" || saved.CCUsage != "" {
		t.Fatalf("preferences after on = %+v, %v", saved, err)
	}
	for _, args := range [][]string{{}, {"enable"}, {"on", "now"}} {
		if code, _, stderr := runCommand(args...); code != 2 || !strings.Contains(stderr, "on, off, or status") {
			t.Fatalf("ccusage %v = %d/%q", args, code, stderr)
		}
	}
}

func TestCCUsageStaysOffWhenTheChoiceCannotBeRead(t *testing.T) {
	t.Parallel()
	deps, path := ccusageDependencies(t, nil)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, off := deps.ccusageRunner(); !off {
		t.Fatal("an unreadable choice let ccusage run")
	}
	deps.preferences = func() (string, error) { return "", errors.New("synthetic failure") }
	if _, off := deps.ccusageRunner(); !off {
		t.Fatal("an unlocatable choice let ccusage run")
	}
}

func TestDoctorIsGivenTheRunnerTheLauncherNamed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		environment map[string]string
		off         bool
		state       doctor.RunnerState
	}{
		{name: "no launcher", state: doctor.RunnerUnavailable},
		{name: "a path that does not exist", environment: map[string]string{ccusagePathVariable: filepath.Join(syntheticHome, "absent", "ccusage")}, state: doctor.RunnerUnavailable},
		{name: "a file that is not the pinned build", environment: map[string]string{ccusagePathVariable: "unpinned"}, state: doctor.RunnerUnpinned},
		{name: "turned off", environment: map[string]string{ccusagePathVariable: "unpinned"}, off: true, state: doctor.RunnerOff},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			environment := map[string]string{}
			for name, value := range test.environment {
				if value == "unpinned" {
					value = filepath.Join(t.TempDir(), "ccusage")
					if err := os.WriteFile(value, []byte("another program"), 0o700); err != nil {
						t.Fatal(err)
					}
				}
				environment[name] = value
			}
			deps, path := ccusageDependencies(t, environment)
			if test.off {
				saved := preferences.Preferences{}
				saved.SetCCUsage(false)
				if err := preferences.Save(path, saved); err != nil {
					t.Fatal(err)
				}
			}
			deps.diagnose = func(ctx context.Context, _, _ string, parser doctor.ParserProbe) doctor.Report {
				return doctor.Report{GOOS: "darwin", GOARCH: "arm64", Parser: parser.Check(ctx)}
			}
			var stdout, stderr bytes.Buffer
			code := run([]string{"doctor"}, &stdout, &stderr, deps)
			wantCode := 0
			if test.state == doctor.RunnerUnpinned {
				wantCode = 1
			}
			if code != wantCode {
				t.Fatalf("doctor = %d/%q/%q", code, stdout.String(), stderr.String())
			}
			want := map[doctor.RunnerState]string{
				doctor.RunnerUnavailable: "ccusage: not available in this install",
				doctor.RunnerUnpinned:    "ccusage: refused",
				doctor.RunnerOff:         "ccusage: turned off",
			}[test.state]
			if !strings.Contains(stdout.String(), want) || strings.Contains(stdout.String(), syntheticHome) {
				t.Fatalf("doctor stdout = %q, want %q and no path", stdout.String(), want)
			}
			if !strings.Contains(stdout.String(), ccusage.AgentGemini.Label()) {
				t.Fatalf("doctor does not name the agents left uncounted: %q", stdout.String())
			}
		})
	}
}
