package autosync

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ansonnchan/tokenholic/internal/privatefs"
	usageSync "github.com/ansonnchan/tokenholic/internal/sync"
)

const (
	applicationDirectory = "tokenholic"
	interval             = time.Hour
	// busyAttempts and busyWait bound how long a file that is still in use
	// is waited for: about three seconds.
	busyAttempts = 30
	busyWait     = 100 * time.Millisecond
)

// Config supplies the local values written into the schedule.
type Config struct {
	Home       string
	Executable string
	// APIURL is the address the job will use, checked before anything is
	// installed.
	APIURL string
	// PinAPIURL writes APIURL into the schedule. Set it only when the user
	// overrode the address: without it the job uses the address built into
	// the executable, so an upgrade that moves the hosted API is followed.
	PinAPIURL bool
	CodexHome string
	// CCUsagePath is where the pinned ccusage executable was installed, as
	// the npm launcher reported it to the command enabling autosync. A
	// scheduled run does not start through the launcher, so the schedule
	// carries the location. Empty leaves the scheduled run without ccusage.
	CCUsagePath string
	// Getenv reads the environment of the command enabling autosync, for the
	// platform directories a scheduler honors. It defaults to os.Getenv.
	Getenv func(string) string
	// scheduler replaces the operating system's scheduler in tests.
	scheduler scheduler
}

// Status reports whether the persistent configuration exists and whether
// the scheduler currently has it loaded.
type Status struct {
	Installed bool
	Loaded    bool
}

// Manager installs and removes one per-user schedule through the operating
// system's own scheduler.
type Manager struct {
	executable, apiURL, codexHome string
	ccusagePath                   string
	pinAPIURL                     bool
	scheduler                     scheduler
	// remove, rename, and wait are the file operations a busy executable can
	// refuse; tests replace them.
	remove func(string) error
	rename func(string, string) error
	wait   time.Duration
}

// CCUsagePathVariable names the variable that tells tokenholic where the
// pinned ccusage executable is. The npm launcher sets it; a schedule repeats
// it for runs the launcher does not start.
const CCUsagePathVariable = "TOKENHOLIC_CCUSAGE_PATH"

// NewManager validates paths and binds the platform scheduler.
func NewManager(config Config) (*Manager, error) {
	if !filepath.IsAbs(config.Home) || !filepath.IsAbs(config.Executable) {
		return nil, errors.New("autosync home and executable must be absolute")
	}
	if config.CodexHome != "" && !filepath.IsAbs(config.CodexHome) {
		return nil, errors.New("autosync Codex home must be absolute")
	}
	if config.CCUsagePath != "" && !filepath.IsAbs(config.CCUsagePath) {
		return nil, errors.New("autosync ccusage location must be absolute")
	}
	if config.Getenv == nil {
		config.Getenv = os.Getenv
	}
	scheduler := config.scheduler
	if scheduler == nil {
		var err error
		if scheduler, err = platformScheduler(config); err != nil {
			return nil, fmt.Errorf("prepare the system scheduler: %w", err)
		}
	}
	return &Manager{
		executable: config.Executable, apiURL: strings.TrimSpace(config.APIURL),
		pinAPIURL: config.PinAPIURL, codexHome: config.CodexHome, ccusagePath: config.CCUsagePath, scheduler: scheduler,
		remove: os.Remove, rename: os.Rename, wait: busyWait,
	}, nil
}

// Requirement names what the operating system must provide for autosync, in
// words that are safe to show.
func (m *Manager) Requirement() string { return m.scheduler.requirement() }

// Enable installs a stable executable copy and loads an hourly schedule.
// Re-enabling atomically refreshes every file from the current release.
func (m *Manager) Enable(ctx context.Context) error {
	if m.apiURL == "" {
		return errors.New("autosync requires an API origin")
	}
	if _, err := usageSync.NewClient(m.apiURL, nil); err != nil {
		return fmt.Errorf("validate autosync API origin: %w", err)
	}
	parsedAPI, err := url.Parse(m.apiURL)
	if err != nil || parsedAPI.User != nil || parsedAPI.RawQuery != "" || parsedAPI.Fragment != "" {
		return errors.New("autosync API origin must not contain credentials, a query, or a fragment")
	}
	// Inspecting first also proves the scheduler is reachable before any file
	// is written.
	status, err := m.Status(ctx)
	if err != nil {
		return err
	}
	paths := m.scheduler.layout()
	definitions, err := m.scheduler.definitions(m.job(paths.installation))
	if err != nil {
		return fmt.Errorf("describe autosync schedule: %w", err)
	}
	if status.Loaded {
		if err := m.scheduler.unload(ctx); err != nil {
			return fmt.Errorf("unload existing autosync schedule: %w", err)
		}
	}
	for _, directory := range []string{filepath.Dir(paths.binary), paths.logs} {
		if err := privatefs.MkdirAll(directory); err != nil {
			return fmt.Errorf("prepare autosync directory: %w", err)
		}
	}
	// The scheduler would create the logs itself, readable by anyone the
	// directory lets in; creating them first keeps them owner-only.
	for _, log := range []string{paths.stdout, paths.stderr} {
		if err := privatefs.Touch(log); err != nil {
			return fmt.Errorf("prepare autosync log: %w", err)
		}
	}
	if err := m.copyExecutable(ctx, paths.binary); err != nil {
		return err
	}
	for _, definition := range definitions {
		if err := os.MkdirAll(filepath.Dir(definition.path), 0o755); err != nil {
			return fmt.Errorf("create scheduler directory: %w", err)
		}
		if err := writeAtomic(definition.path, definition.contents); err != nil {
			return fmt.Errorf("write autosync schedule: %w", err)
		}
	}
	if err := m.scheduler.load(ctx); err != nil {
		return fmt.Errorf("load autosync schedule: %w", err)
	}
	return nil
}

