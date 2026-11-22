package autosync

import (
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func syntheticJob(root string) job {
	return job{
		installation: installation{
			binary: filepath.Join(root, "bin", "tokenholic"),
			stdout: filepath.Join(root, "logs", "autosync.log"),
			stderr: filepath.Join(root, "logs", "autosync-error.log"),
		},
		environment: []variable{
			{"TOKENHOLIC_API_URL", "https://api.example.test"},
			{"CODEX_HOME", filepath.Join(root, "codex home")},
			{"NO_COLOR", "1"},
		},
	}
}

func TestLaunchdDefinition(t *testing.T) {
	root := t.TempDir()
	work := syntheticJob(root)
	definitions, err := launchd{home: root, uid: 501}.definitions(work)
	if err != nil || len(definitions) != 1 {
		t.Fatalf("definitions = %d, %v", len(definitions), err)
	}
	plist := string(definitions[0].contents)
	if want := filepath.Join(root, "Library", "LaunchAgents", launchLabel+".plist"); definitions[0].path != want {
		t.Fatalf("plist path = %q", definitions[0].path)
	}
	for _, want := range []string{
		"<string>" + launchLabel + "</string>", "<string>" + work.binary + "</string>", "<string>sync</string>",
		"<integer>3600</integer>", "<key>RunAtLoad</key>", "<key>TOKENHOLIC_API_URL</key>",
		"<key>CODEX_HOME</key>", "<key>NO_COLOR</key>", work.stdout, work.stderr,
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("plist does not contain %q:\n%s", want, plist)
		}
	}
	decoder := xml.NewDecoder(strings.NewReader(plist))
	for {
		if _, err := decoder.Token(); err == io.EOF {
			break
		} else if err != nil {
			t.Fatalf("plist XML = %v", err)
		}
	}
	if runtime.GOOS == "darwin" {
		path := filepath.Join(root, "check.plist")
		if err := os.WriteFile(path, definitions[0].contents, 0o600); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.Command("plutil", "-lint", path).CombinedOutput(); err != nil {
			t.Fatalf("plutil = %v: %s", err, output)
		}
	}
}

func TestLaunchdDefinitionEscapesMarkup(t *testing.T) {
	work := syntheticJob(t.TempDir())
	work.environment = []variable{{"CODEX_HOME", `/synthetic/<a>&"b"`}}
	definitions, err := launchd{home: t.TempDir(), uid: 501}.definitions(work)
	if err != nil {
		t.Fatal(err)
	}
	if plist := string(definitions[0].contents); !strings.Contains(plist, "/synthetic/&lt;a&gt;&amp;&quot;b&quot;") {
		t.Fatalf("plist did not escape markup:\n%s", plist)
	}
}

func TestSystemdDefinitions(t *testing.T) {
	root := t.TempDir()
	work := syntheticJob(root)
	scheduler := newSystemd(root, func(string) string { return "" }, nil)
	definitions, err := scheduler.definitions(work)
	if err != nil || len(definitions) != 2 {
		t.Fatalf("definitions = %d, %v", len(definitions), err)
	}
	units := filepath.Join(root, ".config", "systemd", "user")
	service, timer := definitions[0], definitions[1]
	if service.path != filepath.Join(units, systemdService) || timer.path != filepath.Join(units, systemdTimer) {
		t.Fatalf("unit paths = %q, %q", service.path, timer.path)
	}
	for _, want := range []string{
		"[Service]\nType=oneshot\n",
		"ExecStart=" + systemdQuote(work.binary) + " sync\n",
		`Environment="TOKENHOLIC_API_URL=https://api.example.test"` + "\n",
		"Environment=" + systemdQuote("CODEX_HOME="+filepath.Join(root, "codex home")) + "\n",
		`Environment="NO_COLOR=1"` + "\n",
		"StandardOutput=append:" + work.stdout + "\n",
		"StandardError=append:" + work.stderr + "\n",
	} {
		if !strings.Contains(string(service.contents), want) {
			t.Errorf("service unit does not contain %q:\n%s", want, service.contents)
		}
	}
	for _, want := range []string{
		"[Timer]\nOnStartupSec=1min\nOnUnitActiveSec=3600s\nUnit=" + systemdService + "\n",
		"[Install]\nWantedBy=timers.target\n",
	} {
		if !strings.Contains(string(timer.contents), want) {
			t.Errorf("timer unit does not contain %q:\n%s", want, timer.contents)
		}
	}
	verifySystemdUnits(t, root, work, definitions)
}

