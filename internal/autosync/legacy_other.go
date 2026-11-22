//go:build !darwin

package autosync

// lockDirectories lists every application directory whose sync.lock must be
// held. Only macOS ever shipped the capitalized spelling.
func lockDirectories() []string {
	return []string{applicationDirectory}
}
