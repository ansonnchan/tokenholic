# Architecture

Status: MVP and M6–M11 implemented and released. The hosted API runs `main`, the website is published, and `@tokenholic/cli` 0.4.0 is on npm (see `docs/release-validation.md` for what the release run did and did not check)
Last updated: November 22, 2026

## System goals

tokenholic must:

- collect supported coding-agent usage without intercepting model traffic;
- preserve the difference between observed and inferred metadata;
- remain useful locally before login;
- aggregate multiple devices into one account;
- retry collection and synchronization without inflating totals;
- expose private-group leaderboards without uploading source content;
- expose public data only after an explicit user or owner opt-in;
- stay simple enough to operate for a small beta.

Sections below describe the implemented system unless marked planned. The
"Planned target design" section records accepted M6–M10 decisions; where it
conflicts with an implemented description, the planned design wins for new
work.

## Repository shape

The repository uses the following current and planned boundaries:

```text
cmd/
  tokenholic/                 Native CLI entry point
  pricegen/                Generator that vendors the pinned price snapshot
internal/
  account/                 Native login and OS credential-vault storage
  adapters/                Source-specific discovery and parsing
  application/             Local refresh and dashboard orchestration
  autosync/                Opt-in hourly schedule and the shared sync lock
  collect/                 Deduplication and counter reduction
  demo/                    Invented usage and boards for `tokenholic demo`
  doctor/                  Privacy-safe source and storage diagnostics
  fixturecheck/            Fixture privacy enforcement
  modelcatalog/            Model resolution and pricing revisions
  normalize/               Canonical usage records
  preferences/             The user's saved choices, such as the TUI's look
  privatefs/               Owner-only files and the per-user application directory
  store/                   Local SQLite lifecycle and migrations
  sync/                    Allowlisted batches, retry, dry run, and acknowledgements
  tui/                     Bubble Tea application shell and local views
server/
  account/                 Authorized multi-device account aggregation
  auth/                    GitHub device flow and tokenholic device credentials
  cmd/api/                 Hosted HTTP API entry point
  group/                   Private groups, memberships, and invitations
  httpapi/                 Bounded JSON routing, rate limiting, and protocol errors
  leaderboard/             Ranking periods, metrics, coverage, and trends
  postgres/                Hosted connection and migration boundary
  sync/                    Authenticated absolute-revision application
  pricing/                 Server-owned model resolution and estimates
  ratelimit/               In-memory token buckets for the single instance
  sharecard/               1200×630 PNG share cards drawn from display text
  sharepage/               Share links: Open Graph pages and card routes under /s/
  website/                 The static site's address: allowed origin and links into it
migrations/                Embedded hosted PostgreSQL schema history
fixtures/                  Sanitized compatibility fixtures
packages/                  npm-distribution workspace
web/                       Static read-only website for GitHub Pages
docs/                      Architecture and product design
```

The root, `docs/`, and `internal/` READMEs provide navigation. Complex implemented subsystems also document their local contracts beside the code.

## Local data flow

The implemented local collection path is:

```text
Narrow read-only source discovery
    -> provider JSONL parser
    -> validated normalized observations
    -> raw source-identity deduplication
    -> per-session cumulative-counter reduction
    -> one SQLite transaction for observations, checkpoints, and adapter health
```

Agents with no native adapter take a second, separate path into the same
transaction:

```text
Does the agent's record folder exist?          (no: not found; nothing runs)
    -> pinned ccusage daily report for that agent
    -> strict contract validation
    -> one day total per agent, UTC day, and model
    -> the same SQLite transaction
```

An agent that is present but cannot be read, because ccusage is turned off,
absent from this install, not the pinned build, or failed, contributes no
records: its health says why and what was stored for it stays. The two paths
share no records, so nothing a day-level agent reports can change Claude or
Codex usage.

Persistence follows this transaction flow:

```text
Normalized observations (source evidence only)
    -> observation transaction
         - observation
         - source checkpoint
         - adapter health
    -> read committed observations
    -> aggregate transaction
         - absolute usage bucket revision (changed buckets only)
         - matching outbox revision
         - local preview of model resolution and estimate
```

Collection never depends on the server. The TUI reads derived local state
through an application interface and does not parse source files or query
SQLite directly.

### Local application boundary

The application service is the only bridge used by the interactive TUI. A
manual refresh runs discovery, parsing, normalization, and the atomic
observation commit before rebuilding absolute usage buckets from committed
observations. Presentation retains its last stable page while that work runs.

The board area reaches the server only through `internal/multiplayer`, which
reads a group's board or the global one and returns it as the server sent
it. The TUI holds no HTTP client and no credential, and it renders rank,
unranked reason, priced share, hidden ranks, and the named comparison
without recomputing any of them. Text from the server is stripped of control
characters before it is drawn.

Claiming from the TUI goes through an account interface the command layer
implements: begin a device login, wait for it, and upload the outbox. It is
the same login and upload `tokenholic claim` runs, split so a view can show
the code while it waits. The upload takes the lock the scheduled sync uses.
The TUI never sees the credential and uploads only after the user confirms.
`internal/share` builds the compose link for a post and drafts its text from
stored totals; the user's browser opens it and the user sends it. The
`tokenholic share` command uses the same draft outside the TUI.

