//go:build unix

package autosync

import "os"

// runnable reports whether a regular file can be executed by its owner.
func runnable(info os.FileInfo) bool {
	return info.Mode().Perm()&0o111 != 0
}
