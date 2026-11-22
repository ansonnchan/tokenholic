package autosync

import "os"

func platformScheduler(config Config) scheduler {
	return launchd{home: config.Home, uid: os.Getuid(), run: runCommand}
}
