package ccusage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var (
	// ErrUnavailable means there is no ccusage executable to run. A build
	// installed without npm has none.
	ErrUnavailable = errors.New("ccusage is not available")
	// ErrUnpinned means the executable is not the pinned release, or the
	// pinned release has no build for this platform. It is never executed.
	ErrUnpinned = errors.New("ccusage executable is not the pinned release")
	// ErrTimeout means a report did not finish within its time limit.
	ErrTimeout = errors.New("ccusage report timed out")
	// ErrFailed means ccusage could not be started or exited with an error.
	ErrFailed = errors.New("ccusage report failed")
	// ErrMalformed means the output does not satisfy the pinned contract.
	ErrMalformed = errors.New("ccusage output does not match the pinned contract")
)

const (
	// DefaultTimeout bounds one agent's report. The pinned release answers
	// in well under a second for years of records.
	DefaultTimeout = 30 * time.Second
	// maxOutputBytes bounds the report read from ccusage.
	maxOutputBytes = 32 << 20
	// deadProxy is a loopback port nothing listens on. --offline already
	// forbids fetching; routing every proxy variable here makes a fetch that
	// slipped through fail on this machine instead of leaving it.
	deadProxy = "http://127.0.0.1:9"
)

// passedVariables are the only variables a report inherits. The home
// variables let ccusage find agent records; the rest are locations the
// agents themselves document. Nothing else reaches it: no credential, no
// proxy, and none of ccusage's own settings.
var passedVariables = []string{
	"HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH", "SystemRoot",
	"XDG_DATA_HOME", "COPILOT_HOME", "COPILOT_OTEL_FILE_EXPORTER_PATH", "GROK_HOME",
}

// Runner executes the pinned ccusage release for one agent at a time.
//
// It runs the executable directly with an argument array, so no shell and no
// Node process is involved, and only after the file's digest matches the
// pin. Each run gets a time limit, an allowlisted environment, and a private
// working directory holding an empty ccusage configuration, so a user's own
// ccusage settings cannot narrow or regroup what is reported.
type Runner struct {
	// Path is the ccusage executable. Empty means none is installed.
	Path string
	// Timeout bounds one report. Zero selects DefaultTimeout.
	Timeout time.Duration
	// Getenv reads the caller's environment. Nil means every variable is unset.
	Getenv func(string) string

	// The fields below are replaced in tests: the platform and digest the
	// executable is held to, and the function that starts it.
	goos, goarch string
	digest       string
	execute      func(ctx context.Context, command Command) ([]byte, error)
}

// Command is one fully resolved execution: what a report runs and nothing
// more. It exists so tests can assert the exact invocation.
type Command struct {
	Path        string
	Arguments   []string
	Environment []string
	Directory   string
}

// Verify reports whether the executable exists and is the pinned release. It
// reads the file and executes nothing.
func (r Runner) Verify() error {
	if strings.TrimSpace(r.Path) == "" {
		return ErrUnavailable
	}
	goos, goarch := r.platform()
	want, found := PinnedDigest(goos, goarch)
	if r.digest != "" {
		want, found = r.digest, true
	}
	if !found {
		return fmt.Errorf("%w: no pinned build for %s/%s", ErrUnpinned, goos, goarch)
	}
	file, err := os.Open(r.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrUnavailable
		}
		return fmt.Errorf("%w: the executable cannot be read", ErrUnpinned)
	}
	defer file.Close()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: the executable is not a regular file", ErrUnpinned)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("%w: the executable cannot be read", ErrUnpinned)
	}
	if hex.EncodeToString(hash.Sum(nil)) != want {
		return ErrUnpinned
	}
	return nil
}