Dashboard loads query Today, This week, 30 days, and All time using the local
timezone, add total tokens for each of the last 30 local dates, and attach the
privacy-safe database diagnostic snapshot. Storage, not presentation, defines
total tokens (the four categories that are not subsets of another) and the
priced share of each total, so every view states them the same way. The TUI
receives no source paths, record identities, observation rows, or raw provider
content. Startup and refresh failures are reduced to stable messages and refer
users to `tokenholic doctor` for path-redacted detail.

### Source adapters

An adapter owns:

- source installation and data-root discovery;
- supported record versions;
- incremental versus cumulative counter semantics;
- stable source record identity;
- token-category interpretation;
- observed model extraction;
- source capability and health reporting.

Adapters emit observations; they do not calculate leaderboard ranks or perform uploads.

The implemented adapters parse supported Claude and Codex JSONL natively. They recover from malformed complete records, defer actively written final lines, preserve nullable categories, and emit stable local identities.

Codex reports each request's usage beside the session's running total. The adapter emits the request usage and identifies it by session, turn, and running total, never by timestamp or file. A forked or resumed rollout replays its parent's history under new timestamps; those copies map to their originals' identities and are deduplicated across files, with the earliest copy supplying the time. Only rollouts without per-request usage are reduced from running totals.

Agents with no native adapter are read through one pinned release of ccusage,
a third-party program. `internal/adapters/ccusage` runs its executable
directly, with an argument array and no shell, only after the file's SHA-256
matches the digest compiled into tokenholic, and with a time limit, an
allowlisted environment, and no network. Its output is validated strictly
against the pinned contract, and its own cost figures are ignored. Running it
is on by default and is the user's to turn off with `tokenholic ccusage off`.
It comes with the npm package and is absent from a release archive, where
`tokenholic doctor` reports the agents that are therefore not counted.
tokenholic never downloads it. Claude and Codex are never read through it.
The [runner guide](../internal/adapters/ccusage/README.md) lists what it
reads, what each agent's totals cover, and their limits.

The agents read this way are GitHub Copilot CLI, Gemini CLI, Grok Build CLI,
OpenCode, and Qwen Code. tokenholic itself only checks whether each agent's
record folder exists, which is what separates an agent that is not installed
from one whose usage cannot be read. A scheduled run does not start through
the npm launcher, so enabling autosync records the ccusage location in the
schedule.

Claude Desktop discovery is limited to Code sessions and local Agent/Cowork evidence. Ordinary Claude Desktop chat storage is never scanned. Default Codex storage is classified as shared local evidence when CLI and Desktop attribution cannot be proven.

Where a provider keeps its records differs by operating system. Each layout is a plain function in the adapters package, tested on every system, and one build-tagged function picks the layout for the system the binary runs on. A location is added only with evidence from the provider's own documentation or source, cited in the [adapter guide](../internal/adapters/README.md); an operating system with no confirmed location has no roots and is reported as unsupported rather than guessed at.

### Normalization

The canonical layer separates:

- the model string recorded by the source;
- the canonical model identity;
- provider identity;
- the pricing lookup key;
- the quality of each resolution.

It also converts source-specific token semantics into non-overlapping billable categories where possible. Unknown categories remain nullable. Reasoning tokens recorded as part of output remain subset metadata.

Cache writes keep a combined total plus optional 5-minute and 1-hour lifetimes. A source that reports only the combined value leaves the lifetimes unknown. Claude lines that carry no model usage, the `<synthetic>` model and API error notices, are excluded by the adapter before normalization.

### Local persistence

SQLite runs in WAL mode and stores:

- adapter/source checkpoints;
- normalized observations;
- current usage bucket revisions;
- pending and acknowledged outbox records;
- model/pricing revisions;
- adapter health and last-success state.

Observation and checkpoint writes share a transaction. A source checkpoint cannot advance without the matching normalized state being durable.

Stored observation, source-root, session, and checkpoint identities are
one-way fingerprints. Raw filesystem paths and provider session identifiers do
not have schema columns. Nullable token categories use decimal text to preserve
both unknown values and the complete unsigned 64-bit range.

The local database uses the CGO-free `github.com/ncruces/go-sqlite3` driver and
lives under the platform user-configuration directory by default; on Windows
that is the local, non-roaming application data folder, because the database
and its upload state belong to one device. Embedded,
checksummed migrations are contiguous and run as one immediate transaction.
The directory and file are made owner-only before SQLite opens the database:
permission bits on Unix, and on Windows a protected access-control list
holding one entry for the current user. `internal/privatefs` owns that
difference so no other package branches on it. Connection-local foreign-key and busy-timeout settings are
applied to every pooled connection.

Local diagnostics read only allowlisted operational metadata from storage:
integrity and schema compatibility, collection freshness, adapter health,
checkpoint count, and pending-outbox count. The renderer distinguishes the
default and a custom database location without revealing its path. Diagnostic
checks never render observations, source identities, or database contents.

