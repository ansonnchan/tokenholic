package autosync

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
)

const taskName = "tokenholic-autosync"

// taskScheduler schedules the job as a Windows Task Scheduler task that runs
// as the signed-in user, only while they are signed in. No password is
// stored with the task.
//
// Task Scheduler can neither set an environment for a program nor redirect
// its output, so the task runs `tokenholic autosync run <job file>` and the
// job file carries both. See ScheduledJob.
type taskScheduler struct {
	// name is the task's name in the scheduler's root folder; a root-level
	// task leaves no folder behind when it is deleted.
	name string
	// root is the local application data folder.
	root string
	// userID is the current user's security identifier in string form.
	userID string
	now    func() time.Time
	run    runner
}

func (taskScheduler) requirement() string { return "Windows Task Scheduler (`schtasks`)" }

// layout keeps everything autosync owns in one private directory, so
// disabling can remove the directory itself.
func (t taskScheduler) layout() layout {
	directory := filepath.Join(t.root, applicationDirectory, "autosync")
	return layout{
		installation: installation{
			binary: filepath.Join(directory, "tokenholic.exe"),
			logs:   directory, stdout: filepath.Join(directory, "autosync.log"), stderr: filepath.Join(directory, "autosync-error.log"),
		},
		definitions: []string{filepath.Join(directory, "job.json"), filepath.Join(directory, "task.xml")},
	}
}

func (t taskScheduler) definitions(work job) ([]definition, error) {
	paths := t.layout().definitions
	settings := ScheduledJob{Environment: make(map[string]string, len(work.environment)), Stdout: work.stdout, Stderr: work.stderr}
	for _, entry := range work.environment {
		settings.Environment[entry.name] = entry.value
	}
	if err := settings.validate(); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode scheduled job: %w", err)
	}
	// A Windows path cannot contain a double quote, so quoting the job file is
	// all the argument needs.
	if strings.ContainsAny(work.binary+paths[0], "\"\r\n") {
		return nil, errors.New("a scheduled task cannot hold a path with a quote or a line break")
	}
	task := `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Hourly tokenholic usage sync</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <UserId>` + escapeXML(t.userID) + `</UserId>
      <Delay>PT1M</Delay>
    </LogonTrigger>
    <TimeTrigger>
      <Repetition>
        <Interval>PT` + fmt.Sprint(int(interval.Minutes())) + `M</Interval>
        <StopAtDurationEnd>false</StopAtDurationEnd>
      </Repetition>
      <StartBoundary>` + t.now().Format("2006-01-02T15:04:05") + `</StartBoundary>
      <Enabled>true</Enabled>
    </TimeTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <UserId>` + escapeXML(t.userID) + `</UserId>
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT10M</ExecutionTimeLimit>
    <Priority>7</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + escapeXML(work.binary) + `</Command>
      <Arguments>autosync run ` + escapeXML(`"`+paths[0]+`"`) + `</Arguments>
    </Exec>
  </Actions>
</Task>
`
	return []definition{
		{path: paths[0], contents: append(encoded, '\n')},
		{path: paths[1], contents: utf16WithByteOrderMark(task)},
	}, nil
}

// utf16WithByteOrderMark encodes text the way Task Scheduler writes and
// reliably reads its own task files: little-endian UTF-16 with a mark.
func utf16WithByteOrderMark(text string) []byte {
	units := utf16.Encode([]rune(strings.ReplaceAll(text, "\n", "\r\n")))
	encoded := make([]byte, 2, 2+2*len(units))
	encoded[0], encoded[1] = 0xFF, 0xFE
	for _, unit := range units {
		encoded = binary.LittleEndian.AppendUint16(encoded, unit)
	}
	return encoded
}

// loaded asks whether the task is registered. schtasks prints messages in
// the system's language, so only its exit code is read.
func (t taskScheduler) loaded(ctx context.Context) (bool, error) {
	_, code, err := t.run(ctx, "schtasks", "/Query", "/TN", t.name)
	if errors.Is(err, exec.ErrNotFound) {
		return false, ErrSchedulerUnavailable
	}
	if err != nil {
		return false, errors.New("schtasks could not run")
	}
	return code == 0, nil
}

func (t taskScheduler) load(ctx context.Context) error {
	if err := t.command(ctx, "/Create", "/TN", t.name, "/XML", t.layout().definitions[1], "/F"); err != nil {
		return err
	}
	// The time trigger first fires an hour from now. Starting the task once
	// makes enabling sync immediately, as the other schedulers do; a refusal
	// here leaves a working schedule and is not a failure.
	_, _, _ = t.run(ctx, "schtasks", "/Run", "/TN", t.name)
	return nil
}

func (t taskScheduler) unload(ctx context.Context) error {
	// Stop a run in progress so its executable can be replaced or removed.
	_, _, _ = t.run(ctx, "schtasks", "/End", "/TN", t.name)
	return t.command(ctx, "/Delete", "/TN", t.name, "/F")
}

func (taskScheduler) forget(context.Context) error { return nil }

func (t taskScheduler) command(ctx context.Context, arguments ...string) error {
	_, code, err := t.run(ctx, "schtasks", arguments...)
	if errors.Is(err, exec.ErrNotFound) {
		return ErrSchedulerUnavailable
	}
	if err != nil || code != 0 {
		return fmt.Errorf("schtasks %s failed", arguments[0])
	}
	return nil
}