// verifySystemdUnits asks systemd itself to parse the generated units, on
// machines that have it. The job's executable must exist for that check.
func verifySystemdUnits(t *testing.T, root string, work job, definitions []definition) {
	t.Helper()
	analyzer, err := exec.LookPath("systemd-analyze")
	if runtime.GOOS != "linux" || err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(work.binary), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(work.binary, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "units")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, definition := range definitions {
		path := filepath.Join(directory, filepath.Base(definition.path))
		if err := os.WriteFile(path, definition.contents, 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	output, err := exec.Command(analyzer, append([]string{"verify", "--user"}, paths...)...).CombinedOutput()
	// Only complaints about these units count; a build machine's own units
	// and missing session are not this test's concern.
	for _, line := range strings.Split(string(output), "\n") {
		if strings.Contains(line, "tokenholic-autosync") {
			t.Errorf("systemd-analyze (%v): %s", err, line)
		}
	}
}

func TestSystemdQuoting(t *testing.T) {
	for _, test := range []struct{ value, quoted string }{
		{"/home/synthetic/bin/tokenholic", `"/home/synthetic/bin/tokenholic"`},
		{"/home/two words/bin", `"/home/two words/bin"`},
		{`/home/a"b`, `"/home/a\"b"`},
		{`/home/a\b`, `"/home/a\\b"`},
		{"/home/100%", `"/home/100%%"`},
	} {
		if got := systemdQuote(test.value); got != test.quoted {
			t.Errorf("systemdQuote(%q) = %s, want %s", test.value, got, test.quoted)
		}
	}
}

func TestSystemdRefusesValuesItCannotRepresent(t *testing.T) {
	for name, change := range map[string]func(*job){
		"newline in a path":        func(work *job) { work.stdout = "/synthetic/a\nExecStart=/bin/false" },
		"newline in a variable":    func(work *job) { work.environment = []variable{{"CODEX_HOME", "/synthetic\n[Service]"}} },
		"dollar in the executable": func(work *job) { work.binary = "/synthetic/$HOME/tokenholic" },
	} {
		work := syntheticJob("/synthetic")
		change(&work)
		if _, err := newSystemd("/synthetic", func(string) string { return "" }, nil).definitions(work); err == nil {
			t.Errorf("%s: definitions succeeded", name)
		}
	}
}

func TestSystemdHonorsXDGBaseDirectories(t *testing.T) {
	home := t.TempDir()
	custom := t.TempDir()
	for _, test := range []struct {
		name        string
		environment map[string]string
		units       string
		binary      string
		logs        string
		preserved   []variable
	}{
		{
			name:   "defaults under the home directory",
			units:  filepath.Join(home, ".config", "systemd", "user"),
			binary: filepath.Join(home, ".local", "share", "tokenholic", "bin", "tokenholic"),
			logs:   filepath.Join(home, ".local", "state", "tokenholic"),
		},
		{
			name: "every base directory set",
			environment: map[string]string{
				"XDG_CONFIG_HOME": filepath.Join(custom, "config"),
				"XDG_DATA_HOME":   filepath.Join(custom, "data"),
				"XDG_STATE_HOME":  filepath.Join(custom, "state"),
			},
			units:     filepath.Join(custom, "config", "systemd", "user"),
			binary:    filepath.Join(custom, "data", "tokenholic", "bin", "tokenholic"),
			logs:      filepath.Join(custom, "state", "tokenholic"),
			preserved: []variable{{"XDG_CONFIG_HOME", filepath.Join(custom, "config")}},
		},
		{
			name:        "relative values are ignored, as the specification requires",
			environment: map[string]string{"XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "data", "XDG_STATE_HOME": "state"},
			units:       filepath.Join(home, ".config", "systemd", "user"),
			binary:      filepath.Join(home, ".local", "share", "tokenholic", "bin", "tokenholic"),
			logs:        filepath.Join(home, ".local", "state", "tokenholic"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			scheduler := newSystemd(home, func(name string) string { return test.environment[name] }, nil)
			paths := scheduler.layout()
			if filepath.Dir(paths.definitions[0]) != test.units || paths.binary != test.binary || paths.logs != test.logs {
				t.Fatalf("layout = %#v", paths)
			}
			definitions, err := scheduler.definitions(syntheticJob(home))
			if err != nil {
				t.Fatal(err)
			}
			// The job must find the same database and lock as the shell did.
			carried := strings.Contains(string(definitions[0].contents), "XDG_CONFIG_HOME=")
			if carried != (len(test.preserved) > 0) || len(scheduler.preserved) != len(test.preserved) {
				t.Fatalf("preserved = %#v, in unit = %t", scheduler.preserved, carried)
			}
			for index, want := range test.preserved {
				if scheduler.preserved[index] != want {
					t.Fatalf("preserved[%d] = %#v, want %#v", index, scheduler.preserved[index], want)
				}
			}
		})
	}
}