### Local usage buckets

Local bucket keys include the UTC interval, source, surface, observed model,
model evidence quality, measurement quality, normalization version, and
pricing evidence. They contain no catalog result, so a catalog or price update
never moves a bucket.

Pricing evidence is what a source proves about how a request was billed: its
service tier and its prompt size. A bucket carries the tier and a prompt band,
the request's place on a fixed ladder of the prompt lengths at which providers
change price. Requests with different evidence never share a bucket, because
the server prices a bucket from one rate card. A bucket whose source proved
neither omits both from its key and its JSON, so it is identical to one built
before the evidence existed and is not uploaded again.
Observations store source evidence only; canonical model, unpriced reason, and
estimate are a per-bucket preview derived with the bundled catalog and kept in
a separate local column that is never uploaded.

A bucket's revision increments only when its uploadable content changes. A
newer local revision replaces its earlier absolute value and the outbox retains
only that newest pending payload. An unchanged bucket keeps its revision and
its acknowledgement, so a refresh does not upload history again; only its
local preview is refreshed in place. Replaying a revision is therefore safe.
A stored bucket that the current observations no longer produce is retracted:
it is replaced by a known zero and uploaded, so usage a corrected adapter stops
counting does not linger locally or on the server. The sync client reads
only this committed outbox, sends at most 100 allowlisted buckets per request,
and marks only the exact acknowledged revision as durable remotely. A late
acknowledgement cannot clear a newer local replacement.

Both real sync and `show-data`/`sync --dry-run` collect, normalize, and commit
locally before reading the outbox. The offline preview writes the exact request
JSON produced by the real serializer without requiring an API origin, account,
or credential, and performs no network or acknowledgement mutation. Transient
network, rate limit, and server failures receive one exponential-backoff retry
with jitter; an interrupted or rejected batch remains pending.

An upload pass has no overall deadline. Each request is bounded, retried at
most once, and never made to wait more than a minute by the server, so a pass
either acknowledges another batch or stops; each acknowledged batch is
committed locally before the next is sent. A first upload of a long history
therefore finishes in one run however slow a batch is, and a run that stops
partway reports how far it got and leaves only the rest pending. The local
scan before it keeps its own five-minute limit.

A day-level source is the one case that is not a minute. Its usage arrives as
one total per UTC day and model, stored as a single record with the count
semantics `day_total` that a later reading replaces in place, so a day that
grows between scans is never counted twice. That record becomes one bucket
filed at 00:00 UTC of its day and marked `"granularity": "day"`. The mark is
part of the bucket's key and JSON; a minute bucket has no such field, so no
existing bucket changed when these sources were admitted. Normalized
validation, the SQLite schema, and the sync protocol each tie day totals to
the day-level sources in both directions, so Claude and Codex usage can never
be stored or uploaded as a day. A day's place against a local midnight is
approximate: in a timezone behind UTC its usage shows under the previous
local date until the UTC date changes.

Local buckets use one-minute UTC intervals. This preserves exact modern IANA
timezone day and week boundaries while grouping events that occur in the same
minute. Today starts at local midnight, weeks start Monday, and the 30-day view
uses 30 local calendar dates rather than a rolling 720-hour duration. Token and
price coverage remains complete, partial, or unavailable through every query.

## Server data flow

The hosted persistence, device authentication, and synchronization foundations are implemented. PostgreSQL stores users,
GitHub identities, independently revocable devices and credential hashes,
pricing revisions, and privacy-limited absolute usage buckets. The HTTP API
implements GitHub device login, credential rotation, device revocation, and
bounded authenticated bucket upload, behind per-address and per-credential
rate limits.

Server migrations are embedded in the Go binary, contiguous, checksummed, and
applied once inside an advisory-locked transaction. Applied migration history
must match the binary exactly; changed or newer history is rejected instead of
being silently repaired. The current implementation targets PostgreSQL 18 and
uses pgx for connection pooling and migration execution.

The schema owns bucket identity with `(device_id, bucket_key)`. Equal revisions
with the same client-evidence payload are no-ops, older revisions are
acknowledged as stale, and newer absolute revisions replace earlier values.
The payload hash covers client evidence only. Every server-derived column sits
outside it, so a catalog update cannot turn an identical retry into a
conflict. Other equal-revision differences reject the transaction.

A batch waits on the database a fixed number of times, whatever its size.
The API and its database are a network hop apart, so that count is what a
batch costs. One read confirms the device and locks every stored row the
batch names, the revisions are compared in the API, and the rows that change
are written in one pipelined exchange, all in key order inside one
transaction. Reading and writing one bucket at a time was 203 waits for 100
buckets and about twelve seconds on the hosted API; it is now four. A test
in `server/postgres` fails if the count grows with the batch again.
Foreign keys make user ownership explicit and cascade user deletion through
devices, credential hashes, and usage buckets. The schema has no columns for
raw source records, local identities, paths, prompts, responses, code, project
metadata, commands, or provider credentials.

