package autosync

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeTools stands in for launchctl and systemctl. It keeps one fact, whether
// the schedule is loaded, and records every command.
type fakeTools struct {
	loaded bool
	// failure, when set, is what every command reports instead of running.
	failure func() ([]byte, int, error)
	calls   [][]string
}

func (f *fakeTools) run(_ context.Context, name string, arguments ...string) ([]byte, int, error) {
	f.calls = append(f.calls, append([]string{name}, arguments...))
	if f.failure != nil {
		return f.failure()
	}
	has := func(word string) bool { return slices.Contains(arguments, word) }
	switch {
	case name == "launchctl" && has("print"):
		if !f.loaded {
			return []byte("Could not find service"), 113, nil
		}
	case name == "launchctl" && has("bootstrap"), name == "systemctl" && has("enable"):
		f.loaded = true
	case name == "launchctl" && has("bootout"), name == "systemctl" && (has("disable") || has("stop")):
		f.loaded = false
	case name == "systemctl" && has("is-active"):
		if !f.loaded {
			return []byte("inactive\n"), 3, nil
		}
	}
	return nil, 0, nil
}

func (f *fakeTools) ran(words ...string) bool {
	for _, call := range f.calls {
		if strings.Contains(strings.Join(call, " "), strings.Join(words, " ")) {
			return true
		}
	}
	return false
}

func syntheticExecutable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tokenholic")
	if err := os.WriteFile(path, []byte("synthetic executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// schedulers builds each real scheduler over a synthetic home and fake tools.
func schedulers(home string, tools *fakeTools) map[string]scheduler {
	return map[string]scheduler{
		"launchd": launchd{home: home, uid: 501, run: tools.run},
		"systemd": newSystemd(home, func(string) string { return "" }, tools.run),
	}
}

func TestManagerInstallsInspectsAndRemovesSchedule(t *testing.T) {
	for _, name := range []string{"launchd", "systemd"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			tools := &fakeTools{}
			manager, err := NewManager(Config{
				Home: home, Executable: syntheticExecutable(t), APIURL: "https://api.example.test",
				CodexHome: filepath.Join(home, "codex"), scheduler: schedulers(home, tools)[name],
			})
			if err != nil {
				t.Fatal(err)
			}
			if status, err := manager.Status(t.Context()); err != nil || status != (Status{}) {
				t.Fatalf("Status() before enabling = %#v, %v", status, err)
			}
			if err := manager.Enable(t.Context()); err != nil {
				t.Fatal(err)
			}
			paths := manager.scheduler.layout()
			if binary, err := os.ReadFile(paths.binary); err != nil || string(binary) != "synthetic executable" {
				t.Fatalf("installed binary = %q, %v", binary, err)
			}
			if !strings.HasPrefix(paths.binary, home) || !strings.HasPrefix(paths.logs, home) {
				t.Fatalf("layout escapes the home directory: %#v", paths)
			}
			for _, definition := range paths.definitions {
				contents, err := os.ReadFile(definition)
				if err != nil {
					t.Fatal(err)
				}
				if info, err := os.Stat(definition); err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("definition mode = %v, %v", info, err)
				}
				// Autosync stores no credentials, on any scheduler.
				for _, forbidden := range []string{"credential", "Bearer", "token="} {
					if strings.Contains(string(contents), forbidden) {
						t.Fatalf("definition mentions %q: %s", forbidden, contents)
					}
				}
			}
			for _, log := range []string{paths.stdout, paths.stderr} {
				if info, err := os.Stat(log); err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("log mode = %v, %v", info, err)
				}
			}
			for _, directory := range []string{filepath.Dir(paths.binary), paths.logs} {
				if info, err := os.Stat(directory); err != nil || info.Mode().Perm() != 0o700 {
					t.Fatalf("private directory mode = %v, %v", info, err)
				}
			}
			if status, err := manager.Status(t.Context()); err != nil || !status.Installed || !status.Loaded {
				t.Fatalf("Status() = %#v, %v", status, err)
			}

			// Enabling again replaces the loaded schedule instead of stacking one.
			before := len(tools.calls)
			if err := manager.Enable(t.Context()); err != nil {
				t.Fatal(err)
			}
			if again := tools.calls[before:]; len(again) < 3 {
				t.Fatalf("re-enable did not unload and load: %#v", again)
			}

			if err := os.Remove(paths.binary); err != nil {
				t.Fatal(err)
			}
			if status, err := manager.Status(t.Context()); err != nil || status.Installed || !status.Loaded {
				t.Fatalf("incomplete Status() = %#v, %v", status, err)
			}
			if err := manager.Disable(t.Context()); err != nil {
				t.Fatal(err)
			}
			owned := append([]string{paths.binary, paths.stdout, paths.stderr, paths.logs, filepath.Dir(paths.binary)}, paths.definitions...)
			for _, path := range owned {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("autosync-owned path remains: %v", err)
				}
			}
			if status, err := manager.Status(t.Context()); err != nil || status != (Status{}) {
				t.Fatalf("Status() after disabling = %#v, %v", status, err)
			}
		})
	}
}

