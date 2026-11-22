package autosync

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ScheduledJob is what a scheduled run needs when its scheduler cannot
// provide it: the job's environment and where its output goes. Windows Task
// Scheduler starts a bare program, so the task passes this file to
// `tokenholic autosync run` instead. It never holds a credential.
type ScheduledJob struct {
	Environment map[string]string `json:"environment"`
	Stdout      string            `json:"stdout"`
	Stderr      string            `json:"stderr"`
}

// scheduledVariables is every variable a job file may set. Anything else in
// the file is refused, so the file cannot be used to steer the process.
var scheduledVariables = map[string]bool{"TOKENHOLIC_API_URL": true, "CODEX_HOME": true, "NO_COLOR": true, CCUsagePathVariable: true}

// LoadScheduledJob reads and validates a job file written by Enable.
func LoadScheduledJob(path string) (ScheduledJob, error) {
	if !filepath.IsAbs(path) {
		return ScheduledJob{}, errors.New("scheduled job path must be absolute")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return ScheduledJob{}, fmt.Errorf("read scheduled job: %w", err)
	}
	var settings ScheduledJob
	if err := json.Unmarshal(contents, &settings); err != nil {
		return ScheduledJob{}, fmt.Errorf("decode scheduled job: %w", err)
	}
	if err := settings.validate(); err != nil {
		return ScheduledJob{}, err
	}
	return settings, nil
}

func (j ScheduledJob) validate() error {
	for name := range j.Environment {
		if !scheduledVariables[name] {
			return fmt.Errorf("scheduled job sets an unexpected variable %q", name)
		}
	}
	if !filepath.IsAbs(j.Stdout) || !filepath.IsAbs(j.Stderr) {
		return errors.New("scheduled job log paths must be absolute")
	}
	return nil
}
