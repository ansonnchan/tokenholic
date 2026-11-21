# tokenholic roadmap

Status: MVP complete; product-parity revamp in progress (M6 complete, M7 in progress, M8–M11 planned)
Last updated: November 21, 2026

## Product definition

tokenholic is a terminal-native, multiplayer leaderboard for coding-agent usage. It reads locally available usage records, normalizes model and token metadata, and synchronizes privacy-limited aggregates to private groups and an opt-in global board.

The terminal is the primary product. A small read-only website exists for discovery and sharing: a landing page, opt-in public profiles and boards, and share cards that unfurl on X. Collection, account management, groups, and every write stay in the CLI/TUI.

The product tracks coding-agent activity only. Ordinary Claude Desktop chat, claude.ai, general chat applications, prompts, responses, source code, file paths, and project names are outside the product.

### Product decisions (November 2026)

These decisions drive milestones M6–M10 and supersede earlier MVP exclusions
where they conflict:

- **Terminal first, small web second.** The TUI is the differentiator. The
  website is read-only, has no sign-in, and exists so a shared link can
  unfurl and a board can be viewed without installing anything.
- **Two headline metrics.** Leaderboards rank by total tokens or by the
  API-equivalent cost estimate. Input, output, cache, and reasoning
  categories remain visible in personal views but are not ranking metrics.
- **The server owns model resolution and pricing.** Clients upload observed
  model IDs and token counts. The client resolver is for offline preview
  only, so catalog updates never break older clients.
- **Opt-in visibility.** Profiles, groups, and global-board participation are
  private by default. GitHub login and avatar are visible to fellow group
  members; they appear publicly only after an explicit opt-in.
- **No anonymous uploads.** Usage appears locally before sign-in; uploading
  always requires a claimed GitHub account.
- **Close to the supplied retro-terminal reference look** in both the TUI and
  the website, with tokenholic's own name, wordmark, and copy.
- **macOS, Linux, and Windows** are all target platforms.
- **Hosting stays on the free Render API plus Supabase PostgreSQL**, with
  the static website on GitHub Pages. Render cold starts are an accepted
  demo limitation.

### MVP user journey

```text
npx @tokenholic/cli
  -> detect local Claude Code and Codex usage
  -> show personal totals in a TUI
  -> authenticate with GitHub device flow
  -> create or join a private group
  -> synchronize usage
  -> view an account-level leaderboard across all devices
  -> optionally enable hourly autosync
```

### MVP commands

```text
tokenholic                         # Open the TUI
tokenholic claim                   # GitHub authentication followed by initial sync
tokenholic login                   # GitHub device authentication
tokenholic logout
tokenholic sync                    # Collect locally and upload pending aggregates
tokenholic show-data               # Show the exact upload payload without an account
tokenholic sync --dry-run          # Show what would be uploaded
tokenholic group create <name>
tokenholic group join <code>
tokenholic group leave <name>
tokenholic autosync enable         # Install an hourly OS scheduler
tokenholic autosync disable
tokenholic autosync status
tokenholic doctor                  # Diagnose sources, auth, storage, and sync
```

### Planned commands (M7–M9)

```text
tokenholic global join             # Opt in to the global board
tokenholic global leave
tokenholic profile                 # Show profile visibility and links
tokenholic profile public|private  # Set public profile visibility
tokenholic group public|private <name>   # Owner sets public board visibility
tokenholic group delete <name>     # Owner deletes a group
tokenholic group transfer <name> <login> # Owner transfers ownership
tokenholic share                   # Print an x.com/intent/post share link
```

## Scope decisions

### Supported in the MVP

- macOS on Apple Silicon and Intel.
- Claude Code CLI sessions.
- Claude Desktop local Code/Agent/Cowork sessions when compatible local usage records are present.
- Codex CLI and Codex Desktop sessions.
- Exact observed model IDs where the source reports them.
- Explicit `reported`, `resolved`, `inferred`, and `unknown` model-resolution states.
- Input, output, cache-read, and cache-write categories when the source exposes them.
- GitHub account identity with multiple independently revocable devices.
- Private invite-only groups.
- Today, current week, last 30 days, and all-time leaderboards.
- Total tokens, output tokens, and API-equivalent cost views.
- Optional hourly background synchronization.

### Added by the parity revamp (M6–M10)

- Five priced token categories: uncached input, cache read, 5-minute cache
  write, 1-hour cache write, and output. Reasoning stays a subset of output.
- Ranking by total tokens and API-equivalent estimate only.
- Rank and value trends against the previous period at the same elapsed time.
- Opt-in global board with a minimum population before ranks are shown.
- Opt-in public profiles and public groups, invite links, group deletion, and
  ownership transfer.
- A static read-only website on GitHub Pages and share cards rendered by the
  API.
- Linux and Windows builds, discovery paths, and autosync schedulers.
- API rate limiting and an advisory plausibility flag.

