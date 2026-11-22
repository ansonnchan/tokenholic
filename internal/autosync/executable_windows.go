package autosync

import "os"

// runnable reports whether a regular file can be executed. Windows has no
// execute permission bit; any regular file a user can read can be run.
func runnable(os.FileInfo) bool { return true }
