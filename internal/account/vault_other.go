//go:build !darwin && !linux

package account

// VaultHelp says, in words safe to show, which credential store this system
// uses and what to do when it cannot be used.
func VaultHelp() string {
	return "tokenholic found no operating-system credential store it can use here, and it never writes a credential to a file."
}
