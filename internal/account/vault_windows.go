package account

// VaultHelp says, in words safe to show, which credential store this system
// uses and what to do when it cannot be used.
func VaultHelp() string {
	return "tokenholic keeps its sign-in in Windows Credential Manager, which could not be used. " +
		"Check that the Credential Manager service is running and try again. tokenholic never writes a credential to a file."
}