### Explicitly excluded

- Ordinary Claude Desktop chat, claude.ai, ChatGPT chat, or mobile chat usage.
- Cursor until reliable local token and model evidence is validated.
- Anonymous or unclaimed uploads.
- Web sign-in, web writes, or a full browser dashboard that duplicates the TUI.
- Calls to the paid X API; sharing uses client-side `x.com/intent/post` links.
- Work-email or company boards.
- Writing-versus-reading-versus-discussion classification.
- Productivity scoring, code-quality scoring, or claims that more tokens mean better work.
- Real-time WebSockets, Redis, queues, or horizontal scaling.
- Cryptographic proof that client-reported usage is genuine.

## Model and source strategy

Models and sources are different extension points:

- A **source adapter** knows where a coding client stores usage and how its records behave.
- A **model resolver** maps the model identifier emitted by a source to a canonical model and pricing entry.

A newly released model should continue to count tokens without a tokenholic release when an existing source reports its identifier. Until pricing metadata is available, tokenholic displays the observed model and token counts with cost marked as unavailable.

A newly supported coding client requires a source adapter or a validated ccusage integration.

### Initial source matrix

| Source | MVP status | Collection path | Notes |
| --- | --- | --- | --- |
| Claude Code CLI | P0 | Claude-compatible local JSONL | Report exact model and token categories when present |
| Claude Desktop Code/Agent/Cowork | P0 | Desktop local-agent session discovery plus Claude-compatible JSONL | Keep the desktop surface distinct from CLI; deduplicate shared records |
| Codex CLI | P0 | Codex rollout JSONL through a pinned parser | Respect cumulative counters and active `turn_context` model |
| Codex Desktop | P0 | Codex local session/rollout storage | Verify whether it shares or duplicates CLI roots |
| Gemini CLI | M11 | Pinned ccusage | Day-level rows; no cache-write lifetimes |
| OpenCode | M11 | Pinned ccusage | Day-level rows; no cache-write lifetimes |
| GitHub Copilot CLI | M11 | Pinned ccusage | Day-level rows; no cache-write lifetimes |
| Grok Build CLI | M11 | Pinned ccusage | Do not claim generic Grok chat support |
| Qwen Code | M11 | Pinned ccusage | Day-level rows; no cache-write lifetimes |
| Cursor | Research | Unknown | Ship only if model and token evidence is reliable |

## Target architecture

```text
Local coding-agent records
        |
        v
Source discovery and adapters
        |
        v
Normalized observations + model resolution
        |
        v
Local SQLite
  - source checkpoints
  - normalized observations
  - aggregate buckets
  - sync outbox
  - adapter health
        |
        +------------------> Bubble Tea TUI
        |
        v
Idempotent HTTPS sync
        |
        v
Go API + PostgreSQL (Render + Supabase)
  - users, devices, visibility and global opt-in
  - groups, memberships, invites
  - usage buckets
  - server-owned model resolution and pricing revisions
        |
        +------------------> TUI group and global boards
        |
        +------------------> Public read-only JSON (CORS, opt-in data only)
        |                          |
        |                          v
        |                    Static website (GitHub Pages)
        |
        +------------------> Share pages and PNG cards for X unfurls
```

### Local technology

- Go CLI and collector.
- Bubble Tea and Lip Gloss for the TUI.
- SQLite in WAL mode for checkpoints, observations, buckets, and the outbox.
- Pinned ccusage manifest and fixtures preserve the reviewed external contract.
- Native adapters provide the implemented Claude and Codex collection paths.

### Server technology

- Go HTTP API.
- PostgreSQL for relational group and device state.
- Optional free Render and Supabase demo deployment for end-to-end validation.
- One API deployment and one database for the demo; no load balancer or distributed cache unless measurements justify them.
- The same Go binary renders share pages and PNG share cards.

### Website technology

- Static HTML, CSS, and minimal JavaScript in `web/`, without a framework or
  build step, deployed to GitHub Pages by GitHub Actions.
- Pages read only public JSON endpoints from the API and show an explicit
  loading state while a sleeping free-tier API wakes.

### Distribution

- `@tokenholic/cli` is the umbrella npm package and exposes the `tokenholic` executable.
- Platform-specific packages contain native Go binaries for supported OS/CPU combinations.
- A small launcher chooses the installed platform binary; users still run one command.
- GitHub Actions or GoReleaser builds, checksums, signs, and publishes releases.
- Homebrew is a post-MVP distribution channel.

## Data rules

### Normalized observation

The internal record must retain observed facts separately from resolved metadata:

```text
source
surface
source_version
source_record_id
session_id_local
occurred_at
observed_model_id
canonical_model_id
model_provider
model_resolution: reported | resolved | inferred | unknown
input_uncached_tokens
input_cache_read_tokens
input_cache_write_tokens
input_cache_write_5m_tokens
input_cache_write_1h_tokens
output_tokens
reasoning_output_tokens
service_tier: standard | priority | flex | batch | fast (absent when unproven)
prompt_tokens (one request's whole prompt; absent when unproven)
count_semantics: incremental | cumulative_snapshot
measurement: reported | derived | estimated
adapter_version
normalization_version
warnings
```