The server registers only trusted bundled model and pricing revisions. Upload
serialization is an explicit field allowlist and stores normalized columns plus
a payload hash, never the client JSON body or local observation IDs.

Sync protocol v2 carries observed evidence only: the observed model ID, how the
source attributed it (`reported`, `inferred`, or `unknown`), measurement
quality, optional pricing evidence (service tier and prompt band), and the six
token categories plus reasoning. It has no field for a
canonical model, provider, pricing key, estimate, or pricing revision. The
`server/pricing` engine derives all of those from the bundled catalog and
stores them beside the raw counts with the revision that produced them. A
model the server does not recognize is stored as unknown and unpriced; it is
never a reason to reject an upload. Unpriced tokens are counted, and priced
plus unpriced tokens always equal the bucket's known total.

`POST /v1/sync/buckets` answers `426 Upgrade Required` with an upgrade
instruction for every caller. The v2 route rejects a body that carries v1's
client-resolved fields.

Because derived values are a pure function of stored evidence and a pricing
revision, the API re-derives history at startup. After migrations and before
it serves a request, it takes an advisory lock and, in one transaction, rewrites
the derived columns of every bucket whose revision differs from the bundled
one. Readers see the previous consistent values until that commit; a failure
rolls the whole pass back and aborts startup. Rows already at the active
revision are skipped, so a restart with unchanged prices does nothing. Client
evidence, bucket revisions, payload hashes, and receipt times are never
modified.

This is correct for the single demo instance. During a deploy overlap, an
older instance can still write rows at its own revision; they are repriced at
the next start. The pass holds one transaction over all stale rows, which is
acceptable at demo scale and should be measured before the table grows large.

Account usage is read as one repeatable PostgreSQL snapshot. Normalized token
categories, non-overlapping total tokens, and API-equivalent estimates preserve
complete, partial, unavailable, and empty coverage. Numeric sums remain decimal
strings so multiple devices cannot overflow a native unsigned integer.

Only active devices contribute to the account total. The response includes all
account devices with revocation, last-authenticated, and last-synchronized
state. Revocation blocks subsequent writes and removes that device from active
totals without deleting its synchronized history or affecting sibling devices.
Authenticated account deletion hard-deletes the user row. Ownership foreign
keys erase GitHub identity, devices, credential hashes, synchronized usage,
owned groups, invitations, and memberships in the same transaction while
leaving unrelated accounts and shared pricing revisions intact. The native
client removes its OS-vault session only after the API confirms deletion;
local SQLite usage remains on each machine.

```text
GitHub device authorization
    -> tokenholic user
    -> revocable device credential

Signed-in client
    -> bounded HTTPS batch
    -> authentication and protocol validation
    -> server-derived model, estimate, and priced/unpriced tokens
    -> idempotent bucket upsert
    -> acknowledgement

TUI
    -> authorized group query
    -> aggregate account devices
    -> rank by period and metric
```

The native `multiplayer` boundary owns authenticated group HTTP calls and reads
the existing device session from the operating-system credential vault. The
TUI consumes its presentation-safe snapshots; it does not own credentials,
HTTP, group mutation policy, or leaderboard aggregation. With no API origin,
the local TUI remains fully usable and the Groups area explains how to opt in.

### Identity and devices

The leaderboard identity is the user account. Devices only collect and upload.

- One GitHub identity maps to one tokenholic user.
- One user can register multiple devices.
- Each device has a separate revocable credential.
- Memberships and invites reference users, not devices.
- Revoking a device prevents future writes without deleting other device data.

The CLI begins authorization through the tokenholic API and displays GitHub's
verification URL and user code. Short-lived GitHub device state is encrypted
and authenticated inside an opaque flow token. After approval, the API uses the
GitHub access token only in memory to read the stable numeric ID and login, then
discards it. Neither PostgreSQL nor the native client stores that token.

tokenholic device credentials contain a public credential ID and 256-bit random
secret. PostgreSQL stores only the SHA-256 secret hash. Rotation revokes the old
credential atomically, logout revokes the entire device, and other devices on
the same account remain independent. The native client stores its session in
the operating-system credential vault with no plaintext-file fallback.

### Groups

Groups have owner and member roles. Invite codes are expiring, usage-limited
authorization artifacts, not permanent credentials. Only invite hashes are
persisted. Group reads, invitation creation, joining, leaving, and member
revocation authorize active membership inside the same repository boundary
that performs the operation. Non-members receive the same not-found result
whether a group exists or not.

The owner alone can make the group's board public or private, transfer
ownership to another active member, and delete the group. Each of those locks
the caller's membership and checks the owner role in the transaction that
makes the change: a member is told the operation is forbidden, and anyone
else gets the same not-found as for a group that does not exist. Deletion
removes memberships and invitations with the group and leaves every account's
usage alone. A transfer demotes the previous owner to a member.

### Public visibility

Nothing is public by default. Three independent choices, each made by the
person it concerns, open a read-only view:

| Choice | Made by | Opens |
| --- | --- | --- |
| `users.profile_visibility = 'public'` | The account | `GET /v1/public/users/{login}`: GitHub login, avatar, and aggregate totals |
| `groups.visibility = 'public'` | The group owner | `GET /v1/public/groups/{slug}/leaderboard`: the board, limited to members whose own profile is public |
| `users.global_opt_in` | The account | A row on the global board for signed-in readers |

