// Package autosync installs the explicit hourly sync schedule and owns the
// process lock shared by scheduled and manual synchronization.
//
// One Manager drives every operating system. What differs per system is the
// scheduler behind it: a launchd agent on macOS and a user-level systemd
// timer on Linux. Each scheduler is ordinary code that is compiled and tested
// everywhere; only the choice of scheduler is build-tagged.
package autosync
