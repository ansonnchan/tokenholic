package autosync

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestTaskSchedulerAcceptsTheGeneratedTask registers the generated task with
// the real Task Scheduler, reads it back, and removes it. It changes system
// state, so it runs only where TOKENHOLIC_TEST_TASK_SCHEDULER is set, as CI
// does. It uses its own task name and never touches a real schedule.
func TestTaskSchedulerAcceptsTheGeneratedTask(t *testing.T) {
	if os.Getenv("TOKENHOLIC_TEST_TASK_SCHEDULER") == "" {
		t.Skip("registers a scheduled task; set TOKENHOLIC_TEST_TASK_SCHEDULER=1 to run")
	}
	platform, err := platformScheduler(Config{})
	if err != nil {
		t.Fatal(err)
	}
	scheduler := platform.(taskScheduler)
	scheduler.name = fmt.Sprintf("tokenholic-autosync-test-%d", os.Getpid())
	scheduler.root = t.TempDir()
	// A harmless system program stands in for tokenholic: the task starts it
	// once, it rejects the arguments, and it exits.
	harmless := filepath.Join(os.Getenv("SystemRoot"), "System32", "whoami.exe")
	manager, err := NewManager(Config{
		Home: t.TempDir(), Executable: harmless, APIURL: "https://api.example.test", scheduler: scheduler,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("schtasks", "/Delete", "/TN", scheduler.name, "/F").Run() })

	if err := manager.Enable(t.Context()); err != nil {
		t.Fatalf("Enable() = %v", err)
	}
	status, err := manager.Status(t.Context())
	if err != nil || !status.Installed || !status.Loaded {
		t.Fatalf("Status() = %#v, %v", status, err)
	}
	registered, err := exec.Command("schtasks", "/Query", "/TN", scheduler.name, "/XML").Output()
	if err != nil {
		t.Fatalf("read the registered task back: %v", err)
	}
	paths := scheduler.layout()
	for _, want := range []string{paths.binary, "autosync run", "PT60M", "InteractiveToken"} {
		if !strings.Contains(string(registered), want) {
			t.Errorf("registered task does not contain %q:\n%s", want, registered)
		}
	}
	// Enabling again replaces the task instead of failing on the existing one.
	if err := manager.Enable(t.Context()); err != nil {
		t.Fatalf("second Enable() = %v", err)
	}
	if err := manager.Disable(t.Context()); err != nil {
		t.Fatalf("Disable() = %v", err)
	}
	status, err = manager.Status(t.Context())
	if err != nil || status.Installed || status.Loaded {
		t.Fatalf("Status() after Disable() = %#v, %v", status, err)
	}
	if _, err := os.Stat(filepath.Dir(paths.binary)); !os.IsNotExist(err) {
		t.Fatalf("autosync directory remains: %v", err)
	}
}
