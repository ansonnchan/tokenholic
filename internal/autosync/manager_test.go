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
	"time"

	"github.com/ansonnchan/tokenholic/internal/privatefs"
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
	case name == "schtasks" && has("/Query"):
		if !f.loaded {
			return nil, 1, nil
		}
	case name == "schtasks" && has("/Create"):
		f.loaded = true
	case name == "schtasks" && has("/Delete"):
		f.loaded = false
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
		"task scheduler": taskScheduler{
			name: taskName, root: filepath.Join(home, "AppData", "Local"), userID: "S-1-5-21-1-2-3-1001",
			now: func() time.Time { return time.Date(2026, 11, 21, 20, 30, 0, 0, time.UTC) }, run: tools.run,
		},
	}
}

func TestManagerInstallsInspectsAndRemovesSchedule(t *testing.T) {
	for _, name := range []string{"launchd", "systemd", "task scheduler"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			tools := &fakeTools{}
			manager, err := NewManager(Config{
				Home: home, Executable: syntheticExecutable(t), APIURL: "https://api.example.test",
				CodexHome: filepath.Join(home, "codex"), CCUsagePath: filepath.Join(home, "synthetic-ccusage"),
				scheduler: schedulers(home, tools)[name],
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
			var scheduled strings.Builder
			for _, definition := range paths.definitions {
				contents, err := os.ReadFile(definition)
				if err != nil {
					t.Fatal(err)
				}
				scheduled.Write(contents)
				if private, err := privatefs.IsPrivate(definition); err != nil || !private {
					t.Fatalf("definition is not private: %t, %v", private, err)
				}
				// Autosync stores no credentials, on any scheduler.
				for _, forbidden := range []string{"credential", "Bearer", "token="} {
					if strings.Contains(string(contents), forbidden) {
						t.Fatalf("definition mentions %q: %s", forbidden, contents)
					}
				}
			}
			// A scheduled run does not start through the npm launcher, so
			// the schedule itself says where the pinned ccusage is.
			if !strings.Contains(scheduled.String(), CCUsagePathVariable) || !strings.Contains(scheduled.String(), "synthetic-ccusage") {
				t.Fatalf("the schedule does not carry the ccusage location: %s", scheduled.String())
			}
			for _, private := range []string{paths.stdout, paths.stderr, filepath.Dir(paths.binary), paths.logs} {
				if ok, err := privatefs.IsPrivate(private); err != nil || !ok {
					t.Fatalf("%s is not private: %t, %v", filepath.Base(private), ok, err)
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
		{
			scheduler: "task scheduler",
			enable: [][]string{
				{"schtasks", "/Query", "/TN", taskName},
				{"schtasks", "/Create", "/TN", taskName, "/XML"},
				{"schtasks", "/Run", "/TN", taskName},
			},
			disable: [][]string{
				{"schtasks", "/End", "/TN", taskName},
				{"schtasks", "/Delete", "/TN", taskName, "/F"},
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

// The schedule names the API address only when the user chose one. Without
// that, the job uses the address built into the executable it runs, so
// re-enabling after an upgrade follows a moved service.
func TestScheduleCarriesTheAPIAddressOnlyWhenPinned(t *testing.T) {
	for _, name := range []string{"launchd", "systemd", "task scheduler"} {
		for _, pinned := range []bool{false, true} {
			home := t.TempDir()
			manager, err := NewManager(Config{
				Home: home, Executable: syntheticExecutable(t), APIURL: "https://api.example.test", PinAPIURL: pinned,
				scheduler: schedulers(home, &fakeTools{})[name],
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.Enable(t.Context()); err != nil {
				t.Fatal(err)
			}
			carried := false
			for _, definition := range manager.scheduler.layout().definitions {
				contents, err := os.ReadFile(definition)
				if err != nil {
					t.Fatal(err)
				}
				// The Windows task file is UTF-16; its job file is what would
				// hold the variable.
				carried = carried || strings.Contains(string(contents), "TOKENHOLIC_API_URL")
			}
			if carried != pinned {
				t.Errorf("%s pinned=%t: schedule carries the address = %t", name, pinned, carried)
			}
		}
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
		{Home: t.TempDir(), Executable: syntheticExecutable(t), CCUsagePath: "ccusage"},
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

func TestMissingSchedulerToolIsReportedOnEverySystem(t *testing.T) {
	home := t.TempDir()
	for _, name := range []string{"launchd", "systemd", "task scheduler"} {
		tools := &fakeTools{failure: func() ([]byte, int, error) { return nil, -1, exec.ErrNotFound }}
		manager, err := NewManager(Config{
			Home: home, Executable: syntheticExecutable(t), APIURL: "https://api.example.test",
			scheduler: schedulers(home, tools)[name],
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := manager.Enable(t.Context()); !errors.Is(err, ErrSchedulerUnavailable) {
			t.Errorf("%s: Enable() = %v, want the scheduler reported unavailable", name, err)
		}
		if manager.Requirement() == "" {
			t.Errorf("%s: no requirement to show", name)
		}
	}
}

// Windows refuses to delete or replace a program that is still exiting. The
// manager waits briefly instead of failing, and gives up after a bound.
func TestBusyFilesAreRetriedBriefly(t *testing.T) {
	newManager := func(t *testing.T) *Manager {
		t.Helper()
		home := t.TempDir()
		manager, err := NewManager(Config{
			Home: home, Executable: syntheticExecutable(t), APIURL: "https://api.example.test",
			scheduler: schedulers(home, &fakeTools{})["task scheduler"],
		})
		if err != nil {
			t.Fatal(err)
		}
		manager.wait = time.Millisecond
		return manager
	}
	busy := func(times int, calls *int, operation func() error) error {
		*calls++
		if *calls <= times {
			return &os.PathError{Op: "remove", Path: "synthetic", Err: os.ErrPermission}
		}
		return operation()
	}

	t.Run("a brief refusal is waited out", func(t *testing.T) {
		manager := newManager(t)
		var renames, removals int
		manager.rename = func(from, to string) error {
			return busy(2, &renames, func() error { return os.Rename(from, to) })
		}
		if err := manager.Enable(t.Context()); err != nil {
			t.Fatalf("Enable() = %v", err)
		}
		binary := manager.scheduler.layout().binary
		manager.remove = func(path string) error {
			if path != binary {
				return os.Remove(path)
			}
			return busy(3, &removals, func() error { return os.Remove(path) })
		}
		if err := manager.Disable(t.Context()); err != nil {
			t.Fatalf("Disable() = %v", err)
		}
		if renames != 3 || removals != 4 {
			t.Fatalf("renames = %d, removals = %d", renames, removals)
		}
		if _, err := os.Lstat(binary); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the executable remains: %v", err)
		}
	})

	t.Run("a lasting refusal is reported", func(t *testing.T) {
		manager := newManager(t)
		if err := manager.Enable(t.Context()); err != nil {
			t.Fatal(err)
		}
		attempts := 0
		manager.remove = func(string) error {
			attempts++
			return &os.PathError{Op: "remove", Path: "synthetic", Err: os.ErrPermission}
		}
		if err := manager.Disable(t.Context()); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("Disable() = %v, want the refusal", err)
		}
		if attempts != busyAttempts {
			t.Fatalf("attempts = %d, want the bound of %d", attempts, busyAttempts)
		}
	})

	t.Run("other failures are not retried", func(t *testing.T) {
		manager := newManager(t)
		if err := manager.Enable(t.Context()); err != nil {
			t.Fatal(err)
		}
		attempts := 0
		manager.remove = func(string) error {
			attempts++
			return errors.New("synthetic: device error")
		}
		if err := manager.Disable(t.Context()); err == nil || attempts != 1 {
			t.Fatalf("Disable() = %v after %d attempts", err, attempts)
		}
	})
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
