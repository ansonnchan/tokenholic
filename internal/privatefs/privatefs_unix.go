//go:build unix

package privatefs

import (
	"fmt"
	"os"
)

// ApplicationRoot returns the directory tokenholic's own directory lives in:
// the user configuration directory, which follows XDG_CONFIG_HOME on Linux.
func ApplicationRoot() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve user configuration directory: %w", err)
	}
	return root, nil
}

func restrict(path string, directory bool) error {
	if directory {
		return os.Chmod(path, 0o700)
	}
	return os.Chmod(path, 0o600)
}

// IsPrivate reports whether only the owner can open path.
func IsPrivate(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	return info.Mode().Perm()&0o077 == 0, nil
}
