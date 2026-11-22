# Release validation

Checks a person runs by hand before a release, because no automated test can.
Everything CI can prove is in the test suite instead; this document covers
only what needs a real machine, a real account, or a real scheduler.

Use synthetic or your own data. Do not paste credentials, prompts, or paths
into issues or pull requests when reporting a result.

## Windows

CI builds, vets, and tests the Windows client on a Windows runner, registers
the generated task with the real Task Scheduler, and runs the x64 archive.
It cannot show a terminal, sign in, or log a user out and in. Run this once
on a real Windows 10 or 11 machine or virtual machine before a release that
changes Windows behavior. It takes about twenty minutes.

### Get a build

Until the release is on npm, build the archive on the development machine and
copy it over:

```sh
make release VERSION=0.2.0-rc.1
```

Copy `dist/tokenholic_0.2.0-rc.1_windows_x64.tar.gz` (or `windows_arm64`) to
the Windows machine and unpack it in PowerShell. Windows 10 and later include
`tar`:

```powershell
mkdir tokenholic-test; cd tokenholic-test
tar -xzf ..\tokenholic_0.2.0-rc.1_windows_x64.tar.gz
.\tokenholic.exe --version
```

Expect the version you built. Windows may ask to confirm running an unsigned
program; release binaries are not code-signed.

### 1. Sources and storage

```powershell
.\tokenholic.exe doctor
```

Look for:

- `Platform: windows/amd64` (or `arm64`) with no "not supported" note.
- One line each for Claude Code CLI, Claude Desktop Code, Agent, and Cowork,
  and Codex. Tools you use should say `detected` with a file count; tools you
  do not use should say `not found`. A tool you use that says `not found` is
  the result to report: note which tool and how it was installed (for Claude
  Desktop, the Microsoft Store or the downloaded installer).
- No path anywhere in the output.

### 2. The terminal interface

```powershell
.\tokenholic.exe
```

Run it once in Windows Terminal and once in the older console (`cmd.exe`
started from the Run dialog). Look for:

- The wordmark, panel borders, and bars drawn cleanly, in color. Broken
  box characters in the older console are worth reporting; setting
  `TOKENHOLIC_ASCII=1` is the fallback.
- Totals that match what the same tools show on another machine or what you
  expect from recent use. Unknown values must read as unknown, not zero.
- `1` to `5` switch views, `p` changes the period, `q` quits and leaves the
  terminal usable.

### 3. Private files

```powershell
icacls "$env:LOCALAPPDATA\tokenholic"
icacls "$env:LOCALAPPDATA\tokenholic\tokenholic.db"
```

Look for exactly one entry on each, your own account with full control
(`(OI)(CI)(F)` on the folder, `(F)` on the file), and no line for `Users`,
`Everyone`, `Administrators`, or `SYSTEM`. The database must be under
`AppData\Local`, not `AppData\Roaming`.

### 4. Sign-in and Credential Manager

```powershell
.\tokenholic.exe claim
cmdkey /list | findstr tokenholic
```

Look for the GitHub code prompt, `Signed in as <you>`, and an upload summary.
`cmdkey` should list one generic credential whose name starts with
`io.tokenholic.cli`. Then:

```powershell
.\tokenholic.exe sync
.\tokenholic.exe sync
```

The second run must report nothing new to upload, and your row on a board
must not grow.

### 5. The lock

Open two PowerShell windows and start `.\tokenholic.exe sync` in both at the
same moment. One should finish normally. The other should print
`synchronization is already running` and exit. If a first sync is too quick
to overlap, run `.\tokenholic.exe` in one window, press `r`, and sync from
the other while the scan runs.

### 6. Autosync

```powershell
.\tokenholic.exe autosync enable
schtasks /Query /TN tokenholic-autosync /V /FO LIST
Get-Content "$env:LOCALAPPDATA\tokenholic\autosync\autosync.log"
```

Look for:

- `Enabled autosync at login and hourly`.
- In the task listing: your account under `Run As User`, `Interactive only`
  under `Logon Mode`, and a next run time about an hour away.
- A `Synchronized ...` line in the log within a few seconds, from the run
  enabling starts. `autosync-error.log` beside it should be empty.
- **Whether a console window appears.** The task starts a console program in
  your session, so a window can flash when a run starts, at enabling and
  again each hour. Note whether it does, for how long, and whether it takes
  focus from what you were typing. This is the least certain part of the
  Windows work and the answer decides whether it needs a follow-up.

Sign out of Windows and back in, wait two minutes, and read the log again: it
should have gained a line about a minute after sign-in. Then:

```powershell
.\tokenholic.exe autosync status
.\tokenholic.exe autosync disable
schtasks /Query /TN tokenholic-autosync
Test-Path "$env:LOCALAPPDATA\tokenholic\autosync"
Test-Path "$env:LOCALAPPDATA\tokenholic\tokenholic.db"
```

After disabling, `schtasks` should report that the task does not exist, the
`autosync` folder should be gone (`False`), and the database should remain
(`True`). `cmdkey /list` should still show the sign-in.

### 7. Sign out

```powershell
.\tokenholic.exe logout
cmdkey /list | findstr tokenholic
```

`findstr` should find nothing.

### 8. The npm package

Once the release is published:

```powershell
npx --yes @tokenholic/cli@0.2.0 --version
```

### Known limits on Windows

- **Group timezone.** Windows names its timezones differently from the IANA
  names the server uses, and the client has no mapping between them yet, so
  a group created from Windows is created in UTC. The command prints the
  timezone it used. Day and week boundaries on that group's board follow UTC
  for every member. Create the group from macOS or Linux when that matters.
  Personal totals are not affected; they use the system's own clock.
- **Windows arm64** is built and checked for format but has no CI runner. If
  you have such a machine, steps 1 and 2 are the ones that matter.
- **WSL.** A coding agent run inside WSL keeps its records in the Linux home.
  The Windows build does not read them; run the Linux build inside WSL.
- **Console window.** See step 6.