Unknown values remain null rather than being converted to zero. Reasoning tokens that are a subset of output tokens must never be added twice.

Cache-write totals keep their 5-minute and 1-hour split when the source
reports it. When only the combined value exists, its time-to-live is unknown:
the estimate prices it at the 5-minute rate and marks the result partial.

Lines that carry no real model usage, such as Claude `<synthetic>` and API
error records, are excluded before normalization.

### Cost semantics

- Every cost is labelled an API-equivalent estimate, never spending.
- Total tokens are uncached input + cache read + cache write + output.
  Reasoning is already inside output.
- Unpriced tokens still count toward the token board. Cost displays as a
  lower bound with the unpriced share visible.
- A user is ranked on cost when at least 95% of their tokens in the window are
  priced; otherwise the row shows its lower bound and coverage without a rank.
- Service tier and prompt length select among a model's prices, from evidence
  the source records for each request. A proven tier or length with no
  vendored rate stays unpriced, and so does usage of a prompt-length-priced
  model that cannot prove which side of the threshold it fell on.
- A service tier the source did not prove is priced at the standard rate
  (decided November 21, 2026). Codex records the tier only from a thread's
  second turn, so the stricter rule left every first turn unpriced. This is
  the one place an estimate assumes a billing fact.
- When the server's pricing revision changes, it reprices every stored bucket
  from raw token counts in one transaction so history stays consistent.

### Synchronization

- Normalize and persist locally before attempting network upload.
- Convert observations into absolute, versioned UTC time buckets.
- Retry uploads at least once with exponential backoff and jitter.
- A repeated bucket revision is a no-op on the server.
- A newer revision replaces the older absolute bucket instead of adding a delta.
- Leaderboard rows aggregate all active devices belonging to the user account.
- Group membership belongs to the user, never to an individual device.
- Every device has a separate revocable credential.
- From protocol v2, clients send observed model IDs and token counts only.
  The server resolves models, derives prices, and rejects v1 uploads with an
  upgrade instruction.

### Privacy boundary

The default upload payload may include:

- user and device identifiers;
- UTC time bucket;
- source and surface;
- the observed model identifier and how the source attributed it;
- the service tier and a coarse prompt-size band, when the source proves them;
- normalized token categories;
- schema and adapter versions.

It must not include:

- prompts or responses;
- source code or diffs;
- tool inputs or command text;
- file paths, project names, repository names, or branch names;
- provider credentials;
- raw session transcripts.

`tokenholic show-data` and its `sync --dry-run` alias must use the real
serialization path and show exactly what would be uploaded without requiring an
API origin, account, or device credential.

Public surfaces (the website, public JSON, and share cards) may expose only
data a user or group owner explicitly made public: GitHub login, display name,
avatar, aggregate totals, ranks, trends, and model or source mix. A private
user or group must be indistinguishable from a nonexistent one on every
public endpoint.

## Milestones and PR-sized workstreams

The identifiers below exist only for internal roadmap cross-references. Do not copy them into branch names, PR titles, commit subjects, or trailers. GitHub issues are optional; preserve the relevant acceptance criteria and dependencies in each focused PR description.

### M0 — Feasibility and foundations (complete)

Goal: prove that the required local evidence exists and freeze the internal contract before building cloud infrastructure.

- [x] **TKH-001: Initialize the Go workspace and repository layout**
  - Create CLI, internal packages, API placeholder, migrations, fixtures, and documentation directories.
  - Add formatting, linting, unit-test, and race-test commands.
  - Acceptance: a clean checkout builds and tests with one documented command.

- [x] **TKH-002: Define normalized observation and capability contracts**
  - Represent count semantics, model-resolution quality, nullable categories, adapter health, and warnings.
  - Acceptance: schema tests cover reported, inferred, unknown, cumulative, and incremental records.
  - Depends on: TKH-001.

- [x] **TKH-003: Create sanitized compatibility fixtures**
  - Capture representative Claude Code CLI, Claude Desktop local-agent, Codex CLI, and Codex Desktop records.
  - Remove prompts, responses, paths, identities, repository metadata, and credentials.
  - Acceptance: fixtures preserve token/model/counter behavior without private content.
  - Depends on: TKH-001.

- [x] **TKH-004: Spike pinned ccusage execution and JSON validation**
  - Resolve and execute a pinned ccusage binary.
  - Reject unsupported versions and malformed output safely.
  - Determine which required per-model and per-period fields are available.
  - Acceptance: Claude Code and Codex fixtures produce validated structured output in tests.
  - Depends on: TKH-002, TKH-003.

