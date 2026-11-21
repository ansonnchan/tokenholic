// Package goldentest compares rendered TUI frames with golden snapshots. It
// is test support: only test files import it.
package goldentest

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

var update = flag.Bool("update", false, "rewrite golden snapshots")

// Compare checks a frame against testdata/<name>.golden in the calling
// package, or rewrites the file when the test runs with -update. Trailing
// spaces are not part of a frame, which keeps the files clean for
// git diff --check.
func Compare(t *testing.T, name string, lines []string) {
	t.Helper()
	trimmed := make([]string, len(lines))
	for index, line := range lines {
		trimmed[index] = strings.TrimRight(line, " ")
	}
	got := strings.Join(trimmed, "\n") + "\n"
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run the package's tests with -update to create it): %v", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from its golden snapshot; run the package's tests with -update and review the diff.\ngot:\n%s", name, got)
	}
}

// Fits fails the test for every line wider than width cells, and, when
// ascii is set, for every line with a character outside printable ASCII.
func Fits(t *testing.T, lines []string, width int, ascii bool) {
	t.Helper()
	for index, line := range lines {
		if cells := lipgloss.Width(line); cells > width {
			t.Errorf("line %d is %d cells, wider than %d: %q", index+1, cells, width, line)
		}
		if !ascii {
			continue
		}
		for _, character := range line {
			if character < 0x20 || character > 0x7e {
				t.Errorf("line %d has %q outside ASCII: %q", index+1, character, line)
				break
			}
		}
	}
}