An owner publishing a group therefore never publishes another member: a
member with a private profile is absent from the public board, not anonymized
on it. A group's slug is random, assigned when it first becomes public, and
kept afterwards so a link survives going private and back; while the group is
private the slug resolves to nothing.

Public routes take no credential and are read-only. Each answers a private,
deleted, or nonexistent record with one identical response: same status,
headers, and body. That holds by construction as well as by test: the
visibility condition is part of the lookup query, so a private record matches
no row and reaches the same code path as a missing one, and input that could
not name a record is refused before any lookup. Public responses carry no
account, device, or group identifier and no sync times. A public board applies
the global board's minimum population and row limit.

An invite code can be previewed without signing in, at
`GET /v1/public/invites/{code}`, which names the group a usable code joins.
An unknown, expired, used-up, or malformed code is the same not-found. When
the API knows the website's address, an invitation also carries a link whose
fragment holds the code, so the code is never sent to the server hosting the
site.

A browser may read the public routes only from the website. The API is told
the site's HTTPS address in `TOKENHOLIC_WEB_ORIGIN`; `server/website` parses
it into the origin a browser sends and the base for links, which keeps a
path because a GitHub Pages project site lives under one. A request for a
public route whose `Origin` header is exactly that origin is answered with
`Access-Control-Allow-Origin` naming it. No other origin, no other route, no
wildcard, and no credentials are ever allowed, and with the variable unset no
browser origin can read anything. The header depends only on the request's
path prefix and origin, never on the record, so the allowlist cannot be used
to tell a private record from a missing one. It wraps the rate limiter, so a
refused request is readable by the page too.

Share links are the other public surface. `GET /s/u/{login}` and
`GET /s/b/{slug}` return a small HTML page whose Open Graph tags name a PNG
card at the same address plus `/card.png`, and whose meta refresh sends a
browser on to the website's page. They read through the same two services as
the public JSON routes, so they can show nothing those routes do not, and
they keep the same rule: every miss under `/s/`, for a page or a card, a
private, deleted, or nonexistent record, a malformed name, or an unknown
path, is one response with identical status, headers, and body. The routes
exist only when the API knows the website's address.

A share page is never cached. A card may be reused by a shared cache for five
minutes and carries an entity tag derived from its content; the page links
the card with that tag in its address, so a crawler fetches a new card when
the totals change. Making a profile or group private stops new pages and
cards at once. A cache may serve the last card for up to those five minutes,
and a site that already unfurled a link keeps its own copy, which the API
cannot recall.

Leaderboard authorization checks membership on every request. A member of one
group cannot enumerate another group or its users.

### Leaderboards

One repeatable-read snapshot aggregates every active member's active devices.
The query calculates Today, current week, and 30-local-date boundaries from the
group's IANA timezone; all periods end at the response generation time. Weeks
start Monday. All-time has no lower bound.

Boards rank by total tokens or by the API-equivalent estimate. Output tokens
and the other categories stay in personal views.

- **Tokens** rank every member with a known total. A total can be a lower
  bound, because some sources never report every category; it is ranked and
  stays labelled partial. Unpriced tokens count in full.
- **Estimates** rank a member only when at least 95% of their tokens in the
  window are priced. Below that the estimate leaves out too much usage to
  compare, so the row shows its lower bound and priced share without a rank.
  The share is computed from the priced and unpriced token counts the server
  stores beside every bucket.

Equal values share a rank and the next rank skips. Unavailable and empty
values remain distinct from zero, and every unranked row says why.

Each row carries a trend: its value and rank in a comparison window read in
the same snapshot. A calendar period is compared with the one before it, cut
off after the same elapsed time, so a board early in the week is measured
against the same part of last week. The previous period starts on a local
calendar boundary and never overlaps the current window. All-time has no
previous period and is compared with itself 24 hours earlier; the response
names which comparison it used. A member with usage now and none in the
comparison window is marked new. Ranks in the comparison window follow the
same rules over the current members.

Usage is self-reported. A member whose tokens on one local day exceed 5
billion, about ten times the heaviest day observed during development, gets an
advisory plausibility note. The note changes neither the value nor the rank.

Every row also includes last-upload freshness, source mix, most-used model,
and counts for unknown or inferred model resolution and unavailable API
estimates. Revoked devices and inactive memberships do not contribute.

A row also counts its day-level buckets in the window: whole UTC days from a
source the client reads through ccusage. The server stores such a bucket with
`interval_seconds` 86400 instead of 60 and files it at the start of its UTC
day, so which side of the board's day boundary its usage fell on is
approximate. The count lets a board say so. The usage is ranked as usual.

