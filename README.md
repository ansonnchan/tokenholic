# tokenholic

tokenholic is a terminal-first application for understanding coding-agent usage.
The current local implementation collects supported Claude and Codex evidence,
normalizes token and model metadata, persists privacy-safe aggregates, reports
installation health, and provides a keyboard-first application shell without
reading ordinary chat data.

The hosted PostgreSQL data model, HTTP API, GitHub device login, idempotent
absolute-bucket synchronization, and account-level multi-device aggregation
complete the accounts/devices milestone. Private groups and authenticated
multiplayer leaderboards are implemented in the API and TUI. Local mode still
works without a server. The final social-loop operation permanently deletes
hosted account data without touching local usage.

## Privacy boundary

tokenholic is designed to count usage, not inspect work. Local collection must never upload or retain prompts, responses, source code, command text, credentials, identifying paths, project names, repository metadata, branch names, or raw transcripts.

The compatibility suite uses synthetic fixtures. Do not replace them with records copied from a real local agent installation. See [fixtures/README.md](./fixtures/README.md) before adding a source-format regression case.

## Current capabilities

The native Go command currently provides:

- read-only discovery for Claude Code CLI;
- read-only discovery for Claude Desktop Code, Agent, and Cowork sessions;
- read-only discovery for Codex CLI and Codex Desktop/shared local storage;
- native parsing of supported Claude and Codex JSONL evidence;
- CI validation of the pinned ccusage compatibility contract;
- deterministic per-model normalization, cumulative-counter reduction, recovery from partial records, and deduplication;
- transactional local SQLite observations, checkpoints, health, and absolute usage buckets;
- timezone-aware local totals with explicit complete, partial, unavailable, and empty states;
- a keyboard-first Overview, Sources, Models, Settings, and private Groups TUI with manual collection;
- GitHub device-flow `claim`/`login` and revoking `logout` commands backed by the OS credential vault;
- bounded `sync` and offline exact-payload `show-data`/`sync --dry-run` commands with durable acknowledgements;
- opt-in hourly autosync on macOS (launchd) and Linux (a systemd user timer) with a stable binary, overlap lock, private logs, status, and complete removal;
- one authenticated account usage row across active devices with independent revocation and freshness;
- private groups with owner/member roles, expiring limited-use invites, and membership-authorized reads;
- timezone-aware group leaderboards for total tokens and API-equivalent estimates, with cost coverage, rank trends, and freshness;
- explicit authorization-checked deletion of the hosted account and uploaded aggregates; and
- privacy-safe source and storage diagnostics through `tokenholic doctor`.

Model names are not allowlisted. A previously unseen, non-empty observed identifier can retain its token counts while canonical resolution and pricing remain unavailable. Reported, resolved, inferred, and unknown attribution are separate states.

## Requirements

- Go 1.25 or newer.
- macOS or Linux for the currently implemented discovery paths. On Linux, signing in needs a Secret Service keyring and autosync needs a systemd user session; local use needs neither.
- PostgreSQL 18 only when exercising the hosted schema integration tests.
- ccusage 20.0.51 only when manually checking the optional compatibility probe; native collection does not require it.

The pnpm workspace owns npm distribution tooling; it is not needed to build the native command from source. Checksummed macOS and Linux archives, each for arm64 and x64, are produced by the release workflow.

Tagged releases publish the same native binaries through `npx @tokenholic/cli`.
The launcher installs only the optional package matching the current system:
macOS or Linux, on arm64 or x64. Other platforms fail with a supported-target
message.

## Quickstart

```sh
npx @tokenholic/cli        # see your usage locally — no login required
```

That's it. The command opens the native dashboard using supported usage records
already on this machine. Nothing is uploaded and no account is created. The first
run may ask npm for permission to download the launcher and matching native
binary.

Install it globally when you want the shorter command:

```sh
npm install --global @tokenholic/cli

tokenholic                 # local dashboard + option to claim
tokenholic show-data       # exact payload preview; nothing uploaded
tokenholic claim           # GitHub sign-in followed by the initial sync
tokenholic sync            # collect and upload now
tokenholic autosync enable # explicitly enable hourly background sync
```

`show-data` is the friendly alias for `sync --dry-run`; both work without a
login or API configuration. `claim` is the onboarding form of `login` and runs
the first sync after GitHub authorization. Existing `login` and `sync --dry-run`
commands remain supported.

Claiming and synchronization require a tokenholic API origin. To use the free
hosted demo, export its origin once in the current shell:

```sh
export TOKENHOLIC_API_URL='https://tokenholic-api.onrender.com'

tokenholic claim
tokenholic sync
tokenholic group create "Demo group"
tokenholic
```

The free demo API may take about a minute to wake after inactivity. Hourly
synchronization is opt-in and is never silently installed by `sync`.

The complete command surface remains available after global installation:

```sh
tokenholic login
tokenholic logout
tokenholic show-data
tokenholic sync --dry-run
tokenholic sync
tokenholic autosync enable
tokenholic autosync status
tokenholic autosync disable
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
tokenholic share
tokenholic share --period week
tokenholic account delete --confirm DELETE
```