// Disable unloads the schedule and removes only autosync-owned files. It
// never deletes the local usage database or OS-vault credential. Files are
// removed even when the scheduler is unreachable, since nothing can then be
// running them.
func (m *Manager) Disable(ctx context.Context) error {
	loaded, err := m.scheduler.loaded(ctx)
	reachable := !errors.Is(err, ErrSchedulerUnavailable)
	if err != nil && reachable {
		return fmt.Errorf("inspect autosync schedule: %w", err)
	}
	if loaded {
		if err := m.scheduler.unload(ctx); err != nil {
			return fmt.Errorf("unload autosync schedule: %w", err)
		}
	}
	paths := m.scheduler.layout()
	for _, definition := range paths.definitions {
		if err := m.removeOwned(ctx, definition); err != nil {
			return err
		}
	}
	for _, installed := range append([]installation{paths.installation}, paths.legacy...) {
		for _, path := range []string{installed.binary, installed.stdout, installed.stderr} {
			if err := m.removeOwned(ctx, path); err != nil {
				return err
			}
		}
		// Directories go only when autosync left them empty; the application
		// directory may also hold the usage database.
		binaries := filepath.Dir(installed.binary)
		for _, directory := range []string{binaries, filepath.Dir(binaries), installed.logs} {
			_ = os.Remove(directory)
		}
	}
	if reachable {
		if err := m.scheduler.forget(ctx); err != nil {
			return fmt.Errorf("refresh the scheduler after removal: %w", err)
		}
	}
	return nil
}

// Status inspects persistent configuration and the scheduler's current view.
func (m *Manager) Status(ctx context.Context) (Status, error) {
	paths := m.scheduler.layout()
	definitionsReady := len(paths.definitions) > 0
	for _, definition := range paths.definitions {
		ready, err := regularFile(definition, false)
		if err != nil {
			return Status{}, fmt.Errorf("inspect autosync schedule file: %w", err)
		}
		definitionsReady = definitionsReady && ready
	}
	binaryReady := false
	for _, installed := range append([]installation{paths.installation}, paths.legacy...) {
		ready, err := regularFile(installed.binary, true)
		if err != nil {
			return Status{}, fmt.Errorf("inspect stable autosync executable: %w", err)
		}
		binaryReady = binaryReady || ready
	}
	status := Status{Installed: definitionsReady && binaryReady}
	loaded, err := m.scheduler.loaded(ctx)
	if err != nil {
		return status, fmt.Errorf("inspect loaded autosync schedule: %w", err)
	}
	status.Loaded = loaded
	return status, nil
}

// job describes the scheduled command. The environment is a fixed, ordered
// allowlist: no credential and nothing else from the caller's environment.
func (m *Manager) job(installed installation) job {
	var environment []variable
	if m.pinAPIURL {
		environment = append(environment, variable{"TOKENHOLIC_API_URL", m.apiURL})
	}
	if m.codexHome != "" {
		environment = append(environment, variable{"CODEX_HOME", m.codexHome})
	}
	if m.ccusagePath != "" {
		// A location, not a permission: the scheduled run still refuses any
		// file whose digest is not the pinned one, and honors the saved
		// choice to keep ccusage off.
		environment = append(environment, variable{CCUsagePathVariable, m.ccusagePath})
	}
	environment = append(environment, variable{"NO_COLOR", "1"})
	return job{installation: installed, environment: environment}
}

// whileBusy runs a file operation, retrying for a short time while it is
// refused for lack of permission. Windows refuses to delete or replace a
// program that is running, and a job that was just told to stop takes a
// moment to go; the same goes for the log files it holds open. Elsewhere a
// running program can be removed, so the wait is only ever paid for a real
// permission problem.
func (m *Manager) whileBusy(ctx context.Context, operation func() error) error {
	var err error
	for attempt := 0; attempt < busyAttempts; attempt++ {
		if err = operation(); err == nil || !errors.Is(err, os.ErrPermission) {
			return err
		}
		timer := time.NewTimer(m.wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return err
		}
	}
	return err
}

// removeOwned deletes one autosync-owned file; a file already gone is fine.
func (m *Manager) removeOwned(ctx context.Context, path string) error {
	err := m.whileBusy(ctx, func() error { return m.remove(path) })
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove autosync-owned file: %w", err)
	}
	return nil
}

func regularFile(path string, executable bool) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}
	return !executable || runnable(info), nil
}

func (m *Manager) copyExecutable(ctx context.Context, destination string) error {
	resolved, err := filepath.EvalSymlinks(m.executable)
	if err != nil {
		return fmt.Errorf("resolve current executable: %w", err)
	}
	input, err := os.Open(resolved)
	if err != nil {
		return fmt.Errorf("open current executable: %w", err)
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() || !runnable(info) {
		return errors.New("current executable is not a regular executable file")
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".tokenholic-*")
	if err != nil {
		return fmt.Errorf("create stable executable: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o700); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure stable executable: %w", err)
	}
	if _, err := io.Copy(temporary, input); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("copy stable executable: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("flush stable executable: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close stable executable: %w", err)
	}
	// The copy being replaced may still be finishing a run.
	if err := m.whileBusy(ctx, func() error { return m.rename(temporaryPath, destination) }); err != nil {
		return fmt.Errorf("install stable executable: %w", err)
	}
	return nil
}

// writeAtomic replaces destination with a private file holding contents.
func writeAtomic(destination string, contents []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".autosync-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := privatefs.RestrictFile(temporaryPath); err != nil {
		return err
	}
	return os.Rename(temporaryPath, destination)
}
