# Local storage

This package owns the private on-device SQLite database. It does not discover
or parse provider records, and persisted schemas must contain normalized,
privacy-safe fields only.

## SQLite driver

tokenholic pins `github.com/ncruces/go-sqlite3` v0.32.0. The project is actively
maintained, supports `database/sql`, tests macOS on both supported
architectures, and does not require CGO because it runs SQLite through wazero.
Re-evaluate the driver and Go 1.25 toolchain together rather than upgrading the
storage dependency implicitly.

The application database lives at `tokenholic/tokenholic.db` under the
operating system's user configuration directory on macOS and Linux, and under
the local application data folder (`%LOCALAPPDATA%`) on Windows. The Windows
choice is deliberate: the database and its upload state belong to one device
and must not follow a roaming profile to another. Existing databases created
under the former capitalized directory remain readable. Callers may supply another
absolute path for tests or explicit configuration.

## Lifecycle and migrations

`Open` creates the application directory and database owner-only (permission
bits on Unix, a protected single-user access-control list on Windows; see
`internal/privatefs`), enables WAL mode, foreign keys, a bounded busy timeout, and
`synchronous=NORMAL`, then applies embedded migrations. These connection-local
settings are part of the open contract; callers must not open the file through
another driver path.

Migration versions are contiguous and immutable. Their names and SHA-256
checksums are recorded in `schema_migrations`. All pending migrations run in
one immediate transaction, so a failed upgrade cannot leave a checkpoint ahead
of its schema. A newer, reordered, renamed, or edited applied migration is an
incompatible database rather than something tokenholic repairs silently.

`Diagnostics` reads a privacy-safe operational snapshot for `tokenholic doctor`:
integrity and schema compatibility, checkpoint and pending-outbox counts, the
last successful collection time, and adapter health. It never exposes database
rows, source identifiers, or filesystem paths.

## Observation commits

`CommitObservations` writes normalized observations, the matching source
checkpoint, and adapter health in one transaction. Observation uniqueness is a
SHA-256 fingerprint of source and source-record identity, so rescanning the
same evidence is a no-op. Source-root, session, and last-record identifiers are
also fingerprinted before SQL execution; the schema has no path or raw
source-identifier fields.

The one exception to the no-op is a record arriving from a different adapter
revision. Its token categories, warnings, and pricing evidence replace the
stored ones, so a corrected parser repairs history whose source files still
exist. Records whose files are gone keep their earlier values; for cache
writes that means a combined total with an unknown time-to-live, and for
pricing evidence it means none. Model attribution, measurement, and
normalization version are never replaced.

The other exception is a day total (see
[day-level sources](../normalize/README.md#day-level-sources)). Its identity
is the agent, the UTC day, and the model, and its value is the day's whole
total so far, so a later reading with different values replaces the stored
ones in place. It is one record however many times the day is read: rereading
an unchanged day changes nothing, and a day that grew holds the new value,
not the sum of the readings. The schema ties `day_total` to the day-level
sources, so a Claude or Codex row cannot be stored as one.

Pricing evidence is the service tier and the prompt size of one request (see
the [normalized contract](../normalize/README.md)). Both columns are nullable,
and NULL means the source did not prove the fact. Evidence is part of the
usage-bucket identity, so a record that gains it moves to another bucket and
the next rebuild retracts the bucket it left.

`CommitScan` extends the same invariant across every source completed by one
application scan: either all observation/checkpoint/health batches commit, or
none do.

Token categories use nullable decimal text. This preserves unavailable versus
reported zero and supports the complete `uint64` range that SQLite's signed
integer storage cannot represent. Cache writes store the combined total plus
nullable 5-minute and 1-hour lifetimes. The model columns hold the observed
identifier and how the source attributed it (`reported`, `inferred`, or
`unknown`). The canonical, provider, and pricing-key columns remain in the
schema but are no longer written.

## Absolute usage buckets

Committed observations reduce into absolute one-minute UTC buckets keyed by
source, surface, observed model, model evidence quality, measurement quality,
normalization version, and pricing evidence: the service tier and the prompt
band. The key holds observed evidence only, so a catalog update never moves a
bucket. The server prices a bucket from one rate card, which is why requests
at different tiers or on different sides of a price threshold never share one.

A day total becomes one bucket filed at the start of its UTC day and marked
`"granularity": "day"`. The mark is part of its key and its JSON. A minute
bucket has no such field, so admitting day-level sources changed no existing
bucket. Because a day total is replaced in place, its bucket is replaced the
same way every other bucket is: a new revision of the same key, holding the
day's new absolute value.

A bucket with no pricing evidence omits both fields from its key and its JSON.
It is byte-identical to a bucket built before the evidence existed, keeps its
content hash, and is not uploaded again. A test pins that key, hash, and JSON.
`internal/sync` recomputes the key from the payload and must stay in step.

Each bucket row keeps two JSON documents:

| Column | Contents | Uploaded |
| --- | --- | --- |
| `payload_json` | Observed model, evidence quality, pricing evidence, and token categories | Yes, exactly as stored |
| `preview_json` | This build's canonical model, unpriced reason, estimate, and priced/unpriced tokens | Never |

A bucket's revision changes only when `payload_json` content changes. A higher
revision with different content replaces the earlier value and is enqueued. A
bucket whose content is unchanged keeps its revision and acknowledgement and
has only its preview refreshed, so repeated refreshes upload nothing. An older
revision is ignored; the same revision with different content is rejected.

Buckets are absolute, so a rebuild also retracts. A stored bucket that the
current observations no longer produce is replaced by a known zero in every
token category at the new revision and enqueued like any other change. Without
that, usage a corrected adapter stops counting would stay in the local totals
and on the server indefinitely. A retracted bucket is kept as a zero row;
retracting it again changes nothing.

Only the newest pending revision for each bucket remains in the local outbox.
Its JSON is the upload payload and has no source record, session, path, or
client-derived model or price field. This package does not implement a network
client.

Migration `0007` adds the two nullable pricing-evidence columns to
observations. Existing rows stay NULL until their source file is read again by
an adapter revision that reports the evidence.

Migration `0006` discards stored Codex observations. Earlier Codex adapter
revisions counted replayed history and misordered snapshots, the affected rows
cannot be told apart from correct ones, and the corrected adapter uses a
different identity, so the rows are collected again from the rollout files.
Usage whose rollout file no longer exists is not recovered. The buckets built
from the old rows are replaced or retracted by the next rebuild.

Migration `0005` drops and recreates these two tables. They are derived state:
the next refresh rebuilds every bucket from stored observations and enqueues it
under its v2 key. The same migration resets each observation's model columns to
source evidence, because catalog results are no longer stored per observation.

Manual application refreshes choose the next revision by parsing every stored
decimal revision as an unsigned 64-bit value. This avoids SQLite signed-integer
coercion and fails visibly if the revision space is exhausted.

Each token category and API-equivalent estimate carries `complete`, `partial`,
or `unavailable` coverage. Period queries add `empty` when no bucket exists.
Aggregation returns an error on unsigned overflow rather than saturating or
wrapping.

Today and the current week begin at midnight in the requested IANA timezone;
weeks begin Monday. The last-30-days window includes the current local calendar
date plus the preceding 29 dates. Boundaries convert to UTC before querying,
and the end includes the current UTC minute. One-minute buckets make modern
IANA offset and daylight-saving boundaries exact.