func TestSchedulerCommands(t *testing.T) {
	home := t.TempDir()
	for _, test := range []struct {
		scheduler string
		enable    [][]string
		disable   [][]string
	}{
		{
			scheduler: "launchd",
			enable:    [][]string{{"launchctl", "print", "gui/501/" + launchLabel}, {"launchctl", "bootstrap", "gui/501"}},
			disable:   [][]string{{"launchctl", "bootout", "gui/501/" + launchLabel}},
		},
		{
			scheduler: "systemd",
			enable: [][]string{
				{"systemctl", "--user", "is-active", systemdTimer},
				{"systemctl", "--user", "daemon-reload"},
				{"systemctl", "--user", "enable", "--now", systemdTimer},
			},
			disable: [][]string{
				{"systemctl", "--user", "disable", "--now", systemdTimer},
				{"systemctl", "--user", "daemon-reload"},
				{"systemctl", "--user", "reset-failed", systemdService, systemdTimer},
			},
		},
	} {
		t.Run(test.scheduler, func(t *testing.T) {
			tools := &fakeTools{}
			manager, err := NewManager(Config{
				Home: home, Executable: syntheticExecutable(t), APIURL: "https://api.example.test",
				scheduler: schedulers(home, tools)[test.scheduler],
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.Enable(t.Context()); err != nil {
				t.Fatal(err)
			}
			for _, want := range test.enable {
				if !tools.ran(want...) {
					t.Fatalf("enable did not run %q: %#v", want, tools.calls)
				}
			}
			tools.calls = nil
			if err := manager.Disable(t.Context()); err != nil {
				t.Fatal(err)
			}
			for _, want := range test.disable {
				if !tools.ran(want...) {
					t.Fatalf("disable did not run %q: %#v", want, tools.calls)
				}
			}
		})
	}
}

func TestManagerRejectsInvalidAPIOrigins(t *testing.T) {
	home := t.TempDir()
	for _, apiURL := range []string{"", "http://remote.example.test", "https://user:secret@api.example.test", "https://api.example.test?x=1"} {
		tools := &fakeTools{}
		manager, err := NewManager(Config{
			Home: home, Executable: syntheticExecutable(t), APIURL: apiURL,
			scheduler: schedulers(home, tools)["systemd"],
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.Enable(t.Context()); err == nil {
			t.Fatalf("Enable(%q) succeeded", apiURL)
		}
		if len(tools.calls) != 0 {
			t.Fatalf("Enable(%q) reached the scheduler: %#v", apiURL, tools.calls)
		}
	}
}

func TestManagerRejectsRelativePaths(t *testing.T) {
	for _, config := range []Config{
		{Home: "relative", Executable: "/synthetic/tokenholic"},
		{Home: t.TempDir(), Executable: "tokenholic"},
		{Home: t.TempDir(), Executable: syntheticExecutable(t), CodexHome: "codex"},
	} {
		if _, err := NewManager(config); err == nil {
			t.Fatalf("NewManager(%#v) succeeded", config)
		}
	}
}

func TestUnreachableSchedulerInstallsNothingButStillCleansUp(t *testing.T) {
	for name, failure := range map[string]func() ([]byte, int, error){
		"no systemctl":   func() ([]byte, int, error) { return nil, -1, exec.ErrNotFound },
		"no session bus": func() ([]byte, int, error) { return []byte("Failed to connect to bus: No medium found\n"), 1, nil },
		"systemd not pid": func() ([]byte, int, error) {
			return []byte("System has not been booted with systemd as init system"), 1, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			tools := &fakeTools{failure: failure}
			manager, err := NewManager(Config{
				Home: home, Executable: syntheticExecutable(t), APIURL: "https://api.example.test",
				scheduler: schedulers(home, tools)["systemd"],
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.Enable(t.Context()); !errors.Is(err, ErrSchedulerUnavailable) {
				t.Fatalf("Enable() = %v, want the scheduler reported unavailable", err)
			}
			if _, err := manager.Status(t.Context()); !errors.Is(err, ErrSchedulerUnavailable) {
				t.Fatalf("Status() = %v, want the scheduler reported unavailable", err)
			}
			paths := manager.scheduler.layout()
			for _, path := range append([]string{paths.binary}, paths.definitions...) {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("Enable() wrote a file without a scheduler: %v", err)
				}
			}

			// Files left by an earlier install are removable without it.
			for _, path := range append([]string{paths.binary}, paths.definitions...) {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("synthetic"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := manager.Disable(t.Context()); err != nil {
				t.Fatal(err)
			}
			for _, path := range append([]string{paths.binary}, paths.definitions...) {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("Disable() left a file: %v", err)
				}
			}
		})
	}
}

func TestManagerRecognizesAndRemovesLegacyInstallation(t *testing.T) {
	home := t.TempDir()
	tools := &fakeTools{loaded: true}
	manager, err := NewManager(Config{
		Home: home, Executable: syntheticExecutable(t), APIURL: "https://api.example.test",
		scheduler: schedulers(home, tools)["launchd"],
	})
	if err != nil {
		t.Fatal(err)
	}
	paths := manager.scheduler.layout()
	legacy := paths.legacy[0]
	for path, mode := range map[string]os.FileMode{legacy.binary: 0o700, paths.definitions[0]: 0o600} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("legacy"), mode); err != nil {
			t.Fatal(err)
		}
	}
	status, err := manager.Status(t.Context())
	if err != nil || !status.Installed || !status.Loaded {
		t.Fatalf("Status() = %#v, %v", status, err)
	}
	if err := manager.Disable(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy.binary); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy binary still exists: %v", err)
	}
}

func TestDisableKeepsFilesAutosyncDoesNotOwn(t *testing.T) {
	home := t.TempDir()
	tools := &fakeTools{}
	manager, err := NewManager(Config{
		Home: home, Executable: syntheticExecutable(t), APIURL: "https://api.example.test",
		scheduler: schedulers(home, tools)["systemd"],
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(t.Context()); err != nil {
		t.Fatal(err)
	}
	paths := manager.scheduler.layout()
	// Another unit beside ours and a file beside the stable binary's directory.
	neighbors := []string{
		filepath.Join(filepath.Dir(paths.definitions[0]), "another.service"),
		filepath.Join(filepath.Dir(filepath.Dir(paths.binary)), "kept.txt"),
	}
	for _, neighbor := range neighbors {
		if err := os.WriteFile(neighbor, []byte("synthetic"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Disable(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, neighbor := range neighbors {
		if _, err := os.Stat(neighbor); err != nil {
			t.Fatalf("Disable() removed a file it does not own: %v", err)
		}
	}
}

func TestUnsupportedSchedulerReportsItself(t *testing.T) {
	manager, err := NewManager(Config{
		Home: t.TempDir(), Executable: syntheticExecutable(t), APIURL: "https://api.example.test",
		scheduler: unsupported{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Enable(t.Context()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Enable() = %v", err)
	}
	if _, err := manager.Status(t.Context()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Status() = %v", err)
	}
}
