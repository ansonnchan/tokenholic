//go:build !darwin && !linux && !windows

package autosync

func platformScheduler(Config) (scheduler, error) { return unsupported{}, nil }