- [x] **TKH-005: Discover Claude Desktop local coding sessions**
  - Implement macOS path discovery for local Code/Agent/Cowork sessions.
  - Determine whether ccusage can parse explicit discovered roots or a small Go adapter is required.
  - Acceptance: a desktop fixture is discovered, classified as a desktop surface, and parsed without reading ordinary chat data.
  - Depends on: TKH-003, TKH-004.

- [x] **TKH-006: Verify cumulative and duplicate semantics**
  - Test rescans, partially written JSONL, cumulative Codex counters, repeated Claude request rows, and desktop/CLI overlap.
  - Acceptance: rescanning unchanged fixtures never increases normalized totals.
  - Depends on: TKH-004, TKH-005.

M0 exit criteria:

- Claude CLI, Claude Desktop coding sessions, and Codex yield attributable model/token records from sanitized fixtures.
- Unsupported or ambiguous records fail visibly instead of becoming zeroes or guessed models.
- Repeat parsing is deterministic and duplicate-safe.
- The normalized schema is stable enough to persist locally.

### M1 — Local native MVP

Goal: deliver a useful local application before authentication or multiplayer work.

- [x] **TKH-010: Implement local SQLite storage and migrations**
  - Add checkpoints, observations, buckets, outbox, and adapter-health tables.
  - Use transactions and WAL mode.
  - Acceptance: interruption and restart tests preserve committed observations and checkpoints.
  - Depends on: M0.

- [x] **TKH-011: Implement source collection orchestration**
  - Detect sources, collect incrementally, normalize, persist, and report adapter warnings.
  - Acceptance: one scan combines CLI and desktop sources without duplicates.
  - Depends on: TKH-010.

- [x] **TKH-012: Implement model resolution and pricing registry**
  - Store observed, canonical, provider, pricing key, quality, source, and revision separately.
  - Unknown models retain tokens while cost remains unavailable.
  - Acceptance: reported, aliased, routed, and unknown fixtures display correctly.
  - Depends on: TKH-002, TKH-010.

- [x] **TKH-013: Build the initial Bubble Tea TUI**
  - Overview, sources/health, models, recent periods, and settings/help views.
  - Support narrow terminals and graceful no-color output.
  - Acceptance: a new user can identify detected sources, totals, model mix, and collection warnings without reading documentation.
  - Depends on: TKH-011, TKH-012.

- [x] **TKH-014: Implement `tokenholic doctor`**
  - Report platform, source paths without sensitive details, parser version, database health, and collection errors.
  - Acceptance: missing sources, permissions, unsupported formats, and corrupt local state have actionable diagnostics.
  - Depends on: TKH-011.

M1 exit criteria:

- The native app is useful with no server configured.
- It shows model and token totals from Claude and Codex coding sessions.
- Closing or interrupting the app does not corrupt local state.
- Repeated scans do not inflate totals.

### M2 — Accounts, devices, and synchronization (complete)

Goal: establish one account across multiple independently managed devices.

- [x] **TKH-020: Define and migrate the server data model**
  - Users, GitHub identities, devices, device credentials, pricing revisions, and usage buckets.
  - Acceptance: database constraints enforce ownership and bucket idempotency.

- [x] **TKH-021: Implement GitHub device-flow login**
  - Complete CLI-friendly authorization without accepting a GitHub password.
  - Exchange GitHub identity for a tokenholic account and device credential.
  - Acceptance: login, logout, credential refresh/failure, and device revocation are tested.
  - Depends on: TKH-020.

- [x] **TKH-022: Implement local outbox and sync protocol**
  - Build absolute versioned buckets, bounded batches, retry behavior, acknowledgements, and dry-run serialization.
  - Acceptance: retries, duplicate submissions, out-of-order revisions, and network interruption do not inflate totals.
  - Depends on: TKH-010, TKH-020, TKH-021.

- [x] **TKH-023: Implement account-level multi-device aggregation**
  - Aggregate devices under one user while retaining device revocation and freshness.
  - Acceptance: two devices contribute to one account row; revoking one prevents future writes without deleting the other.
  - Depends on: TKH-022.

M2 exit criteria:

- A user can sign in and synchronize from two devices.
- Retrying the same upload cannot change totals.
- Synced data matches the dry-run preview and contains no prohibited fields.

### M3 — Private groups and leaderboards

Goal: complete the social product loop.

- [x] **TKH-030: Implement groups, memberships, roles, and invites**
  - Private groups only, owner/member roles, expiring invite codes, usage limits, and leave/revoke behavior.
  - Acceptance: authorization tests prevent non-members from reading or mutating a group.

- [x] **TKH-031: Implement period and metric leaderboard queries**
  - Today, current week, last 30 days, and all-time in a group timezone.
  - Total tokens, output tokens, and API-equivalent cost.
  - Acceptance: query tests cover timezone boundaries, multiple devices, unknown prices, and ties.
  - Depends on: TKH-023, TKH-030.

