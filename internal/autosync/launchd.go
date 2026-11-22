package autosync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const launchLabel = "io.tokenholic.autosync"

// launchd schedules the job as a per-user macOS launch agent.
type launchd struct {
	home string
	uid  int
	run  runner
}

func (launchd) requirement() string { return "launchd" }

func (l launchd) layout() layout {
	return layout{
		installation: l.installation(applicationDirectory),
		definitions:  []string{filepath.Join(l.home, "Library", "LaunchAgents", launchLabel+".plist")},
		// Releases before the lowercase rename installed under a capitalized
		// directory; on a case-sensitive volume those files are separate.
		legacy: []installation{l.installation(legacyApplicationDirectory())},
	}
}

func (l launchd) installation(directory string) installation {
	logs := filepath.Join(l.home, "Library", "Logs", directory)
	return installation{
		binary: filepath.Join(l.home, "Library", "Application Support", directory, "bin", "tokenholic"),
		logs:   logs, stdout: filepath.Join(logs, "autosync.log"), stderr: filepath.Join(logs, "autosync-error.log"),
	}
}

func (l launchd) domain() string { return "gui/" + strconv.Itoa(l.uid) }

func (l launchd) definitions(work job) ([]definition, error) {
	var environment strings.Builder
	for _, entry := range work.environment {
		environment.WriteString("\t\t<key>" + escapeXML(entry.name) + "</key>\n\t\t<string>" + escapeXML(entry.value) + "</string>\n")
	}
	contents := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + launchLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + escapeXML(work.binary) + `</string>
		<string>sync</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
` + environment.String() + `	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>StartInterval</key>
	<integer>` + strconv.Itoa(int(interval.Seconds())) + `</integer>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>` + escapeXML(work.stdout) + `</string>
	<key>StandardErrorPath</key>
	<string>` + escapeXML(work.stderr) + `</string>
</dict>
</plist>
`
	return []definition{{path: l.layout().definitions[0], contents: []byte(contents)}}, nil
}

func (l launchd) loaded(ctx context.Context) (bool, error) {
	output, code, err := l.run(ctx, "launchctl", "print", l.domain()+"/"+launchLabel)
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return false, ErrSchedulerUnavailable
	case err != nil:
		return false, errors.New("launchctl could not run")
	case code == 0:
		return true, nil
	case code == 113 || bytes.Contains(bytes.ToLower(output), []byte("could not find service")):
		return false, nil
	}
	return false, errors.New("launchctl command failed")
}

func (l launchd) load(ctx context.Context) error {
	return l.command(ctx, "bootstrap", l.domain(), l.layout().definitions[0])
}

func (l launchd) unload(ctx context.Context) error {
	return l.command(ctx, "bootout", l.domain()+"/"+launchLabel)
}

func (launchd) forget(context.Context) error { return nil }

func (l launchd) command(ctx context.Context, arguments ...string) error {
	if _, code, err := l.run(ctx, "launchctl", arguments...); err != nil || code != 0 {
		return fmt.Errorf("launchctl %s failed", arguments[0])
	}
	return nil
}

func escapeXML(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(value)
}