`tokenholic share` drafts a post about your own local totals and prints an
`x.com/intent/post` link. It posts nothing: the link opens X's compose page,
where you review and send it. When your profile is public the post also
carries your share page, which unfurls with a card.

Login requests public GitHub identity only. The CLI never receives a GitHub
access token, and the API uses it only in memory to resolve the stable numeric
GitHub identity before discarding it.

The API can be run locally or deployed as an optional free demo. The documented
Render and Supabase configuration is intentionally a personal test environment,
not a production availability claim. See the [deployment guide](./docs/deployment.md).

## Build and verify

Run the complete local verification suite from a clean checkout:

```sh
make check
```

That command checks formatting, runs `go vet`, executes the race-enabled test suite, builds every package, and checks the Git diff for whitespace errors.

Build a local executable:

```sh
go build -o ./bin/tokenholic ./cmd/tokenholic
./bin/tokenholic
```

The first launch scans local usage and shows what it found; nothing leaves
the machine. Press `enter` for the dashboard, `r` to scan again, `1`–`5` to
switch views, `p` to change the period, `d` for exact token categories, `s`
to draft a post of your totals, `?` for help, and `q` to quit. With
`TOKENHOLIC_API_URL` set, `c` claims an account from inside the TUI. The
minimum supported width is 48 columns.

The TUI is in color by default and picks its light or dark palette from the
terminal's background. Three environment variables adjust that:

| Variable | Effect |
| --- | --- |
| `TOKENHOLIC_THEME=light` or `dark` | Use that palette when the terminal's background is detected wrongly |
| `NO_COLOR` (any value) | Disable all styling; selection and status use brackets and words |
| `TOKENHOLIC_ASCII` (any value) | Replace box-drawing and symbol characters with ASCII |

Run the source diagnostic:

```sh
./bin/tokenholic doctor
```

`doctor` reports source types, health, candidate-file availability,
capabilities, parser compatibility, and privacy-safe local database health. It
intentionally does not print local source or database paths, parse session
contents, or expose persisted observations.

## Repository guide

| Path | Responsibility |
| --- | --- |
| `cmd/tokenholic` | Native executable entry point |
| `cmd/pricegen` | Generator that vendors the pinned, checksum-verified price snapshot |
| `internal/adapters` | Source discovery, source-specific parsers, and the ccusage compatibility contract |
| `internal/normalize` | Provider-neutral observations, capabilities, and invariants |
| `internal/collect` | Parsing orchestration, deduplication, and cumulative reduction |
| `internal/application` | Local refresh sequencing and committed dashboard queries |
| `internal/account` | Device login, rotation, revocation, account deletion, and OS credential-vault storage |
| `internal/doctor` | Privacy-safe source and storage diagnostic reporting |
| `internal/fixturecheck` | Automated fixture privacy validation |
| `internal/modelcatalog` | Vendored price snapshot, model resolution, and API-equivalent estimates |
| `internal/store` | Local SQLite lifecycle, observations, aggregates, and diagnostics |
| `internal/tui` | Bubble Tea application shell and local presentation boundary |
| `internal/sync` | Privacy-allowlisted protocol, bounded batching, retry, and dry-run serialization |
| `fixtures` | Synthetic compatibility and regression data |
| `server/postgres` | Hosted PostgreSQL connection and migration boundary |
| `server/account`, `server/auth`, `server/sync`, and `server/httpapi` | Account aggregation/deletion, device authorization, idempotent upload, and bounded JSON API |
| `deploy/render` and `render.yaml` | Optional free demo API container and Render Blueprint |
| `migrations` | Immutable, embedded hosted PostgreSQL schema history |
| `docs` | Cross-cutting architecture and design decisions |

Package-level navigation and ownership are documented in [internal/README.md](./internal/README.md). Cross-cutting documents are indexed in [docs/README.md](./docs/README.md).

## Design principles

- Keep discovery, parsing, normalization, collection, persistence, synchronization, and presentation separate.
- Keep observed model identifiers distinct from canonical model and pricing identifiers.
- Treat unavailable metadata as unavailable, never as zero.
- Make repeated collection deterministic and safe.
- Keep provider-specific rules inside adapters.
- Validate external tool output as a pinned, versioned contract.
- Normalize and commit local state before any network upload attempt.

## Documentation

- [Documentation index](./docs/README.md)
- [Development guide](./docs/development.md)
- [Free demo deployment](./docs/deployment.md)
- [Roadmap](./ROADMAP.md)
- [Architecture](./docs/architecture.md)
- [TUI and website design direction](./docs/design.md)
- [Contributor and agent guidance](./AGENTS.md)

## Contributing

Read [AGENTS.md](./AGENTS.md), the roadmap, architecture, and design documents before changing the project. Keep fixtures synthetic, use conventional commits, run `make check`, and leave pull requests open for review unless a specific merge is explicitly requested.
