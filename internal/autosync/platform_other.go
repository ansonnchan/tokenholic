//go:build !darwin && !linux

package autosync

func platformScheduler(Config) scheduler { return unsupported{} }
