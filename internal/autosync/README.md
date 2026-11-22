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
| Windows | Task Scheduler task `tokenholic-autosync` | `task.xml` and `job.json` in `%LOCALAPPDATA%\tokenholic\autosync` |
| anything else | none | Every command reports that autosync is not supported |

Each scheduler is ordinary Go that is compiled and tested on every system, so
its generated files are checked wherever the suite runs. Only
`platform_<os>.go`, which picks the scheduler, is build-tagged.

## Files it owns

| | macOS | Linux | Windows |
| --- | --- | --- | --- |
| Stable executable | `~/Library/Application Support/tokenholic/bin/tokenholic` | `$XDG_DATA_HOME/tokenholic/bin/tokenholic` | `%LOCALAPPDATA%\tokenholic\autosync\tokenholic.exe` |
| Logs | `~/Library/Logs/tokenholic/` | `$XDG_STATE_HOME/tokenholic/` | the same `autosync` folder |

The XDG variables fall back to `~/.config`, `~/.local/share`, and
`~/.local/state` when unset, empty, or relative. Directories are owner-only
and the two log files are created owner-only before the scheduler first
writes to them. "Owner-only" is the permission bits on Unix and a protected
single-user access-control list on Windows; `internal/privatefs` owns both. Disabling removes these files and the definition files, then
any directory that is left empty. It never touches the usage database or the
stored sign-in.

## What the job runs

The stable executable with the single argument `sync`, and a fixed
environment: `CODEX_HOME` when the user had it set, `NO_COLOR`, on Linux
`XDG_CONFIG_HOME` when the user had it set, and the API address only when the
user had overridden it with `TOKENHOLIC_API_URL`. Without an override the job
uses the address built into the executable it runs, so re-enabling after an
upgrade follows a hosted API that has moved. A
scheduler does not inherit a shell's exports, and without the last one the
job would look for the database and the lock somewhere else. Nothing else
from the caller's environment is copied, and no credential is ever written:
the job reads the sign-in from the OS credential store like any other run.

## Windows specifics

- The task runs as the signed-in user with an interactive token, only while
  they are signed in, at least privilege. No password is stored with it.
  Triggers are sign-in, after a one-minute delay, and every hour from the
  moment it was enabled. Enabling also starts it once.
- Task Scheduler starts a bare program: it cannot set an environment or
  redirect output. The task therefore runs `tokenholic autosync run
  <job.json>`. The job file holds the allowlisted environment and the two
  log paths, and `LoadScheduledJob` refuses any other variable, so the file
  cannot steer the process. The command is not offered to users.
- Everything autosync owns is in one private folder, so disabling removes
  the folder. The task is registered at the scheduler's root so that
  deleting it leaves no empty task folder behind.
- `schtasks` prints messages in the system's language, so only its exit code
  is read.
- Windows refuses to delete or replace a program that is still running, and
  a task told to stop takes a moment to go. Removing and replacing the stable
  executable therefore retry for about three seconds before reporting the
  refusal. The Windows CI runner found this.
- A scheduled console program can show a console window when it starts. The
  job gives the console up immediately, but the window may flash first.
  Whether it does is part of the manual run in
  [release validation](../../docs/release-validation.md).

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
lock, `tokenholic/sync.lock` beside the usage database, so two never run at
once. The second one reports that a sync is already running. The lock is
`flock` on Unix and `LockFileEx` on Windows. Both belong to the open file,
and the operating system drops them when the holder exits, however it exits,
so a crashed sync never blocks the next. On macOS the lock also covers the
capitalized directory that releases before the rename used.
