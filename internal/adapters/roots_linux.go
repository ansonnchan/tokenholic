package adapters

// DefaultRoots returns the evidence roots for the system tokenholic runs on.
func DefaultRoots(environment Environment) []RootSpec {
	roots := LinuxRoots(environment.Home, environment.CodexHome, environment.lookup("XDG_CONFIG_HOME"))
	return withClaudeConfigDir(roots, environment.Home, environment.lookup(ClaudeConfigDirVariable))
}
