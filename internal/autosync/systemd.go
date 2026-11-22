package autosync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	systemdService = "tokenholic-autosync.service"
	systemdTimer   = "tokenholic-autosync.timer"
)

// systemd schedules the job as a user-level systemd timer. It needs a running
// user service manager and never touches system units or lingering, so the
// job runs only while the user has a session.
type systemd struct {
	configHome, dataHome, stateHome string
	// preserved carries the XDG settings the job needs to find the same
	// database and lock as the shell that enabled it.
	preserved []variable
	run       runner
}

// newSystemd resolves the XDG base directories as the specification defines
// them: a variable that is unset, empty, or relative falls back to its
// default under the home directory.
func newSystemd(home string, getenv func(string) string, run runner) systemd {
	base := func(name string, fallback ...string) (string, bool) {
		if value := getenv(name); filepath.IsAbs(value) {
			return filepath.Clean(value), true
		}
		return filepath.Join(append([]string{home}, fallback...)...), false
	}
	scheduler := systemd{run: run}
	var configured bool
	scheduler.configHome, configured = base("XDG_CONFIG_HOME", ".config")
	if configured {
		// A user service manager does not inherit a shell's exports.
		scheduler.preserved = append(scheduler.preserved, variable{"XDG_CONFIG_HOME", scheduler.configHome})
	}
	scheduler.dataHome, _ = base("XDG_DATA_HOME", ".local", "share")
	scheduler.stateHome, _ = base("XDG_STATE_HOME", ".local", "state")
	return scheduler
}

func (systemd) requirement() string { return "a systemd user session (`systemctl --user`)" }

func (s systemd) layout() layout {
	logs := filepath.Join(s.stateHome, applicationDirectory)
	units := filepath.Join(s.configHome, "systemd", "user")
	return layout{
		installation: installation{
			binary: filepath.Join(s.dataHome, applicationDirectory, "bin", "tokenholic"),
			logs:   logs, stdout: filepath.Join(logs, "autosync.log"), stderr: filepath.Join(logs, "autosync-error.log"),
		},
		definitions: []string{filepath.Join(units, systemdService), filepath.Join(units, systemdTimer)},
	}
}

func (s systemd) definitions(work job) ([]definition, error) {
	environment := append(append([]variable(nil), work.environment...), s.preserved...)
	values := []string{work.binary, work.stdout, work.stderr}
	for _, entry := range environment {
		values = append(values, entry.name, entry.value)
	}
	for _, value := range values {
		if strings.ContainsFunc(value, unicode.IsControl) {
			return nil, errors.New("a systemd unit cannot hold a value with control characters")
		}
	}
	// systemd expands `$` in a command line and offers no escape for it in the
	// program path, so such a path cannot be written faithfully.
	if strings.Contains(work.binary, "$") {
		return nil, errors.New("a systemd unit cannot run an executable whose path contains `$`")
	}

	var service strings.Builder
	service.WriteString("[Unit]\nDescription=tokenholic usage sync\n\n[Service]\nType=oneshot\n")
	fmt.Fprintf(&service, "ExecStart=%s sync\n", systemdQuote(work.binary))
	for _, entry := range environment {
		fmt.Fprintf(&service, "Environment=%s\n", systemdQuote(entry.name+"="+entry.value))
	}
	// append: needs systemd 240 or newer. An older manager ignores the line
	// and keeps the output in the user journal instead.
	fmt.Fprintf(&service, "StandardOutput=append:%s\n", systemdSpecifiers(work.stdout))
	fmt.Fprintf(&service, "StandardError=append:%s\n", systemdSpecifiers(work.stderr))
	service.WriteString("Nice=10\n")

	// OnStartupSec counts from the start of the user's service manager, which
	// is login. A timer started after that moment runs the job at once, so
	// enabling syncs immediately, as the launchd agent does.
	timer := "[Unit]\nDescription=Hourly tokenholic usage sync\n\n" +
		"[Timer]\nOnStartupSec=1min\n" +
		fmt.Sprintf("OnUnitActiveSec=%ds\n", int(interval.Seconds())) +
		"Unit=" + systemdService + "\n\n" +
		"[Install]\nWantedBy=timers.target\n"

	paths := s.layout().definitions
	return []definition{
		{path: paths[0], contents: []byte(service.String())},
		{path: paths[1], contents: []byte(timer)},
	}, nil
}

func (s systemd) loaded(ctx context.Context) (bool, error) {
	output, code, err := s.run(ctx, "systemctl", "--user", "is-active", systemdTimer)
	if systemdUnavailable(output, err) {
		return false, ErrSchedulerUnavailable
	}
	if err != nil {
		return false, errors.New("systemctl could not run")
	}
	// Every other exit code means inactive, failed, or unknown to the manager.
	return code == 0, nil
}

func (s systemd) load(ctx context.Context) error {
	if err := s.command(ctx, "daemon-reload"); err != nil {
		return err
	}
	return s.command(ctx, "enable", "--now", systemdTimer)
}

func (s systemd) unload(ctx context.Context) error {
	if err := s.command(ctx, "disable", "--now", systemdTimer); err != nil {
		// A timer whose unit file is already gone cannot be disabled, but it
		// can still be stopped.
		return s.command(ctx, "stop", systemdTimer)
	}
	return nil
}

func (s systemd) forget(ctx context.Context) error {
	if err := s.command(ctx, "daemon-reload"); err != nil {
		return err
	}
	// A last run that failed would otherwise stay listed after its unit is
	// gone. There is nothing to reset when it succeeded, which is not an error.
	_, _, _ = s.run(ctx, "systemctl", "--user", "reset-failed", systemdService, systemdTimer)
	return nil
}

func (s systemd) command(ctx context.Context, arguments ...string) error {
	output, code, err := s.run(ctx, "systemctl", append([]string{"--user"}, arguments...)...)
	if systemdUnavailable(output, err) {
		return ErrSchedulerUnavailable
	}
	if err != nil || code != 0 {
		return fmt.Errorf("systemctl %s failed", arguments[0])
	}
	return nil
}

// systemdUnavailable recognizes a machine without a reachable user service
// manager: no systemctl at all, systemd not running as init (a container, or
// WSL without it), or no session bus for this user.
func systemdUnavailable(output []byte, err error) bool {
	if errors.Is(err, exec.ErrNotFound) {
		return true
	}
	lower := bytes.ToLower(output)
	return bytes.Contains(lower, []byte("failed to connect to bus")) ||
		bytes.Contains(lower, []byte("not been booted with systemd"))
}

// systemdSpecifiers escapes the one character systemd expands in every unit
// value.
func systemdSpecifiers(value string) string {
	return strings.ReplaceAll(value, "%", "%%")
}

// systemdQuote writes one word of a unit line so spaces, quotes, and
// backslashes survive systemd's parsing.
func systemdQuote(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(systemdSpecifiers(value)) + `"`
}
