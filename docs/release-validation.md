# Release validation

Checks a person runs by hand before a release, because no automated test can.
Everything CI can prove is in the test suite instead; this document covers
only what needs a real machine, a real account, a real scheduler, or the
deployed service.

Use synthetic or your own data. Do not paste credentials, prompts, or paths
into issues or pull requests when reporting a result.

Every step below is the owner's to run. One thing does happen on its own:
Render deploys the API on every push to `main`, so a merged pull request that
changes the server is live within minutes. Nothing else is automatic. The
website workflow stays skipped until a repository variable exists, and the
release workflow runs only on a version tag.

## Who sets what

No end user sets anything. These are the operator's settings, each set once:

| Setting | Where | What it is for |
| --- | --- | --- |
| `hostedAPI` | A constant in `cmd/release/main.go` | The API address every release build uses. Change it in a pull request if the service moves |
| `DATABASE_URL`, `GITHUB_CLIENT_ID`, `TOKENHOLIC_AUTH_STATE_KEY` | Render, on the API service | Required for the API to start. See the [deployment guide](./deployment.md) |
| `TOKENHOLIC_TRUSTED_PROXY_HOPS` | Render, on the API service | How many proxies sit in front of the API, so rate limits key on the real client. Measured in step 2 |
| `TOKENHOLIC_WEB_ORIGIN` | Render, on the API service | The website's address. Lets the site read the API and turns on share pages and invite links |
| `TOKENHOLIC_API_ORIGIN` | GitHub, a repository variable | The API's address, written into the published website |
| `NPM_TOKEN` | GitHub, a repository secret | Lets the release workflow publish the npm packages |

`TOKENHOLIC_API_URL` is not in this table on purpose. It is an override for
development and for trying a second deployment; a released client does not
need it.

## The 0.2 release, in order

The order matters: the server must speak the new protocol before clients that
need it are published, and the website needs the API's address before it is
built.

### 1. Confirm the API is running `main`

There is no deploy to start: Render deploys each push to `main`. Check that
the newest one finished, and that it is the commit you expect:

```sh
gh api 'repos/ansonnchan/tokenholic/deployments?per_page=1' --jq '.[0] | .sha, .created_at'
curl --fail --show-error https://tokenholic-api.onrender.com/healthz
```

Expect the head of `main` and `{"status":"ok"}`. The first request after an
idle period can take about a minute. In the Render log, a line `repriced N
usage buckets` or nothing at all is fine; a migration error is not. Demo
usage was already reset when the sync v2 server first deployed; see "Notes
to include in the next release" in [release artifacts](./releasing.md).

If the service's origin is not the one in `hostedAPI`, stop and change the
constant first. A release built with the wrong address cannot claim.

### 2. Measure the proxy chain

The log line this step reads exists from the release-preparation change
onward, so do it after that change has merged and deployed, before other
traffic:

```sh
curl --silent --output /dev/null https://tokenholic-api.onrender.com/v1/public/users/nobody
```

Find this line in the Render log:

```text
tokenholic api: a request arrived with N X-Forwarded-For entries; TOKENHOLIC_TRUSTED_PROXY_HOPS is 0
```

Set `TOKENHOLIC_TRUSTED_PROXY_HOPS` to that `N` on the Render service, let it
redeploy, send the request again, and confirm the new line shows the same `N`
beside the new setting. If `N` is 0, leave the variable unset and write that
down in the deployment guide: the platform is not adding the header. Record
the measured value and the date in `docs/deployment.md`, which still says it
has not been measured.

### 3. Publish the website

Follow section 6 of the [deployment guide](./deployment.md):

1. Repository **Settings → Pages**: source **GitHub Actions**.
2. Repository variable `TOKENHOLIC_API_ORIGIN` =
   `https://tokenholic-api.onrender.com`.
3. Run the **Website** workflow.
4. On Render, set `TOKENHOLIC_WEB_ORIGIN` to the address the site is served
   from, for example `https://ansonnchan.github.io/tokenholic`.

Then, in a browser:

