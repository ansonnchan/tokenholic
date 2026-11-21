# Server boundary

The hosted PostgreSQL persistence foundation is implemented in `postgres`. It
opens a pgx connection pool and applies the embedded, checksummed migrations in
one advisory-locked transaction. `auth` and `httpapi` implement GitHub device
authorization, tokenholic credential rotation/revocation, and bounded JSON routes.
`sync` authenticates privacy-limited uploads, `pricing` derives the
server-owned model and estimate columns, and `postgres` applies absolute
revisions transactionally.

`account` exposes one authenticated usage row across active devices. Its
PostgreSQL query runs in a repeatable-read snapshot, preserves unavailable and
partial coverage, and reports device freshness and revocation without local
machine identity. Revoked-device history remains stored but no longer
contributes to active account totals. Its deletion operation authenticates the
presented device and hard-deletes the owning user so every account-owned row is
erased by foreign-key cascades in one transaction.

`group` implements private group creation, member-authorized reads, and
owner-only invitation and revocation operations. Invitations expire, have a
fixed use limit, and are stored only as SHA-256 hashes. PostgreSQL locks invite
and membership state in the same transaction so retries cannot over-consume a
code or leave a group without its owner.

`leaderboard` defines timezone-relative periods, the two ranking metrics, cost
coverage, trends, and competition ties. Its PostgreSQL repository authorizes
active membership and aggregates active devices over the current window and
its comparison window in one repeatable-read snapshot; the package then ranks
both. The rules it applies:

| Rule | Value |
| --- | --- |
| Metrics | `total_tokens` and `api_estimate`. Any other metric is invalid input |
| Token rank | Every member with a known total, including a lower bound |
| Estimate rank | Only members with at least 95% of their tokens priced; others show their lower bound and coverage without a rank |
| Trend | Rank and value against the previous day, week, or 30 days at the same elapsed time; all-time against 24 hours earlier |
| New entrant | Usage in the current window and none in the comparison window |
| Plausibility note | More than 5 billion tokens on one local day. Advisory only: nothing is capped, dropped, or unranked |

The board response states the coverage threshold and the plausibility limit,
so a client explains them without copying server policy. Freshness and
model/price limitations travel with every row.

The global board is the same query over a different set of members: accounts
whose `global_opt_in` is true. That column is false by default.
`PUT /v1/account/global-board` changes it and requires an explicit
`{"participating": true|false}`; anything else is rejected, so no malformed
request can opt an account in. `GET /v1/global/leaderboard` needs a signed-in
device but not participation. It uses UTC boundaries, omits account IDs (except
the viewer's own row) and sync times, hides ranks while fewer than three
members can be ranked, and returns at most 100 rows plus the viewer's own.

The schema contains users, stable GitHub identities, independently revocable
devices and hashed credentials, pricing revisions, privacy-limited absolute
usage buckets, private groups, memberships, and hashed invites. Bucket identity
is unique per device.

The server must never accept prompts, responses, source code, command text,
local paths, project or repository metadata, provider credentials, or raw
source records. Local discovery, provider parsing, cumulative-counter
reduction, and raw-record deduplication remain native-client responsibilities.

The API requires `DATABASE_URL`, `GITHUB_CLIENT_ID`, and a dedicated 32-byte
base64 `TOKENHOLIC_AUTH_STATE_KEY`. `TOKENHOLIC_TRUSTED_PROXY_HOPS` is optional
and described under [Rate limits](#rate-limits). `TOKENHOLIC_LISTEN_ADDR` defaults to
loopback. When that explicit override is absent, a hosting platform's numeric
`PORT` binds on all container interfaces before the loopback default is used.
Register the GitHub OAuth app with Device Flow enabled and no repository scopes.
GitHub access tokens are used only in memory for the authenticated-user request
and are never stored or returned to the native client.

The upload route accepts at most 100 buckets and one MiB of JSON. Equal
revisions are no-ops only when their canonical payload hashes match; stale
revisions cannot replace newer state, and a different equal-revision payload
rolls back the whole batch. Token counts remain self-reported local evidence.
Uploads use sync protocol v2 at `POST /v2/sync/buckets` and carry observed
evidence only. `pricing` resolves each observed model with the bundled catalog
and derives the API-equivalent estimate, priced and unpriced token counts, and
the pricing revision. A bucket may also carry pricing evidence, the service
tier and prompt band of its requests, which selects among the model's rate
cards; the stored pricing key names the card that was applied. Evidence is
optional and is stored as NULL when absent. The
[model catalog guide](../internal/modelcatalog/README.md) owns the selection
rules. An unrecognized model is stored as unknown and unpriced,
never rejected, so a client with a different catalog always syncs.
`POST /v1/sync/buckets` returns `426 Upgrade Required` with an upgrade
instruction.

At startup, after migrations and before serving, `postgres.RepriceUsage`
re-derives every stored bucket whose pricing revision differs from the bundled
one. The pass runs in one advisory-locked transaction, rewrites only
server-derived columns, and is a no-op when nothing is stale. A failure rolls
back and stops startup rather than serving a mix of revisions. Moving the
price pin or editing an override therefore reprices history on the next
deploy with no manual step.

## Rate limits

`ratelimit` is an in-memory token bucket and `httpapi.RateLimited` applies it
before any route runs. A request over a limit gets `429 Too Many Requests`
with a `Retry-After` header in whole seconds, rounded up, and the body
`{"error":"rate_limited"}`. It reaches no route and costs no further
allowance.

| Limit | Key | Burst | Refill | Applies to |
| --- | --- | --- | --- | --- |
| Per address | Client address | 300 | 1 every 200 ms (300 per minute) | Every request |
| Per credential | SHA-256 of the bearer credential | 300 | 1 every 200 ms (300 per minute) | Every request that presents a credential, valid or not |
| Device login start | Client address | 10 | 1 every 6 s (10 per minute) | `POST /v1/auth/device/start`, which calls GitHub without a credential |

`GET /healthz` is exempt, so a platform's probes cannot be throttled into
reporting the service down. The values are constants in `cmd/api`. They are
sized so a first sync of 30,000 buckets fits inside the burst; the sync client
waits for `Retry-After` and retries once, and anything still pending uploads
on the next sync.

An IPv6 client is keyed by its /64. Limiter state never holds a credential,
only its digest, and idle keys are dropped as their buckets refill. If more
than 50,000 keys are active at once the rest share one overflow bucket, which
bounds memory and still limits a flood of new keys.

The client address is the connection's address unless
`TOKENHOLIC_TRUSTED_PROXY_HOPS` says how many reverse proxies in front of the
API append to `X-Forwarded-For`. With `n` trusted proxies the client is the
`n`th entry from the right; entries further left came from the client and are
never read. Set it too high and a client can choose its own address; leave it
too low and every client shares the proxy's allowance. An invalid value stops
startup.

Limiter state is local to one process. That is correct for the single demo
instance: several instances would each grant the full limit. Move the state to
shared storage before running more than one, and not before a measured need.

Run the disposable-schema integration suite with `make test-postgres` and an
`TOKENHOLIC_TEST_DATABASE_URL`. Do not target production. Do not add distributed
infrastructure, background queues, caches, or deployment configuration before
a measured requirement and the corresponding roadmap stage.

The optional free demo environment is documented in
[`docs/deployment.md`](../docs/deployment.md). It keeps the API stateless and
uses one external PostgreSQL database; it does not add queues, caches, or
workers.
