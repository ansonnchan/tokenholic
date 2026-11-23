package adapters

// DefaultRoots returns the evidence roots for the system tokenholic runs on.
func DefaultRoots(environment Environment) []RootSpec {
	roots := MacRoots(environment.Home, environment.CodexHome)
	return withClaudeConfigDir(roots, environment.Home, environment.lookup(ClaudeConfigDirVariable))
}
