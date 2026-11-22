# Command reference

Everything the `tokenholic` command does. `npx @tokenholic/cli <command>` runs
the same commands without a global install.

## Local

```sh
tokenholic                 # the terminal interface
tokenholic demo            # the interface on invented data, for showing it
tokenholic doctor          # health of each source and of local storage
tokenholic show-data       # the exact upload payload; nothing is sent
tokenholic sync --dry-run  # the same as show-data
tokenholic share           # draft a post of your own totals
tokenholic share --period week
tokenholic --version
```

`doctor` reports source types, health, candidate-file availability,
capabilities, and local database health. It does not print local source or
database paths, parse session contents, or show stored records.

`show-data` works without a login, an account, or an API address.

`tokenholic demo` opens the same interface on invented usage and boards, with
`DEMO: invented data` in its header. It reads no agent records, opens no
database, signs nobody in, and contacts no server, so it is safe to show or
record on any machine. Every key works; creating or joining a group and
inviting are declined in a line, since there is no account behind it, and the
look you switch to is not saved.

`tokenholic share` drafts a post about your own local totals and prints an
`x.com/intent/post` link. It posts nothing: the link opens X's compose page,
where you review and send it. When your profile is public the post also
carries your share page, which unfurls with a card.

## Account and sync

```sh
tokenholic claim           # GitHub sign-in followed by the first upload
tokenholic login           # GitHub sign-in only
tokenholic logout
tokenholic sync            # collect and upload now
tokenholic autosync enable # hourly background sync; never installed silently
tokenholic autosync status
tokenholic autosync disable
tokenholic account delete --confirm DELETE
```

After its first upload, `claim` asks whether to turn on hourly sync, here and
in the terminal interface. Enter means yes; anything else leaves it off, and
`tokenholic autosync enable` turns it on later. Nothing is asked, and nothing
is installed, when it is already on, when this system has no scheduler
tokenholic can use, or when no terminal is attached.

Login requests public GitHub identity only. The command never receives a
GitHub access token, and the API uses it only in memory to resolve the stable
numeric GitHub identity before discarding it.

Claiming and synchronization talk to the hosted tokenholic API. A released
build already knows its address, so there is nothing to configure.
`TOKENHOLIC_API_URL` exists only to point the client somewhere else: at your
own deployment, or at a local API during development. A build made from source
with `go build` carries no address and does need it; `tokenholic doctor` says
which case applies. The free demo API may take about a minute to wake after
inactivity.

A first `sync` of a long history uploads many batches and can take several
minutes on the free API. It prints how far it has got, and it has no time
limit of its own: it runs for as long as batches keep being accepted.
Stopping it, or losing the connection, is safe. What was uploaded is kept,
the command says how many buckets that was, and the next `tokenholic sync`
sends only the rest.

`account delete` permanently deletes the hosted account and its uploaded
totals. Local usage is not touched.

## Groups, the global board, and your profile

```sh
tokenholic group list
tokenholic group create "Quiet team"
tokenholic group invite --expires 24h --uses 10 "Quiet team"
tokenholic group join "$TOKENHOLIC_INVITE_CODE"
tokenholic group leave "Quiet team"
tokenholic group public "Quiet team"
tokenholic group private "Quiet team"
tokenholic group transfer "Quiet team" new-owner-login
tokenholic group delete --confirm "Quiet team" "Quiet team"
tokenholic global join
tokenholic global leave
tokenholic profile
tokenholic profile public
tokenholic profile private
```

Groups, profiles, and global-board participation are private until you turn
them on.

## Agents read through ccusage

```sh
tokenholic ccusage status
tokenholic ccusage off
tokenholic ccusage on
```

`tokenholic ccusage` controls the one third-party program tokenholic runs:
ccusage, at a pinned and digest-checked release, for agents tokenholic has no
reader of its own for (GitHub Copilot CLI, Gemini CLI, Grok Build CLI,
OpenCode, and Qwen Code). It is on by default, comes with the npm package, and
reads those agents' local session records on this machine. `off` stops
tokenholic from running it; the choice is saved. Claude and Codex are never
read through it. The [runner guide](../internal/adapters/ccusage/README.md)
lists exactly what it reads and how it is confined.

## In the terminal interface

The first launch scans local usage and shows what it found; nothing leaves the
machine.

| Key | Does |
| --- | --- |
| `enter` | Open the dashboard from the first-launch screen |
| `1`–`5` | Switch views: HOME, BOARDS, MODELS, SOURCES, SETTINGS |
| `p` | Change the period |
| `d` | Show exact token categories |
| `r` | Scan again |
| `s` | Draft a post of your totals |
| `c` | Claim an account |
| `t` | Switch between the light and dark look; the choice is remembered |
| `?` | Help |
| `q` | Quit |

The minimum supported width is 48 columns. The interface is in color and light
by default, on its own background, so it looks the same in every terminal.

Nobody has to set a variable to use tokenholic. Three optional ones adjust
display:

| Variable | Effect |
| --- | --- |
| `TOKENHOLIC_THEME=light` or `dark` | Start in that look regardless of the saved choice |
| `NO_COLOR` (any value) | Disable all styling; selection and status use brackets and words |
| `TOKENHOLIC_ASCII` (any value) | Replace box-drawing and symbol characters with ASCII |

## Requirements

- Node.js 20 or newer for the npm package. The package installs a native
  program for macOS, Linux, or Windows, on arm64 or x64, and the pinned
  ccusage beside it. Other platforms fail with a supported-target message.
- On Linux, signing in needs a Secret Service keyring and autosync needs a
  systemd user session; local use needs neither. Windows support is verified
  in CI and has not yet had its manual run on a real machine.
- Building from source needs Go 1.25 or newer; see the
  [development guide](./development.md). A build from source has no ccusage,
  and `tokenholic doctor` says so.
