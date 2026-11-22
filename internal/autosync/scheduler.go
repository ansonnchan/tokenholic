package autosync

import (
	"context"
	"errors"
	"os/exec"
)

// ErrSchedulerUnavailable means the operating system's per-user scheduler
// cannot be reached, so a schedule can be neither installed nor inspected.
var ErrSchedulerUnavailable = errors.New("the user scheduler is not available")

// ErrUnsupported means tokenholic has no scheduler for this operating system.
var ErrUnsupported = errors.New("autosync is not supported on this operating system")

// scheduler is one operating system's way to run the sync job at login and
// hourly. Implementations only describe and drive the schedule; the Manager
// owns the files.
type scheduler interface {
	// requirement names what the system must provide, for a message to the user.
	requirement() string
	layout() layout
	// definitions renders the scheduler's own files for one job.
	definitions(job) ([]definition, error)
	loaded(context.Context) (bool, error)
	// load registers and starts the schedule from the written definitions.
	load(context.Context) error
	unload(context.Context) error
	// forget runs after the definitions are removed.
	forget(context.Context) error
}

// installation is one place the stable executable and its logs live.
type installation struct {
	binary, logs, stdout, stderr string
}

// layout lists every file a schedule owns. Nothing outside it is ever removed.
type layout struct {
	installation
	definitions []string
	// legacy holds locations earlier releases used; they are recognized and
	// removed but never written.
	legacy []installation
}

// job is what a schedule runs: the stable executable with `sync`, a small
// fixed environment, and two private log files. It carries no credential.
type job struct {
	installation
	environment []variable
}

type variable struct{ name, value string }

type definition struct {
	path     string
	contents []byte
}

// runner executes one scheduler command without a shell. err is set only
// when the command could not run at all; a command that ran and failed
// reports its exit code. Output is inspected, never shown: it can name
// local paths.
type runner func(ctx context.Context, name string, arguments ...string) (output []byte, exitCode int, err error)

func runCommand(ctx context.Context, name string, arguments ...string) ([]byte, int, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	// Scheduler tools are matched on their English messages.
	command.Env = append(command.Environ(), "LC_ALL=C")
	output, err := command.CombinedOutput()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return output, exitErr.ExitCode(), nil
	}
	if err != nil {
		return output, -1, err
	}
	return output, 0, nil
}

// unsupported is the scheduler of an operating system tokenholic cannot
// schedule on. Every operation reports that plainly.
type unsupported struct{}

func (unsupported) requirement() string                   { return "a supported operating system" }
func (unsupported) layout() layout                        { return layout{} }
func (unsupported) definitions(job) ([]definition, error) { return nil, ErrUnsupported }
func (unsupported) loaded(context.Context) (bool, error)  { return false, ErrUnsupported }
func (unsupported) load(context.Context) error            { return ErrUnsupported }
func (unsupported) unload(context.Context) error          { return ErrUnsupported }
func (unsupported) forget(context.Context) error          { return ErrUnsupported }