- [x] **TKH-032: Add group and leaderboard views to the TUI**
  - Join/create/switch group, ranking, personal standing, source mix, most-used model, and freshness.
  - Acceptance: two users can join a group, sync independently, and see matching rankings.
  - Depends on: TKH-031.

- [x] **TKH-033: Implement account and uploaded-data deletion**
  - Revoke devices, leave groups, erase synchronized usage, and delete the account.
  - Acceptance: deletion is authorization-checked, complete, and tested.
  - Depends on: TKH-020, TKH-030.

M3 exit criteria:

- The full MVP user journey works on real machines for at least two users.
- Group authorization and deletion behavior have automated tests.
- Every leaderboard value exposes period, metric, freshness, and model-resolution limitations.

### M4 — Packaging, autosync, and deployment

Goal: make tokenholic installable and reliable without developer assistance.

- [x] **TKH-040: Build cross-platform release artifacts**
  - Produce checksummed macOS arm64/x64 binaries and pinned parser assets.
  - Acceptance: artifacts run on clean supported machines.

- [x] **TKH-041: Prepare the npm umbrella and platform packages**
  - Expose one `tokenholic` executable through `npx @tokenholic/cli`.
  - Fail with an actionable message on unsupported platforms.
  - Acceptance: package preparation and launcher tests pass for both macOS architectures.
  - Depends on: TKH-040.

- [x] **TKH-042: Implement macOS autosync**
  - Install, inspect, and remove a user-level `launchd` schedule.
  - Use a stable executable path, a single-instance lock, and local logs without sensitive payloads.
  - Acceptance: hourly sync survives logout/restart, does not overlap manual sync, and is fully removable.
  - Depends on: TKH-022, TKH-040.

- [ ] **TKH-043: Provision the free demo environment**
  - Deploy the Go API on Render and PostgreSQL on Supabase using documented secret inputs.
  - Preserve free-tier limitations honestly; do not claim production backup or availability guarantees.
  - Acceptance: the Blueprint builds, `/healthz` succeeds, embedded migrations apply, and the complete login-to-leaderboard journey is validated manually.
  - Depends on: M3.

M4 exit criteria:

- A tester can install with one command, authenticate, join a group, sync, and enable autosync without repository access.
- Releases are reproducible and versioned.
- The free demo environment can be removed without affecting authoritative local usage.

### M5 — Private beta and hardening (optional, after M10)

Goal: validate reliability with real installations rather than chase scale.

- [ ] **TKH-050: Run a 5-user compatibility pilot**
  - Capture installation, provider detection, sync, and retention failures.
  - Fix blockers before expanding the cohort.

- [ ] **TKH-051: Expand to a 10–20 developer beta**
  - Prefer sustained weekly use over one-time installs.
  - Collect only operational product metrics; do not collect prompts or source metadata.
  - Depends on: TKH-050.

- [ ] **TKH-052: Publish compatibility and privacy documentation**
  - Document supported client versions, coverage limitations, upload fields, deletion, and the self-reported-data caveat.

- [ ] **TKH-053: Measure and harden the critical path**
  - Track parser failures, sync success, duplicate rejections, API latency, and stale devices.
  - Add regression fixtures for every real compatibility bug.

M5 success signals:

- At least 5 weekly active testers across four consecutive weeks.
- At least 2 active private groups.
- Repeat sync and retry incidents do not inflate totals.
- Real compatibility bugs become sanitized regression fixtures.
- Users can install and recover from common failures without direct developer intervention.

### Parity revamp delivery rules

M6–M10 ship as stacked PRs, one focused branch each, in the order below.
Each PR states its parent, keeps `make check` green, and stays open until the
user explicitly merges it. Suggested branch names follow AGENTS.md and never
contain the identifiers used here.

### M6 — Usage and cost correctness

Goal: make token totals and API-equivalent estimates match real local usage
before any presentation work depends on them.

- [x] **TKH-060: Record the product direction** (`docs/product-direction`)
  - Update the roadmap, architecture, design direction, and contributor guidance.
  - Acceptance: documents agree with each other and with the product decisions above.

- [x] **TKH-061: Correct Claude usage semantics** (`fix/claude-usage-semantics`)
  - Exclude `<synthetic>` and `isApiErrorMessage` assistant lines.
  - Read the `cache_creation` object into 5-minute and 1-hour cache-write
    categories; keep the combined value when no split exists.
  - Add local SQLite migration columns and sanitized regression fixtures.
  - Acceptance: fixture totals match hand-computed values; rescans do not
    change totals; combined-only records report an unknown time-to-live.
  - Depends on: TKH-060.

