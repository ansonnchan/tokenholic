# Autosync

Autosync runs `tokenholic sync` at login and hourly through the operating
system's own per-user scheduler. It is installed only when the user runs
`tokenholic autosync enable`, stores no credential, and is removed completely
by `tokenholic autosync disable`.

## Shape

`Manager` is the same on every system. It validates the API origin, copies the
running executable to a stable private location, writes the scheduler's files,
and asks the scheduler to load them. A `scheduler` supplies what differs: where
files go, what they contain, and which commands load, inspect, and unload them.

| System | Scheduler | Definition files |
| --- | --- | --- |
| macOS | `launchd` agent in the user's GUI domain | `~/Library/LaunchAgents/io.tokenholic.autosync.plist` |
| Linux | systemd user timer | `tokenholic-autosync.service` and `tokenholic-autosync.timer` in `$XDG_CONFIG_HOME/systemd/user` |
| anything else | none | Every command reports that autosync is not supported |

Each scheduler is ordinary Go that is compiled and tested on every system, so
its generated files are checked wherever the suite runs. Only
`platform_<os>.go`, which picks the scheduler, is build-tagged.

## Files it owns

| | macOS | Linux |
| --- | --- | --- |
| Stable executable | `~/Library/Application Support/tokenholic/bin/tokenholic` | `$XDG_DATA_HOME/tokenholic/bin/tokenholic` |
| Logs | `~/Library/Logs/tokenholic/` | `$XDG_STATE_HOME/tokenholic/` |

The XDG variables fall back to `~/.config`, `~/.local/share`, and
`~/.local/state` when unset, empty, or relative. Directories are owner-only
and the two log files are created owner-only before the scheduler first
writes to them. Disabling removes these files and the definition files, then
any directory that is left empty. It never touches the usage database or the
stored sign-in.

## What the job runs

The stable executable with the single argument `sync`, and a fixed
environment: the API origin, `CODEX_HOME` when the user had it set,
`NO_COLOR`, and on Linux `XDG_CONFIG_HOME` when the user had it set. A
scheduler does not inherit a shell's exports, and without the last one the
job would look for the database and the lock somewhere else. Nothing else
from the caller's environment is copied, and no credential is ever written:
the job reads the sign-in from the OS credential store like any other run.

## Linux specifics

- The timer uses `OnStartupSec=1min` and `OnUnitActiveSec=1h`. A timer started
  after its first moment has passed runs at once, so enabling mid-session
  syncs immediately, as launchd's `RunAtLoad` does.
- It is a user unit. It runs while the user has a session and starts again
  at the next login. tokenholic does not enable lingering, which would keep
  it running after logout and is the user's decision to make.
- Output goes to the private log files through `append:`, which needs systemd
  240 or newer. An older systemd ignores those lines and keeps the output in
  the user journal, which `disable` cannot remove.
- A machine with no reachable user service manager (a container, WSL without
  systemd, a session with no D-Bus) gets a message naming the requirement.
  Nothing is written, and files from an earlier install can still be removed.
- Values are quoted for systemd. A path it cannot represent faithfully, one
  with a control character or a `$` in the executable path, is refused.

## The lock

Manual and scheduled synchronization take the same non-blocking exclusive
lock, `tokenholic/sync.lock` beside the usage database in the user
configuration directory, so two never run at once. The second one reports
that a sync is already running. On macOS the lock also covers the capitalized
directory that releases before the rename used.
