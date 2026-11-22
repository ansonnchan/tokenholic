package adapters

// DefaultRoots returns the evidence roots for the system tokenholic runs on.
func DefaultRoots(environment Environment) []RootSpec {
	return LinuxRoots(environment.Home, environment.CodexHome, environment.lookup("XDG_CONFIG_HOME"))
}
