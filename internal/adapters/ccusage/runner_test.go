package ccusage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestMain lets the test binary stand in for ccusage. Started the way the
// runner starts a report, it behaves as the agent name says and exits; no
// real ccusage runs in tests and no home directory is read.
func TestMain(m *testing.M) {
	if len(os.Args) == 8 && slices.Equal(os.Args[2:], reportArguments()) {
		os.Exit(standInReport(os.Args[1]))
	}
	os.Exit(m.Run())
}

const standInTotals = `"totals":{"inputTokens":1,"outputTokens":0,"cacheCreationTokens":0,"cacheReadTokens":0,"totalTokens":1}`

func standInReport(agent string) int {
	switch agent {
	case "qwen":
		time.Sleep(time.Minute)
	case "grok":
		fmt.Print("No usage data found.\n")
	case "copilot":
		fmt.Fprintln(os.Stderr, "error reading /synthetic/private/location")
		return 3
	case "gemini":
		fmt.Print(strings.Repeat(" ", maxOutputBytes+1))
	case "opencode":
		// Report what the process was given through the one free-text field.
		names := make([]string, 0, len(os.Environ()))
		for _, entry := range os.Environ() {
			name, _, _ := strings.Cut(entry, "=")
			names = append(names, name)
		}
		sort.Strings(names)
		_, configErr := os.Stat(filepath.Join(".ccusage", "ccusage.json"))
		observed := fmt.Sprintf("config=%t env=%s", configErr == nil, strings.Join(names, ","))
		fmt.Printf(`{"daily":[{"date":"2026-01-02","inputTokens":1,"outputTokens":0,"cacheCreationTokens":0,"cacheReadTokens":0,"totalTokens":1,`+
			`"modelsUsed":[%q],"modelBreakdowns":[{"modelName":%q,"inputTokens":1,"outputTokens":0,"cacheCreationTokens":0,"cacheReadTokens":0}]}],%s}`,
			observed, observed, standInTotals)
	}
	return 0
}

func standInRunner(t *testing.T) Runner {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	return Runner{Path: path, digest: hex.EncodeToString(digest[:])}
}

func TestRunnerBuildsTheExactCommand(t *testing.T) {
	t.Parallel()
	base := standInRunner(t)
	for _, agent := range Agents() {
		t.Run(string(agent), func(t *testing.T) {
			t.Parallel()
			var command Command
			var configured bool
			runner := base
			runner.Getenv = func(name string) string {
				return map[string]string{
					"HOME": "/synthetic/home", "GROK_HOME": "/synthetic/grok", "XDG_DATA_HOME": "/synthetic/data",
					// None of these may reach ccusage.
					"TOKENHOLIC_API_URL": "https://synthetic.invalid", "GITHUB_TOKEN": "synthetic-secret",
					"HTTPS_PROXY": "http://synthetic.invalid:8080", "GEMINI_DATA_DIR": "/synthetic/elsewhere",
					"CLAUDE_CONFIG_DIR": "/synthetic/claude", "PATH": "/synthetic/bin", "CCUSAGE_OFFLINE": "0",
				}[name]
			}
			runner.execute = func(_ context.Context, issued Command) ([]byte, error) {
				command = issued
				_, err := os.Stat(filepath.Join(issued.Directory, ".ccusage", "ccusage.json"))
				configured = err == nil
				return readFixture(t, "empty-daily.json"), nil
			}
			if _, err := runner.Daily(context.Background(), agent); err != nil {
				t.Fatal(err)
			}
			wantArguments := []string{string(agent), "daily", "--json", "--no-cost", "--offline", "--timezone", "UTC"}
			if command.Path != base.Path || !slices.Equal(command.Arguments, wantArguments) {
				t.Fatalf("command = %q %q, want %q %q", command.Path, command.Arguments, base.Path, wantArguments)
			}
			wantEnvironment := []string{
				"HOME=/synthetic/home", "XDG_DATA_HOME=/synthetic/data", "GROK_HOME=/synthetic/grok",
				"CCUSAGE_OFFLINE=1", "LOG_LEVEL=0", "NO_COLOR=1",
				"HTTP_PROXY=" + deadProxy, "HTTPS_PROXY=" + deadProxy, "ALL_PROXY=" + deadProxy,
				"http_proxy=" + deadProxy, "https_proxy=" + deadProxy, "all_proxy=" + deadProxy,
			}
			if !slices.Equal(command.Environment, wantEnvironment) {
				t.Fatalf("environment = %q, want %q", command.Environment, wantEnvironment)
			}
			if !configured {
				t.Fatal("the working directory had no empty ccusage configuration")
			}
			if _, err := os.Stat(command.Directory); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the private working directory was left behind: %v", err)
			}
		})
	}
}

func TestRunnerRefusesAnythingButThePinnedExecutable(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	other := filepath.Join(directory, "ccusage")
	if err := os.WriteFile(other, []byte("#!/bin/sh\necho another release\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	pinned := standInRunner(t)
	tests := []struct {
		name   string
		runner Runner
		want   error
	}{
		{name: "no executable configured", runner: Runner{}, want: ErrUnavailable},
		{name: "blank path", runner: Runner{Path: "  "}, want: ErrUnavailable},
		{name: "executable missing", runner: Runner{Path: filepath.Join(directory, "absent")}, want: ErrUnavailable},
		{name: "another file", runner: Runner{Path: other}, want: ErrUnpinned},
		{name: "another release for this platform", runner: Runner{Path: pinned.Path}, want: ErrUnpinned},
		{name: "a directory", runner: Runner{Path: directory}, want: ErrUnpinned},
		{name: "a platform without a build", runner: Runner{Path: other, goos: "freebsd", goarch: "amd64"}, want: ErrUnpinned},
		{name: "the pinned file under another platform's digest", runner: Runner{Path: pinned.Path, goos: "linux", goarch: "amd64"}, want: ErrUnpinned},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := test.runner
			runner.execute = func(context.Context, Command) ([]byte, error) {
				t.Error("an unverified executable was started")
				return nil, nil
			}
			if err := runner.Verify(); !errors.Is(err, test.want) {
				t.Fatalf("Verify() = %v, want %v", err, test.want)
			}
			if _, err := runner.Daily(context.Background(), AgentGemini); !errors.Is(err, test.want) {
				t.Fatalf("Daily() = %v, want %v", err, test.want)
			}
		})
	}
	if err := pinned.Verify(); err != nil {
		t.Fatalf("Verify() of the pinned file = %v", err)
	}
}

func TestRunnerClassifiesFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		agent   Agent
		timeout time.Duration
		execute func(context.Context, Command) ([]byte, error)
		want    error
	}{
		{name: "malformed output", agent: AgentGemini, execute: func(context.Context, Command) ([]byte, error) {
			return []byte(`{"daily":[]}`), nil
		}, want: ErrMalformed},
		{name: "output of another report shape", agent: AgentGemini, execute: func(context.Context, Command) ([]byte, error) {
			return []byte(`{"type":"daily","data":[],"summary":{}}`), nil
		}, want: ErrMalformed},
		{name: "empty output", agent: AgentGemini, execute: func(context.Context, Command) ([]byte, error) {
			return nil, nil
		}, want: ErrMalformed},
		{name: "exit with an error", agent: AgentGemini, execute: func(context.Context, Command) ([]byte, error) {
			return nil, errors.New("exit status 2: /synthetic/private/location")
		}, want: ErrFailed},
		{name: "time limit", agent: AgentGemini, timeout: 20 * time.Millisecond, execute: func(ctx context.Context, _ Command) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}, want: ErrTimeout},
		{name: "an agent outside the contract", agent: Agent("claude"), want: ErrFailed},
		{name: "a second argument smuggled in the agent", agent: Agent("gemini --since 20260101"), want: ErrFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := standInRunner(t)
			runner.Timeout = test.timeout
			runner.execute = test.execute
			if runner.execute == nil {
				runner.execute = func(context.Context, Command) ([]byte, error) {
					t.Error("an agent outside the contract was run")
					return nil, nil
				}
			}
			_, err := runner.Daily(context.Background(), test.agent)
			if !errors.Is(err, test.want) {
				t.Fatalf("Daily() = %v, want %v", err, test.want)
			}
			if strings.Contains(err.Error(), "/synthetic/private") {
				t.Fatalf("the error repeats ccusage's own output: %v", err)
			}
		})
	}
}

func TestRunnerReturnsTheCallersCancellation(t *testing.T) {
	t.Parallel()
	runner := standInRunner(t)
	ctx, cancel := context.WithCancel(context.Background())
	runner.execute = func(ctx context.Context, _ Command) ([]byte, error) {
		cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if _, err := runner.Daily(ctx, AgentGemini); !errors.Is(err, context.Canceled) {
		t.Fatalf("Daily() = %v, want the caller's cancellation", err)
	}
}

// These cases start a real child process: the test binary acting as ccusage.
func TestRunnerStartsARealProcess(t *testing.T) {
	t.Parallel()
	t.Run("validated report, shielded environment", func(t *testing.T) {
		t.Parallel()
		runner := standInRunner(t)
		runner.Getenv = func(name string) string {
			return map[string]string{"HOME": t.TempDir(), "GITHUB_TOKEN": "synthetic-secret", "SystemRoot": os.Getenv("SystemRoot")}[name]
		}
		report, err := runner.Daily(context.Background(), AgentOpenCode)
		if err != nil {
			t.Fatal(err)
		}
		observed := report.Days[0].Models[0].Model
		if !strings.HasPrefix(observed, "config=true ") {
			t.Fatalf("the process did not start beside an empty configuration: %s", observed)
		}
		for _, absent := range []string{"GITHUB_TOKEN", "PATH", "TOKENHOLIC"} {
			if strings.Contains(observed, absent) {
				t.Fatalf("the process inherited %s: %s", absent, observed)
			}
		}
		for _, present := range []string{"HOME", "CCUSAGE_OFFLINE", "HTTPS_PROXY"} {
			if !strings.Contains(observed, present) {
				t.Fatalf("the process was not given %s: %s", present, observed)
			}
		}
	})
	failures := []struct {
		name    string
		agent   Agent
		timeout time.Duration
		want    error
	}{
		{name: "text instead of JSON", agent: AgentGrok, want: ErrMalformed},
		{name: "oversized output", agent: AgentGemini, want: ErrMalformed},
		{name: "non-zero exit", agent: AgentCopilot, want: ErrFailed},
		{name: "killed at the time limit", agent: AgentQwen, timeout: 200 * time.Millisecond, want: ErrTimeout},
	}
	for _, test := range failures {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := standInRunner(t)
			runner.Timeout = test.timeout
			runner.Getenv = func(name string) string {
				if name == "SystemRoot" {
					return os.Getenv(name)
				}
				return ""
			}
			started := time.Now()
			_, err := runner.Daily(context.Background(), test.agent)
			if !errors.Is(err, test.want) {
				t.Fatalf("Daily() = %v, want %v", err, test.want)
			}
			if strings.Contains(err.Error(), "/synthetic/private") {
				t.Fatalf("the error repeats ccusage's own output: %v", err)
			}
			if elapsed := time.Since(started); elapsed > 20*time.Second {
				t.Fatalf("the report took %s; the time limit did not stop it", elapsed)
			}
		})
	}
}
