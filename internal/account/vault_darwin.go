package account

// VaultHelp says, in words safe to show, which credential store this system
// uses and what to do when it cannot be used.
func VaultHelp() string {
	return "tokenholic keeps its sign-in in the macOS login keychain, which could not be used. Unlock the keychain and try again."
}