// Daily runs the pinned daily report for one agent and returns it validated.
// Every failure is one of the package's sentinel errors, and none of them
// carries ccusage's own output, which can name local paths.
func (r Runner) Daily(ctx context.Context, agent Agent) (Report, error) {
	if !knownAgent(agent) {
		return Report{}, fmt.Errorf("%w: %q is not an agent in the pinned contract", ErrFailed, agent)
	}
	if err := r.Verify(); err != nil {
		return Report{}, err
	}
	directory, err := privateDirectory()
	if err != nil {
		return Report{}, fmt.Errorf("%w: no private working directory", ErrFailed)
	}
	defer os.RemoveAll(directory)

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	execute := r.execute
	if execute == nil {
		execute = executeCommand
	}
	output, err := execute(runCtx, Command{
		Path: r.Path, Arguments: Arguments(agent), Environment: r.environment(), Directory: directory,
	})
	if err != nil {
		if ctx.Err() != nil {
			return Report{}, ctx.Err()
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return Report{}, ErrTimeout
		}
		if errors.Is(err, ErrMalformed) {
			return Report{}, ErrMalformed
		}
		return Report{}, ErrFailed
	}
	report, err := ParseDailyReport(output)
	if err != nil {
		// The parse error describes the report's shape, never its content.
		return Report{}, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	return report, nil
}

func (r Runner) platform() (string, string) {
	if r.goos != "" || r.goarch != "" {
		return r.goos, r.goarch
	}
	return runtime.GOOS, runtime.GOARCH
}

func (r Runner) environment() []string {
	environment := make([]string, 0, len(passedVariables)+8)
	if r.Getenv != nil {
		for _, name := range passedVariables {
			if value := r.Getenv(name); value != "" {
				environment = append(environment, name+"="+value)
			}
		}
	}
	return append(environment,
		"CCUSAGE_OFFLINE=1", "LOG_LEVEL=0", "NO_COLOR=1",
		"HTTP_PROXY="+deadProxy, "HTTPS_PROXY="+deadProxy, "ALL_PROXY="+deadProxy,
		"http_proxy="+deadProxy, "https_proxy="+deadProxy, "all_proxy="+deadProxy,
	)
}

func knownAgent(agent Agent) bool {
	for _, known := range Agents() {
		if agent == known {
			return true
		}
	}
	return false
}

// privateDirectory makes the working directory of one run. ccusage reads its
// configuration from the working directory before anywhere else and stops at
// the first file it finds, so an empty one here shields the run from a
// configuration in the user's home that filters dates or changes the
// timezone.
func privateDirectory() (string, error) {
	directory, err := os.MkdirTemp("", "tokenholic-ccusage-")
	if err != nil {
		return "", err
	}
	settings := filepath.Join(directory, ".ccusage")
	if err := os.Mkdir(settings, 0o700); err == nil {
		err = os.WriteFile(filepath.Join(settings, "ccusage.json"), []byte("{}\n"), 0o600)
	}
	if err != nil {
		_ = os.RemoveAll(directory)
		return "", err
	}
	return directory, nil
}

// executeCommand starts the executable directly. Standard error is discarded
// unread: it can name local paths and nothing in it is needed.
func executeCommand(ctx context.Context, command Command) ([]byte, error) {
	process := exec.CommandContext(ctx, command.Path, command.Arguments...)
	process.Env = command.Environment
	process.Dir = command.Directory
	process.Stdin = nil
	process.Stderr = io.Discard
	// A child that outlives the deadline must not keep the pipe open.
	process.WaitDelay = time.Second
	var output limitedBuffer
	process.Stdout = &output
	if err := process.Run(); err != nil {
		return nil, err
	}
	if output.exceeded {
		return nil, ErrMalformed
	}
	return output.buffer.Bytes(), nil
}

// limitedBuffer keeps at most maxOutputBytes and remembers going over. It
// never reports a short write, so an oversized report ends as malformed
// output rather than a broken pipe.
type limitedBuffer struct {
	buffer   bytes.Buffer
	exceeded bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if room := maxOutputBytes - b.buffer.Len(); len(data) > room {
		b.exceeded = true
		b.buffer.Write(data[:room])
		return len(data), nil
	}
	return b.buffer.Write(data)
}
