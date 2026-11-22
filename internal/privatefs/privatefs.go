package privatefs

import (
	"fmt"
	"os"
)

// MkdirAll creates a directory with any missing parents and makes the
// directory itself private. Parents that already existed are left alone.
func MkdirAll(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create private directory: %w", err)
	}
	if err := restrict(path, true); err != nil {
		return fmt.Errorf("restrict private directory: %w", err)
	}
	return nil
}

// RestrictFile makes an existing regular file private.
func RestrictFile(path string) error {
	if err := restrict(path, false); err != nil {
		return fmt.Errorf("restrict private file: %w", err)
	}
	return nil
}

// Touch creates an empty private file when none exists and makes an existing
// one private, without changing its contents.
func Touch(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create private file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("create private file: %w", err)
	}
	return RestrictFile(path)
}
