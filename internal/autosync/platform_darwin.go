package autosync

import "os"

func platformScheduler(config Config) (scheduler, error) {
	return launchd{home: config.Home, uid: os.Getuid(), run: runCommand}, nil
}
