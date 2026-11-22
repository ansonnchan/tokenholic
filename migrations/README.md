# Migrations

This package embeds the immutable PostgreSQL schema history for the hosted API.
It remains separate from the local SQLite migrations beside `internal/store`.

PostgreSQL files use `NNNN_short_name.sql`, begin at version 1, and remain
contiguous. The server records each name and SHA-256 checksum. Never edit an
applied migration; add the next version instead. A changed checksum, changed
name, gap, or database version newer than the binary is an incompatibility.

All pending migrations run in one transaction under a PostgreSQL advisory
lock. Validate changes with `make test-postgres` and a disposable database URL.
Schema comments should explain privacy, ownership, and unusual invariants
instead of duplicating column names.

Migrations are forward-only. `0003_sync_v2_usage_buckets` is deliberately
destructive: it replaces `usage_buckets` and discards uploaded demo usage
instead of translating it, because v1 rows cannot be re-derived. Accounts,
devices, and groups are kept, and clients upload their history again after
upgrading. A destructive migration must say so in its header comment and in
the release notes.

`0004_pricing_evidence` is additive. It adds the nullable `service_tier` and
`prompt_band` columns, which are client evidence: they are covered by the
payload hash and read back when history is repriced. Existing rows keep NULL,
which means unproven. The schema checks the shape of both values and the API
validates the names, so a new tier or ladder step needs no migration.

`0005_global_board_opt_in` adds `users.global_opt_in`, false by default. An
account is listed outside its own groups only where that column, or a later
visibility column like it, is true.

`0006_profiles_and_group_visibility` adds the avatar URL, profile visibility,
and group visibility with its public slug. Every visibility column defaults
to private. A public read must filter on these columns in its lookup query.

`0007_day_level_buckets` is additive. `interval_seconds` was fixed at 60 and
now also admits 86400: a day-level bucket, the absolute total of one agent,
UTC day, and model from a source the client has no reader of its own for. A
day bucket must start at midnight UTC. Existing rows are minute buckets and
stay valid. The API validates which sources may send a day; the schema checks
only the shape. Boards count a member's day buckets from this column so they
can say the day boundary is approximate.

Do not add deployment state before its roadmap stage. Server migrations must
never introduce columns for raw local records or prohibited source material.
