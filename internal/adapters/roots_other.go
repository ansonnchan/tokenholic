//go:build !darwin && !linux && !windows

package adapters

// DefaultRoots returns no roots: no provider location has been confirmed on
// this system, and discovery never guesses one.
func DefaultRoots(Environment) []RootSpec { return nil }
