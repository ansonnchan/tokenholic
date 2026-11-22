package preferences

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ansonnchan/tokenholic/internal/privatefs"
)

func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokenholic", fileName)
	// Nothing saved yet is the defaults, not an error.
	if saved, err := Load(path); err != nil || saved != (Preferences{}) {
		t.Fatalf("Load() before saving = %+v, %v", saved, err)
	}
	for _, theme := range []string{"light", "dark", "light"} {
		if err := Save(path, Preferences{Theme: theme}); err != nil {
			t.Fatal(err)
		}
		if saved, err := Load(path); err != nil || saved.Theme != theme {
			t.Fatalf("Load() = %+v, %v; want theme %q", saved, err, theme)
		}
	}
	for _, private := range []string{path, filepath.Dir(path)} {
		if ok, err := privatefs.IsPrivate(private); err != nil || !ok {
			t.Fatalf("%s is not private: %t, %v", filepath.Base(private), ok, err)
		}
	}
	// Saving leaves nothing but the file behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("directory holds %d entries, %v; want only the preferences file", len(entries), err)
	}
}

func TestLoadSurvivesADamagedFile(t *testing.T) {
	directory := t.TempDir()
	for name, contents := range map[string]string{
		"not JSON":      "theme = light",
		"wrong type":    `{"theme": 7}`,
		"empty":         "",
		"unknown field": `{"theme": "light", "later": true}`,
	} {
		path := filepath.Join(directory, name+".json")
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		saved, err := Load(path)
		if name == "unknown field" {
			// A newer version's file still reads.
			if err != nil || saved.Theme != "light" {
				t.Errorf("%s: %+v, %v", name, saved, err)
			}
			continue
		}
		// The defaults come back with the reason, so a launch can carry on.
		if err == nil || saved != (Preferences{}) {
			t.Errorf("%s: %+v, %v; want the defaults and an error", name, saved, err)
		}
	}
}

func TestSaveRequiresAnAbsolutePath(t *testing.T) {
	if err := Save(fileName, Preferences{Theme: "dark"}); err == nil {
		t.Fatal("Save() accepted a relative path")
	}
}
