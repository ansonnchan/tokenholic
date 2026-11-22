package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWriteArchiveIsDeterministic(t *testing.T) {
	entries := []archiveEntry{
		{name: binaryName, mode: 0o755, data: []byte("synthetic-binary")},
		{name: lockName, mode: 0o644, data: []byte("synthetic-lock")},
	}
	first := filepath.Join(t.TempDir(), "first.tar.gz")
	second := filepath.Join(t.TempDir(), "second.tar.gz")
	if err := writeArchive(first, entries); err != nil {
		t.Fatal(err)
	}
	if err := writeArchive(second, entries); err != nil {
		t.Fatal(err)
	}
	firstBytes, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatal("archives differ for identical inputs")
	}

	gzipReader, err := gzip.NewReader(bytes.NewReader(firstBytes))
	if err != nil {
		t.Fatal(err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	for index, want := range entries {
		header, err := tarReader.Next()
		if err != nil {
			t.Fatalf("entry %d: %v", index, err)
		}
		contents, err := io.ReadAll(tarReader)
		if err != nil {
			t.Fatal(err)
		}
		if header.Name != want.name || header.Mode != want.mode || !header.ModTime.Equal(time.Unix(0, 0)) || !bytes.Equal(contents, want.data) {
			t.Fatalf("entry %d = %#v/%q", index, header, contents)
		}
	}
	if _, err := tarReader.Next(); err != io.EOF {
		t.Fatalf("trailing archive entry: %v", err)
	}
}

func TestReleaseInputValidation(t *testing.T) {
	for _, version := range []string{"", "v1.2.3", "1", "1.2.3/unsafe", "1.2.3-", "1.2.3-foo..bar"} {
		var output strings.Builder
		if err := run([]string{"-version", version, "-output", t.TempDir()}, &output); err == nil {
			t.Fatalf("run(%q) succeeded", version)
		}
	}
}

func TestReplaceEnvironment(t *testing.T) {
	got := strings.Join(replaceEnvironment([]string{"PATH=/bin", "GOOS=linux", "GOARCH=386"}, map[string]string{
		"GOOS": "darwin", "GOARCH": "arm64", "CGO_ENABLED": "0",
	}), "\n")
	for _, want := range []string{"PATH=/bin", "CGO_ENABLED=0", "GOARCH=arm64", "GOOS=darwin"} {
		if !strings.Contains(got, want) {
			t.Fatalf("environment %q does not contain %q", got, want)
		}
	}
}

func TestTargetsNameEveryShippedPlatform(t *testing.T) {
	var names []string
	for _, target := range targets {
		if target.verify == nil {
			t.Fatalf("%s has no binary check", target)
		}
		names = append(names, target.archiveName("1.2.3"))
	}
	want := []string{
		"tokenholic_1.2.3_darwin_arm64.tar.gz", "tokenholic_1.2.3_darwin_x64.tar.gz",
		"tokenholic_1.2.3_linux_arm64.tar.gz", "tokenholic_1.2.3_linux_x64.tar.gz",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("archives = %q, want %q", names, want)
	}
}

// Each target's check must accept its own build and refuse every other
// target's, so an archive can never carry the wrong binary.
func TestTargetChecksAcceptOnlyTheirOwnBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("cross-compiles one small program per target")
	}
	source := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(source, []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	built := make(map[string]string, len(targets))
	for _, target := range targets {
		path := filepath.Join(t.TempDir(), "synthetic")
		command := exec.Command("go", "build", "-o", path, source)
		command.Env = replaceEnvironment(os.Environ(), map[string]string{
			"CGO_ENABLED": "0", "GOOS": target.goOS, "GOARCH": target.goArch,
		})
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v: %s", target, err, output)
		}
		built[target.String()] = path
	}
	for _, target := range targets {
		for name, path := range built {
			err := target.verify(path)
			if name == target.String() && err != nil {
				t.Errorf("%s refused its own binary: %v", target, err)
			}
			if name != target.String() && err == nil {
				t.Errorf("%s accepted the %s binary", target, name)
			}
		}
	}
}
