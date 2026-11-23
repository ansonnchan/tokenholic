package adapters

// DefaultRoots returns the evidence roots for the system tokenholic runs on.
func DefaultRoots(environment Environment) []RootSpec {
	roots := WindowsRoots(environment.Home, environment.CodexHome, environment.lookup("APPDATA"), environment.lookup("LOCALAPPDATA"))
	return withClaudeConfigDir(roots, environment.Home, environment.lookup(ClaudeConfigDirVariable))
}
