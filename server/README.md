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

`leaderboard` defines timezone-relative periods, ranking metrics, explicit
coverage, and competition ties. Its PostgreSQL repository authorizes active
membership and aggregates active devices in one repeatable-read snapshot.
Partial values remain visible but unranked; freshness and model/price
limitations travel with every row.

The schema contains users, stable GitHub identities, independently revocable
devices and hashed credentials, pricing revisions, privacy-limited absolute
usage buckets, private groups, memberships, and hashed invites. Bucket identity
is unique per device.

The server must never accept prompts, responses, source code, command text,
local paths, project or repository metadata, provider credentials, or raw
source records. Local discovery, provider parsing, cumulative-counter
reduction, and raw-record deduplication remain native-client responsibilities.

The API requires `DATABASE_URL`, `GITHUB_CLIENT_ID`, and a dedicated 32-byte
base64 `TOKENHOLIC_AUTH_STATE_KEY`. `TOKENHOLIC_LISTEN_ADDR` defaults to
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

Run the disposable-schema integration suite with `make test-postgres` and an
`TOKENHOLIC_TEST_DATABASE_URL`. Do not target production. Do not add distributed
infrastructure, background queues, caches, or deployment configuration before
a measured requirement and the corresponding roadmap stage.

The optional free demo environment is documented in
[`docs/deployment.md`](../docs/deployment.md). It keeps the API stateless and
uses one external PostgreSQL database; it does not add queues, caches, or
workers.