- The landing page loads with the API asleep.
- `…/u/?login=<a login with a public profile>` shows `WAKING THE API`, then
  the profile. A private or unknown login shows the not-found panel, and the
  two are indistinguishable.
- The browser console shows no blocked request. A page that never loads
  while `/healthz` answers means `TOKENHOLIC_WEB_ORIGIN` does not match the
  address bar.

This is the first time the site reads a real API; until now it has only run
against a stub.

### 4. Two accounts, two machines

Needs two GitHub accounts, A and B, on two machines, ideally two different
operating systems. Use a release candidate built with
`make release VERSION=0.2.0-rc.1`, so both run exactly what will ship.

On each machine first:

```sh
tokenholic doctor
```

Expect `API address: built in; no setting needed`. If it says anything else,
`TOKENHOLIC_API_URL` is set in that shell; unset it, because this run must
prove a new user needs no configuration.

| # | Who | Do | Expect |
| --- | --- | --- | --- |
| 1 | A | `tokenholic` on a machine that has never run it | The welcome screen with local totals. Nothing uploaded yet |
| 2 | A | Press `c`, confirm, approve the code at GitHub | **The in-TUI claim against real GitHub and the real API, for the first time.** The code screen, then `Signed in as A`, an upload, and the BOARDS view |
| 3 | A | `tokenholic sync` twice | The second run uploads nothing |
| 4 | A | In BOARDS press `c` and create a group; press `y` to copy the invite | A board with A alone, ranked 1 |
| 5 | B | `tokenholic claim` in a terminal, then `tokenholic group join <code>` | Signed in as B, joined, an upload summary |
| 6 | both | `tokenholic`, BOARDS | Both rows, each marked `★ YOU` on its own machine, ranks by `$ EST` and by tokens, the same totals on both screens |
| 7 | A | Use a coding agent for a few minutes, `tokenholic sync`, press `r` on B | A's total rises on B's screen by what A's HOME shows, once |
| 8 | A | `tokenholic autosync enable`, then `tokenholic autosync status` | Installed and loaded. Leave it an hour or run step 7 again without a manual sync |
| 9 | A | `tokenholic profile public`, `tokenholic group public <name>` | The profile and board pages on the website now show A. B, still private, is absent from the public board |
| 10 | A | `tokenholic share` | A draft and a `Card:` address. Opening it lands on A's profile page; adding `/card.png` shows the card |
| 11 | A | Paste the `Card:` address into a link-preview checker, with the API awake | **The live card check.** A large image card with A's totals |
| 12 | B | `tokenholic share` | A text-only draft and a one-line reason: B's profile is private |
| 13 | A | `tokenholic profile private` | The profile page and the share page go back to not found. A card a cache already holds can linger up to five minutes |
| 14 | B | `tokenholic logout`, then `tokenholic` | B's device is revoked; BOARDS offers to claim again. A's board no longer counts that device |
| 15 | B | `tokenholic account delete --confirm DELETE` after signing in again | B disappears from A's board. B's local totals are untouched |

Also check one upgrade: on a machine with `@tokenholic/cli@0.1`, `tokenholic
sync` must say the version is too old and to upgrade, and must keep local
usage.

### 5. Windows

Run the [Windows section](#windows) below once. It is the acceptance step the
Windows work could not do for itself.

### 6. Tag

Only after steps 1 to 5:

```sh
git tag v0.2.0
git push origin v0.2.0
```

The release workflow builds every archive, runs the Linux and Windows ones on
their own runners, creates the GitHub release, and publishes the npm
packages. It needs the `NPM_TOKEN` secret. Add the notes from
[release artifacts](./releasing.md) to the generated release text by hand.

### 7. After publishing

On each of macOS, Linux, and Windows:

```sh
npx --yes @tokenholic/cli@0.2.0 --version
npx --yes @tokenholic/cli@0.2.0 doctor
```

Expect `0.2.0`, the right platform, and `API address: built in`. Then run
step 4's rows 1 and 2 once more from `npx`, with no checkout and no variable
set: that is the claim the landing page promises.

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

Once the release is published, in PowerShell:

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
