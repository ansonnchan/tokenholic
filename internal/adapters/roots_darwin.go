package adapters

// DefaultRoots returns the evidence roots for the system tokenholic runs on.
func DefaultRoots(environment Environment) []RootSpec {
	return MacRoots(environment.Home, environment.CodexHome)
}