The global board ranks every account that opted in, with the same rules and
UTC boundaries, since its members share no timezone. `users.global_opt_in` is
false by default and changes only through an authenticated request that states
the choice explicitly. Every board query is built from one definition of the
scope's members, so no query can read an account the scope does not include.
Any signed-in account may read the board. Its rows carry a display name and
aggregate values: the account ID is present only on the viewer's own row, and
sync times are omitted. Ranks are hidden while fewer than three members can be
ranked, because a rank among one or two accounts says nothing; values and
order stay. A private group has no such floor. A long board returns its first
100 rows plus the viewer's own.

### Pricing

Pricing is a versioned registry built from a pinned LiteLLM `model_prices`
snapshot plus small reviewed overrides. `cmd/pricegen` vendors the pinned
commit, verifies its SHA-256, and keeps only first-party token prices: those
of Anthropic, OpenAI, Google, and xAI. Every
override cites an official provider pricing page and retrieval date. The
registry provides standard API-equivalent estimates only; it does not claim
subscription usage was billed. The
[model catalog guide](../internal/modelcatalog/README.md) is authoritative for
the data files and rules.

Resolution uses exact identifiers plus two explicit suffix rules: a client
context-window marker such as `[1m]`, and a trailing snapshot date. Ambiguous
routing aliases remain unknown instead of selecting a backend model. Every
unpriced derivation carries a reason.

A model can have several rate cards: one per service tier the snapshot
publishes, and one above a prompt-length threshold. A bucket's pricing
evidence selects the card, and the stored pricing key names it. A proven tier
or prompt length without a vendored rate stays unpriced. A model that charges
more above a threshold is priced only when the bucket proves which side its
requests fell on. A day-level bucket proves no prompt length, so such a
model stays unpriced for the day-level sources. A tier the source did not prove is priced on the standard
card; that default is a recorded product decision, described in the model
catalog guide, and it is the only place an estimate assumes a billing fact.

Estimates price five categories: uncached input, cache read, 5-minute cache
write, 1-hour cache write, and output. A cache write with an unknown lifetime
is priced at the 5-minute rate and makes the estimate a lower bound. Unknown
models retain token totals and display unavailable cost.

The catalog and pricing share one revision derived from their content, so any
price or rule change produces a new revision. Stored history follows the
active revision: when it changes, the server reprices every bucket from its raw
token counts.

The CLI and API build the same catalog code. The server's derivation is stored
and authoritative; the CLI's is a preview labelled as such. Their revisions may
differ without affecting uploads.

## Deployment

The optional demo environment uses one stateless Go API deployment on Render
and one Supabase PostgreSQL database. A root Blueprint and a production
container make that environment reproducible without introducing application
dependencies on either provider. Render deploys the service on every push to
`main`, so merging a pull request that changes the server is a production
deploy, and embedded migrations run when it starts. Local collection remains the primary product
and does not require the hosted service. The static website is served
separately by GitHub Pages and depends on the API only for public JSON. A
workflow publishes `web/` as it is and writes one value into it, the API
origin; it stays skipped until a repository variable supplies that origin.

How many proxies sit in front of the API decides which `X-Forwarded-For`
entry is the client. The API logs the entry count it sees, once per distinct
count and without any address, so the operator measures the chain instead of
guessing it.

The free environment exists for end-to-end personal testing and interviews,
not for a production availability or durability claim. Free-tier sleeping,
pausing, and missing automatic backups are accepted constraints. If measured
usage later justifies a persistent beta, infrastructure-as-code, managed
backups, budgets, and alerts should be evaluated as a separate operational
milestone rather than implied by the demo configuration.

The MVP does not require:

- WebSockets;
- Redis;
- queues;
- separate workers;
- a load balancer;
- horizontal scaling.

Polling on TUI entry and explicit refresh is sufficient. Add infrastructure only after measurement identifies a bottleneck or reliability requirement.

## Planned target design (M6–M10)

These decisions are accepted and drive the roadmap's M6–M10 milestones.

### Server-authoritative models and pricing

Implemented. The sections above describe the shipped behavior; this list
records the decisions behind it.

```text
client: observed model ID + token categories (protocol v2)
  -> server/pricing: alias rules -> canonical model -> price entry
  -> estimate + priced/unpriced token counts + pricing revision
  -> usage_buckets (raw tokens retained for repricing)
```

- Clients stop sending canonical model, provider, pricing key, resolution
  quality derived from the catalog, and estimates. They still send source
  evidence quality (reported or inferred) and nullable token categories.
- The client resolver remains only for offline TUI and `show-data` previews,
  labelled as a preview. Server and client catalog versions may differ
  without rejecting uploads.
- Prices come from a pinned LiteLLM `model_prices` snapshot, vendored by a Go
  generator that verifies the upstream commit and SHA-256 and keeps only
  token-priced fields. A reviewed overrides file covers models newer than
  the snapshot; each entry cites an official provider URL and retrieval date.
- Alias rules are explicit, reviewed, and tested. They may strip documented
  date suffixes or context markers such as `[1m]`; they never map ambiguous
  routing aliases. Each match records its kind (exact, alias, reviewed).
- Five priced categories: uncached input, cache read, 5-minute cache write,
  1-hour cache write, and output. A combined cache write without a time-to-live
  is priced at the 5-minute rate and marks the estimate partial.
