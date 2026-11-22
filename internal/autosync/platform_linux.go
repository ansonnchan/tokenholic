package autosync

func platformScheduler(config Config) scheduler {
	return newSystemd(config.Home, config.Getenv, runCommand)
}
