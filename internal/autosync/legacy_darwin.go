package autosync

// lockDirectories lists every application directory whose sync.lock must be
// held. macOS releases before the lowercase rename used a capitalized
// directory, and a scheduled binary from one of them may still be running.
func lockDirectories() []string {
	return []string{applicationDirectory, legacyApplicationDirectory()}
}