- Service-tier and prompt-length prices are selected by pricing evidence the
  client uploads. A tier or length with no vendored rate, and a prompt length
  the bucket cannot prove, stay unpriced. An unproven tier is priced as
  standard.
- Estimates use integer micro-dollars with exact big-integer arithmetic.
- When the active pricing revision changes, startup reprices every stored
  bucket from raw tokens inside one advisory-locked transaction.
- `/v1/sync/buckets` returns `426 Upgrade Required`. The v2 migration replaces
  demo usage data rather than translating it.

### Leaderboard v2

Metrics, the cost-coverage rule, trends, the plausibility note, and the
global scope are implemented and described under "Leaderboards" above. The
public-group scope follows.

- Scopes: a private group (membership-authorized), a public group (by slug,
  read-only), and the opt-in global board.
- Metrics: total tokens and API-equivalent estimate. Output-only ranking is
  removed; category detail stays in personal views.
- Cost ranks require at least 95% of the window's tokens to be priced. Rows
  below the threshold show a lower bound and coverage without a rank.
- Periods keep their existing boundaries: Today, This week (Monday start), 30
  local dates, and All time, in the group timezone; the global board uses UTC.
- Trends compare each user's rank and value with the previous period
  truncated to the same elapsed time, computed in the same snapshot query.
  All-time compares with the state 24 hours earlier. First-period entrants
  are marked new.
- The global board shows values without ranks until three members can be
  ranked. Private groups rank any number of members.
- No Redis or materialized rollup: indexed minute buckets are sufficient until
  a measured query shows otherwise.

### Identity, visibility, and integrity

- Implemented: login stores the GitHub avatar URL alongside the stable numeric
  identity, accepting only GitHub's avatar host.
- Implemented: `users.profile_visibility` and `groups.visibility` default to
  private and `users.global_opt_in` to false. Public endpoints return the
  same not-found result for private and nonexistent users or groups.
- Implemented: group lifecycle adds invite links, owner deletion, and
  ownership transfer.
- Implemented: an in-memory token-bucket limiter runs before every route
  except the health check, per client address and per presented credential,
  with a stricter limit on starting a device login. A refused request gets
  `429` with `Retry-After`. State is local to one process, which is correct
  for the single demo instance only; the server guide lists the limits and
  how the client address is determined behind a proxy.
- Implausible daily totals are flagged, not capped or silently dropped.

### Public web surface

```text
GitHub Pages (static web/)  --fetch-->  API public JSON (origin allowlist)
X crawler  -->  API share page (/s/...) with Open Graph tags + PNG card
                 -> browsers redirect to the GitHub Pages page
```

- Implemented: the website is static HTML, CSS, and minimal JavaScript with
  no framework or build step. It never writes and never signs in. Its only
  network code reads the three public routes with credential-free GETs.
- Implemented: the API's origin allowlist, described under "Public
  visibility".
- Implemented: the site formats values with a JavaScript port of
  `internal/numfmt` that runs the Go formatter's test vectors, and takes its
  colors from the TUI palette under a test, so neither can drift.
- A page shows a private, deleted, or nonexistent record as one state, and
  an unknown value as unknown, exactly as the API reports them.
- Implemented: share pages and 1200×630 PNG cards are rendered by the Go API
  because crawlers do not execute JavaScript. `server/sharepage` turns public
  values into display text and serves the routes; `server/sharecard` only
  draws that text, with the website's fonts embedded in the binary. Cards are
  cacheable for five minutes and content-hashed. The absolute addresses Open
  Graph needs are built from the request's host, which must be a plain name,
  so the API needs no setting for its own address.
- Implemented: `og:url` names the share page itself. A crawler that follows
  it must land on the tags again, not on the static site, which has none per
  record.
- Implemented: `tokenholic share` and the website's share buttons build
  `x.com/intent/post` links client-side. The native client attaches a share
  page only after fetching it without a credential, as a crawler will, so a
  private profile or a server without share pages yields a text-only post.
- Render free-tier cold starts can delay pages and cards; that is an accepted
  demo limitation. Implemented for pages: a read that has not answered within
  a few seconds shows a wake-up state and keeps retrying for two minutes.

### Platforms

- Linux, implemented. Claude Code and Codex keep their records under the
  home directory on Linux and ignore the XDG variables, so discovery reads
  `~/.claude/projects` and `~/.codex` as on macOS. Two locations do follow
  `XDG_CONFIG_HOME`: Claude Desktop's data folder, and the directory Claude
  Code 1.0.28 to 1.0.30 briefly used, read as an optional root. tokenholic's
  own database and lock stay in the user configuration directory, which is
  `XDG_CONFIG_HOME` there.
- Linux credentials use the Secret Service on the session D-Bus through the
  same keyring library as macOS. A machine without one gets a message naming
  it before any GitHub authorization starts; there is no file fallback.
- Linux autosync is a user-level systemd timer, described under
  "Distribution architecture". Linux x64 and arm64 archives and npm packages
  ship with the macOS ones, and CI runs the Go suite on Linux.
