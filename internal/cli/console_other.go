//go:build !windows

package cli

// detachConsole does nothing where a scheduled job gets no console window.
func detachConsole() {}
