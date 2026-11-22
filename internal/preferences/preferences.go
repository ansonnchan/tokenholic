// Package preferences keeps the few choices a user makes about tokenholic
// itself, such as the TUI's look, in one small private file beside the usage
// database. It holds no usage, no account, and no credential.
//
// The choices live in a file rather than in the database so that adding one
// never changes the database schema, which an older scheduled copy of
// tokenholic would then refuse to open.
package preferences

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ansonnchan/tokenholic/internal/privatefs"
	"github.com/ansonnchan/tokenholic/internal/store"
)

const fileName = "preferences.json"

// Preferences are the saved choices. A missing field means the default.
type Preferences struct {
	// Theme is "dark" or "light". Anything else reads as the default.
	Theme string `json:"theme,omitempty"`
}

// DefaultPath is the preferences file beside the default usage database.
func DefaultPath() (string, error) {
	database, err := store.DefaultPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(database), fileName), nil
}

// Load reads the saved choices. A file that does not exist yet is not an
// error: it yields the defaults. A file that cannot be read or parsed yields
// the defaults and the reason, so a caller can carry on with them.
func Load(path string) (Preferences, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Preferences{}, nil
	}
	if err != nil {
		return Preferences{}, fmt.Errorf("read preferences: %w", err)
	}
	var saved Preferences
	if err := json.Unmarshal(contents, &saved); err != nil {
		return Preferences{}, fmt.Errorf("decode preferences: %w", err)
	}
	return saved, nil
}

// Save replaces the preferences file with the given choices. The file and
// its directory are private, like everything else tokenholic keeps there.
func Save(path string, choices Preferences) error {
	if !filepath.IsAbs(path) {
		return errors.New("save preferences: path must be absolute")
	}
	if err := privatefs.MkdirAll(filepath.Dir(path)); err != nil {
		return fmt.Errorf("save preferences: %w", err)
	}
	encoded, err := json.MarshalIndent(choices, "", "  ")
	if err != nil {
		return fmt.Errorf("encode preferences: %w", err)
	}
	// Write beside the destination and rename, so a crash never leaves half
	// a file where the next launch would read it.
	temporary, err := os.CreateTemp(filepath.Dir(path), ".preferences-*")
	if err != nil {
		return fmt.Errorf("save preferences: %w", err)
	}
	defer os.Remove(temporary.Name())
	if _, err := temporary.Write(append(encoded, '\n')); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("save preferences: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("save preferences: %w", err)
	}
	if err := privatefs.RestrictFile(temporary.Name()); err != nil {
		return fmt.Errorf("save preferences: %w", err)
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return fmt.Errorf("save preferences: %w", err)
	}
	return nil
}