- Windows, implemented and CI-verified; the manual run on a real machine is
  still to do and is written out in
  [release validation](./release-validation.md).
  - Discovery reads Claude Code and Codex under the user profile and Claude
    Desktop's data folder under `%APPDATA%`, plus the packaged install's
    redirected folder as an optional root.
  - Credentials use Windows Credential Manager through the same keyring
    library, with no file fallback.
  - Private files carry a protected single-user access-control list instead
    of permission bits, and tokenholic's own files live under
    `%LOCALAPPDATA%`.
  - The sync lock is `LockFileEx`, released by the system when the holder
    exits, as `flock` is on Unix.
  - Autosync is a Task Scheduler task that runs as the signed-in user with no
    stored password. Task Scheduler cannot set an environment or redirect
    output, so the task runs `tokenholic autosync run <job file>` and a
    private job file carries an allowlisted environment and the log paths.
  - A group created from Windows is created in UTC: Windows does not name
    its timezone the way the server needs, and the client has no mapping
    yet. The command prints the timezone it used.

## Distribution architecture

The core application is a native Go binary. pnpm manages npm package metadata and publishing:

```text
@tokenholic/cli
    -> small platform selector
    -> @tokenholic/darwin-arm64
       @tokenholic/darwin-x64
       @tokenholic/linux-arm64
       @tokenholic/linux-x64
       @tokenholic/win32-arm64
       @tokenholic/win32-x64
```

The user still runs `npx @tokenholic/cli`. Release automation cross-compiles the
Go binary without CGO for every target, verifies each file's format and CPU: Mach-O, ELF, or PE
(and, for Linux, that it is statically linked, so one build serves glibc and
musl systems), normalizes archive metadata, and publishes checksummed native
artifacts. The build machine can execute only its own target, so a runner for
each other system runs its archive before anything is published. Each
native archive includes the pinned ccusage contract and no ccusage
executable. The npm launcher package pins the six ccusage platform packages
at one exact version; npm installs the one for the current system, the
launcher passes its location to the native program, and the program runs it
only when its digest matches the compiled pin. Platform packages consume these
artifacts, and the umbrella npm package is published only after every platform
package exists. One list in `scripts/platforms.mjs` names the shipped
platforms; tests hold the launcher, the manifests, and the release tool to it.
The launcher resolves only the package matching the current operating system
and CPU, then executes its binary directly without a shell. Unsupported targets
and missing optional packages fail explicitly.

A release build carries the hosted API's address, set at link time from one
constant in `cmd/release`, so no user configures one. `TOKENHOLIC_API_URL`
overrides it for self-hosting and development. A build from source carries no
address and cannot reach the hosted service by accident. `tokenholic doctor`
reports which of the three applies and never prints an overridden address.
Autosync writes the address into its schedule only when it was overridden;
otherwise the scheduled copy uses its own built-in address.

Autosync copies the current native executable to a stable private path only
after explicit user consent: `tokenholic autosync enable`, or a yes to the
question a claim asks once its first upload is done. Both install the same
schedule from the same settings. The question is asked only where a person
can answer it; a claim run without a terminal installs nothing and names the
command instead. The operating system's own per-user scheduler
runs it at login and hourly: a launch agent on macOS and a systemd user timer
on Linux, and a Task Scheduler task on Windows. One manager drives all
three; a scheduler supplies only its file locations, file contents, and
commands, and is tested on every system. The
schedule stores no credentials and writes only the sync command's
privacy-safe summary or stable failure message to private local logs. Manual
and scheduled synchronization share a non-blocking advisory lock, so they
cannot overlap. Disabling autosync unloads the schedule and removes its
binary, definition files, and logs without touching SQLite usage or the
OS-vault session. A system whose scheduler cannot be reached, such as a
container or WSL without systemd, is told what is missing and nothing is
installed. The [autosync guide](../internal/autosync/README.md) lists each
platform's files.

## Security and privacy invariants

- Provider credentials never leave their provider-owned storage.
- Raw source records never leave the machine.
- Upload serialization uses an allowlist, not field omission from a raw record.
- Logs avoid payload bodies, secrets, and source paths.
- Device credentials are stored using the best available OS credential store.
- Deletion and device revocation are first-class API operations.
- Client data is self-reported and never presented as cheat-proof.

## Feasibility decisions

M0 established these local rules:

- ccusage is run, at one pinned and digest-checked release, only for agents without a native adapter. Claude and Codex stay native.
- Claude Desktop coding roots are discovered narrowly and parsed through native compatible-record adapters.
- Default Codex CLI and Desktop evidence is represented once as `shared_local`; a distinct `CODEX_HOME` permits separate CLI and Desktop roots.
- Cross-root deduplication happens before cumulative snapshots become deltas.
- Codex usage is read per request when the rollout reports it; replayed history is deduplicated by session, turn, and running total.
- Source capabilities distinguish reported, derived, inferred, and unsupported fields.
- Missing model or token metadata remains unknown rather than becoming zero or a guessed alias.

The local interval is fixed at one minute. A later network protocol may define
a coarser upload projection, but it must retain absolute replacement revisions
and the same privacy allowlist.
