# Collection semantics

The `collect` package owns the application scan boundary. Its orchestrator asks
the discovery and storage interfaces for work, while the collector opens
supported JSONL containers, delegates provider parsing to adapters, deduplicates
raw observations, converts cumulative counters to increments, and produces
deterministic normalized state.

Collection coordinates discovery and persistence through consuming-boundary
interfaces. It does not define provider roots, SQL behavior, uploads, or UI.

## Processing order

```text
candidate files
    -> source parser
    -> raw normalized observations
    -> deduplicate source identities
    -> reduce cumulative snapshots per session
    -> deterministic observations and warnings
    -> atomic observations + checkpoints + adapter health
```

Deduplication must happen before cumulative reduction. Reducing two overlapping copies first would turn the same snapshot stream into separate deltas and inflate totals.

## Day-level agents

Agents with no native adapter are read by `DayLevel`, after the native
sources and inside the same scan transaction. It shares nothing with the path
above: no candidate files, no deduplication, no counter reduction. For each
agent it checks whether the record folder exists, runs the pinned ccusage
report only if it does, and returns the agent's health and day totals.

A day total is not deduplicated because it has nothing to collapse with: its
identity is the agent, the day, and the model, and storage replaces a
differing value in place. An agent that could not be read returns no
observations, which leaves what was stored untouched while its health says
why. Each agent is read on its own, so one failure degrades one source.

Because the batches are built separately, nothing a day-level agent reports
can reach a Claude or Codex record. A test scans the native fixtures with the
reader present, absent, failing, refused, and reporting, and requires the
native observations, buckets, upload bytes, and totals to be identical.

## Duplicate conflicts

The source plus stable source-record identifier forms the duplicate key. Exact duplicates collapse silently.

Copies that agree on model, usage, and measurement but were recorded at different times are the same event read twice, for example a Codex fork replaying its parent's history. They are not a conflict. The earliest copy wins, whatever order the files were read in, because the event happened when it was first recorded.

Copies that disagree on usage emit `duplicate_conflict` and use a deterministic winner selected by:

1. number of known token categories;
2. non-overlapping token magnitude;
3. later source timestamp;
4. stable surface precedence;
5. a privacy-safe signature containing only normalized metadata.

The final tie-breaker never includes raw record content or a source path.

## Scan transactions and cancellation

All source batches from one scan commit in a single store transaction. A
partial parser failure degrades only the matching source health and does not
discard valid observations from other sources. If cancellation arrives after
discovery, already-normalized state is committed through a bounded detached
context before `Scan` returns the cancellation; this prevents a retry from
losing durable progress or inflating totals. The canceled state is recorded in
adapter health and returned to the caller.

## Cumulative counters

Only sources without per-request usage emit cumulative snapshots; current Codex rollouts report each request directly and skip this step.

Cumulative state is isolated by source and local session identity. If a session identifier is unavailable, the adapter supplies a logical-file identity so unrelated files do not share state.

Snapshots are reduced in time order. Snapshots that share a timestamp are ordered by ascending magnitude, which is their source order because a running total only grows. Ordering them by record identity instead would make rising totals look like resets and count each one in full.

A decreased known counter starts a new segment and emits `counter_reset`; the current snapshot becomes the first increment in that segment. A missing category remains unknown for that observation, but the reducer remembers the last known value so a later reappearance is compared with the correct snapshot.