- [x] **TKH-062: Adopt a pinned LiteLLM price snapshot** (`feat/litellm-pricing`)
  - A Go generator vendors a pinned LiteLLM commit verified by SHA-256 and
    keeps only token-priced fields, including the 1-hour cache-write price.
  - A reviewed overrides file adds models newer than the snapshot. Every
    override cites an official provider pricing URL and retrieval date;
    prices are verified at implementation time, never guessed.
  - Reviewed alias rules handle dated snapshot IDs and suffixes such as
    `[1m]`; ambiguous routing aliases stay unknown.
  - Add the MIT notice for the vendored data.
  - Acceptance: every model observed in the maintainer's local logs either
    prices or is explicitly unpriced with a reason; golden cost tests cover
    all five priced categories and unknown models.
  - Depends on: TKH-061.

- [x] **TKH-063: Make sync v2 server-authoritative** (`feat/sync-v2`)
  - Clients upload observed model IDs and the six token categories only.
  - The server resolves models and derives estimates plus priced and
    unpriced token counts.
  - A forward-only migration replaces the demo `usage_buckets` table with the
    v2 shape; the release notes call this a demo-data reset.
  - `/v1/sync/buckets` returns `426 Upgrade Required` with an upgrade message.
  - Acceptance: a client with an older catalog still syncs; repeated sync is a
    no-op; `show-data` matches the request body; v1 is rejected clearly.
  - Depends on: TKH-062.

- [x] **TKH-064: Reprice history on pricing changes** (`feat/server-repricing`)
  - On startup, when the active pricing revision differs from stored rows,
    reprice every bucket in one advisory-locked transaction.
  - Acceptance: integration tests show identical totals after repricing twice
    and correct totals after a revision change.
  - Depends on: TKH-063.

- [x] **TKH-065: Correct Codex cumulative-counter reduction** (`fix/codex-counter-ordering`)
  - Found while validating M6 against real local logs. Collection sorts
    observations by timestamp and then by record hash before reducing
    cumulative snapshots. A resumed Codex rollout replays earlier snapshots
    under one timestamp, so the hash order scrambles them, each apparent
    decrease is treated as a counter reset, and the full running total is
    counted again. On real logs this inflates Codex totals many times over.
  - A forked rollout also replays its parent's whole history, which was
    counted again in every fork.
  - Read per-request usage and identify each request by session, turn, and
    running total so replays deduplicate; reduce legacy snapshots in source
    order; retract buckets that no longer have usage.
  - Stored Codex observations are discarded and collected again.
  - Acceptance: a sanitized fixture with replayed same-timestamp snapshots
    matches hand-computed totals; rescans do not change totals.
  - Depends on: TKH-061.

M6 exit criteria:

- Real local usage produces totals and estimates whose coverage is visible.
- Catalog or price updates never reject an otherwise valid upload.

Two pricing gaps remained after M6 and opened M7 as TKH-074: usage whose price
depends on prompt length or service tier was not yet priced from proven
evidence.

### M7 — Leaderboards, profiles, and integrity

Goal: reach social-product parity with honest ranking rules.

- [x] **TKH-074: Carry pricing-tier evidence** (`feat/pricing-tier-evidence`)
  - Ships first in M7, before the protocol is released, because cost ranking
    depends on it and it changes the upload shape.
  - Two prices depend on evidence a usage bucket does not carry today:
    - Prompt length. Models such as the current default Codex model charge
      more above a prompt-length threshold, so they resolve but stay unpriced.
      Local records carry each request's prompt size.
    - Service tier. Codex records the tier in `thread_settings_applied`
      (`priority` or legacy `fast`, `flex`, and `default` or `standard`), and a
      settings event without the key keeps the previous tier. Fast usage is
      billed above the standard rate, and Codex estimates currently ignore it.
      Claude records carry `service_tier` and `speed` in the same way.
  - Carry both as evidence from the adapter through the normalized record,
    bucket identity, sync protocol, and a server column. Buckets without the
    evidence must serialize exactly as they do now.
  - Vendor the per-tier rates the price snapshot publishes. Usage in a tier
    with no vendored rate stays unpriced with a reason.
  - Decided: an unknown service tier is priced at the standard rate. Codex
    writes no settings event before a thread's first turn, so the stricter
    rule left about a tenth of the maintainer's Codex usage unpriced. An
    unproven prompt length still stays unpriced. The Codex configuration file
    is never read to guess.
  - Acceptance: golden cost tests cover each tier and threshold side; a
    bucket without tier evidence keeps its key and content hash; a fixture
    proves a settings event without a tier keeps the previous one.
  - Depends on: TKH-064.

- [ ] **TKH-070: Leaderboard v2** (`feat/leaderboard-v2`)
  - Metrics: total tokens and API-equivalent estimate.
  - Cost ranking requires at least 95% priced tokens; otherwise show the
    lower bound and coverage without a rank.
  - Trends compare rank and value with the previous period truncated to the
    same elapsed time. All-time compares with 24 hours ago and says so.
  - Add an advisory plausibility flag for implausible daily totals.
  - Acceptance: SQL tests cover non-UTC boundaries, ties, coverage, trends,
    and first-period entrants.
  - Depends on: TKH-074.

