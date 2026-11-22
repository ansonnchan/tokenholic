package autosync

func platformScheduler(config Config) (scheduler, error) {
	return newSystemd(config.Home, config.Getenv, runCommand), nil
}
