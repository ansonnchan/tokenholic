package adapters

// DefaultRoots returns the evidence roots for the system tokenholic runs on.
func DefaultRoots(environment Environment) []RootSpec {
	return WindowsRoots(environment.Home, environment.CodexHome, environment.lookup("APPDATA"), environment.lookup("LOCALAPPDATA"))
}
