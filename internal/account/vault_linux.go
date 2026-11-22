package account

// VaultHelp says, in words safe to show, which credential store this system
// uses and what to do when it cannot be used. On Linux that store is the
// Secret Service on the session D-Bus; a headless machine often has none.
func VaultHelp() string {
	return "tokenholic keeps its sign-in in the desktop keyring (the Secret Service: GNOME Keyring, KWallet, or KeePassXC), and none answered. " +
		"Start and unlock one, then try again. tokenholic never writes a credential to a file."
}