- [ ] **TKH-071: Rate-limit the API** (`feat/api-rate-limits`)
  - In-memory token buckets per credential and per IP for the single instance.
  - Acceptance: handler tests return 429 with `Retry-After`; limits are documented.
  - Depends on: TKH-070.

- [ ] **TKH-072: Add the opt-in global board** (`feat/global-board`)
  - `users.global_opt_in` defaults to false; `tokenholic global join|leave`.
  - Hide ranks when a scope has fewer than three ranked users.
  - Acceptance: a user who has not opted in never appears in a global response.
  - Depends on: TKH-071.

- [ ] **TKH-073: Add profiles, public visibility, and group lifecycle** (`feat/profiles`)
  - Store the GitHub avatar URL at login; profile visibility defaults to private.
  - Groups gain public/private visibility with a public slug.
  - Add invite links, group deletion, and ownership transfer.
  - Acceptance: public endpoints cannot distinguish private users or groups
    from nonexistent ones; owner-only operations are authorization-tested.
  - Depends on: TKH-072.

- [ ] **TKH-075: Count Codex compaction and compressed rollouts** (`fix/codex-compaction-usage`)
  - Newer Codex versions record automatic compaction requests as top-level
    `token_usage_record` lines matched to `compacted` items by response ID,
    and may store rollouts as `.jsonl.zst`. Neither is read today.
  - Blocked on evidence: no local rollout contains either. Build the fixture
    from records written by a Codex version that emits them, not from another
    tool's description of the format.
  - A compaction that also advances the running total must not count twice.
  - Until then, report a rollout the adapter cannot read instead of skipping
    it silently.
  - Depends on: TKH-065.

### M8 — TUI revamp

Goal: deliver the retro-terminal dashboard described in `docs/design.md`.

- [ ] **TKH-080: Build the TUI design system** (`feat/tui-design-system`)
  - Theme tokens, chip tabs, titled panels, tables, sparklines, delta
    markers, and the compact number and money formatter.
  - Acceptance: golden snapshots at 48, 88, and 120 columns, with and without color.
  - Depends on: TKH-060 (may run beside M6–M7 against fixtures).

- [ ] **TKH-081: Redesign the personal dashboard** (`feat/tui-dashboard`)
  - Headline tokens and estimate, 30-day sparkline, model and source
    breakdown with category detail, coverage, and freshness.
  - Depends on: TKH-080, TKH-064.

- [ ] **TKH-082: Build the board view** (`feat/tui-board`)
  - Standings, current-user row, standing panel, group panel with invite copy,
    period and metric chips, group/global switching, scrolling, and trends.
  - Depends on: TKH-081, TKH-073.

- [ ] **TKH-083: Add in-TUI onboarding and sharing** (`feat/tui-onboarding`)
  - First run scans, reveals the headline totals, and offers claim without
    leaving the TUI; a share action prints an intent link.
  - Acceptance: a new user reaches a ranked board from `npx` without docs.
  - Depends on: TKH-082.

### M9 — Website and sharing

Goal: make tokenholic discoverable and shareable without a browser dashboard.

- [ ] **TKH-090: Publish the static website** (`feat/web-pages`)
  - `web/` landing page, public profile, public board, and invite landing
    pages on GitHub Pages; the API serves public JSON with an origin allowlist.
  - Acceptance: private data returns not-found; pages work at phone width and
    show a wake-up state while the API cold-starts.
  - Depends on: TKH-073, TKH-080 (shared visual tokens).

- [ ] **TKH-091: Render share pages and cards** (`feat/share-cards`)
  - The API renders 1200×630 PNG cards and small share pages with Open Graph
    tags that redirect browsers to the website.
  - Acceptance: image dimensions and content tests pass; a card validator shows
    the large image when the API is awake.
  - Depends on: TKH-090.

### M10 — Platforms and release

- [ ] **TKH-100: Support Linux** (`feat/linux-support`)
  - Discovery paths, a user-level systemd timer, linux x64/arm64 artifacts and
    npm packages, and Linux CI.
  - Depends on: TKH-083.

- [ ] **TKH-101: Support Windows** (`feat/windows-support`)
  - Discovery paths, Windows Credential Manager, ACL-based private files, a
    Windows-compatible autosync lock, Task Scheduler autosync, windows
    x64/arm64 artifacts and npm packages, and a Windows CI runner.
  - Acceptance includes one manual run on a real Windows machine or VM.
  - Depends on: TKH-100.

- [ ] **TKH-102: Release and validate** (`chore/release-0.2`)
  - Deploy, run the two-account demo checklist, tag, and publish npm packages.
  - Depends on: TKH-091, TKH-101.

M6–M10 exit criteria:

- Two GitHub accounts on different machines can install, claim, join a
  group, sync, compare in the TUI, opt in to public visibility, and share a
  link that unfurls with a card.
- Totals survive repeated sync, repricing, and device revocation.

### M11 — Long-tail agents through ccusage

Goal: count usage from coding agents tokenholic has no native adapter for by
running a pinned ccusage and treating its output as an external contract.

Starts after M10. Claude and Codex keep their native adapters and are never
routed through ccusage: the native path gives minute buckets, cache-write
lifetimes, and control over what is read, and routing both ways would count
the same usage twice.

Constraints that shape this milestone:

- ccusage reports one row per day and model, in one timezone. Leaderboard
  day boundaries for these sources are therefore approximate and must be
  labelled so.
- Its rows carry a combined cache-write total with no lifetime, so any cache
  write is priced at the base rate and marks the estimate a lower bound.
- It is a Node program. It is available to `npx` installs and absent from a
  standalone binary; an unavailable runner is reported, never shown as zero.
- It is third-party code that reads local agent data. Pin an exact version
  and integrity hash, as `ccusage.lock.json` already pins 20.0.51, and never
  a floating major.

- [ ] **TKH-110: Run a pinned ccusage** (`feat/ccusage-runner`)
  - Execute `<agent> daily --json --no-cost --offline --timezone UTC` for the
    pinned version with an argument array, no shell, a time limit, and no
    network. Refuse any other version.
  - Validate output strictly against the pinned contract and ignore
    ccusage's own cost figures.
  - One failing agent degrades only that source.
  - Decide whether running ccusage is on by default or something the user
    turns on, and document what it reads either way.
  - Acceptance: contract fixtures validate; a missing runner, an unpinned
    version, and malformed output each appear in `tokenholic doctor`.
  - Depends on: TKH-102.

- [ ] **TKH-111: Admit day-level sources** (`feat/day-level-sources`)
  - Add the sources and their surface to the allowlists that enumerate
    Claude and Codex today: local SQLite checks, normalized validation, and
    sync protocol validation. The server schema already checks shape only.
  - A day's row grows until the day ends. Store it as an absolute value keyed
    by agent, day, and model that a later scan replaces, not as a new record.
  - Record that the value is day-level so boards can label the boundary.
  - Extend the price snapshot's provider list for the models these agents
    report. A model without a vendored price stays unpriced with a reason.
  - Acceptance: rescanning a growing day replaces its value without adding
    to it; totals survive repeated sync; `show-data` still matches the upload.
  - Depends on: TKH-110.

- [ ] **TKH-112: Enable the first agents** (`feat/ccusage-agents`)
  - Gemini CLI, OpenCode, GitHub Copilot CLI, Grok Build CLI, and Qwen Code.
  - Each ships a sanitized contract fixture, hand-computed totals, capability
    declarations, and documented coverage limits.
  - Do not claim generic chat support for any of them.
  - Acceptance: each agent's fixture matches its totals; a machine without
    an agent shows it as not found; the TUI SOURCES view lists coverage.
  - Depends on: TKH-111.

M11 exit criteria:

- A user of any listed agent sees its tokens on the board with day-level
  coverage labelled and no change to Claude or Codex totals.

## Extensions after MVP

Extensions are gated by evidence, not by the number of tools in the market.

### Source extensions

Gemini CLI, OpenCode, GitHub Copilot CLI, Grok Build CLI, and Qwen Code moved
into M11 and are collected through a pinned ccusage. ccusage ships further
adapters; add one only with its own fixture.

Cursor remains research until reliable token and model evidence is verified.

Linux and Windows platform support moved into M10.

Each source must ship with:

- sanitized fixtures;
- discovery tests;
- incremental/cumulative semantics tests;
- model and token capability declarations;
- duplicate and partial-write tests;
- explicit unsupported-version behavior.

### Product extensions

- Configurable group seasons and reset rules.
- Team-versus-team views.
- Self-hosted deployment documentation.
- Homebrew distribution.
- Personal quota status that remains separate from leaderboard token accounting.

### Deferred research

- Privacy-preserving cross-device deduplication when identical source logs are copied between devices.
- Signed client releases and provenance verification.
- A stable public adapter SDK.

## Project guardrails

- Do not add a source without trustworthy fixtures.
- Do not treat missing fields as zero.
- Do not infer a backend model silently from a routing alias.
- Do not send raw source records to the server.
- Do not install autosync without explicit consent.
- Do not call API-equivalent cost actual spend.
- Do not optimize for scale before measuring a real bottleneck.
- Do not publish adoption, latency, or reliability numbers that were not measured.

## Next delivery sequence

The local, multiplayer, packaging, and autosync implementation is complete,
and `@tokenholic/cli` 0.1.0 is published. M6, usage and cost correctness, is
complete. The remaining parity revamp continues in order: leaderboards and
profiles, the TUI revamp, the website and share cards, and finally Linux,
Windows, and a validated release. The free demo environment's end-to-end
validation (TKH-043) folds into TKH-102. Long-tail agents through ccusage
follow in M11, after the release. A measured private beta remains optional.
