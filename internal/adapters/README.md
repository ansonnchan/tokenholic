# Source adapters

Adapters are the only packages that understand provider-owned storage layouts and record formats. They discover narrowly scoped evidence, parse compatible records, and emit normalized observations. They do not aggregate leaderboards, persist state, or upload data.

## Discovery boundary

`DefaultMacRoots` lists the currently supported macOS evidence roots:

- Claude Code CLI project-session JSONL;
- Claude Desktop Code sessions;
- Claude Desktop local Agent records under the `rpm` subtree;
- Claude Desktop Cowork `audit.jsonl` records outside that subtree;
- Codex session rollouts, represented as shared local storage when CLI and Desktop cannot be distinguished. Each Codex home is read from `sessions` and, when it holds rollouts, from `archived_sessions`, where Codex moves a rollout the user archives. No other file in the Codex home is opened.

A root marked optional is a secondary location of a source. It adds records when it has any and reports nothing when it is absent or empty, so a missing archive never degrades a healthy source. An optional root that exists but cannot be used is still reported.

These roots are intentionally narrow. Ordinary Claude Desktop chat and general web/desktop chat storage are out of scope. Discovery is read-only, refuses root symlinks, skips nested symlinks and non-regular files, and records health through stable warning codes.

Candidate paths are local implementation details. They are needed to open a file but must not appear in diagnostics, normalized observations, logs, fixtures, or uploads. Relative paths may be used locally to separate otherwise anonymous files, but are never public metadata.

## Native JSONL parsers

The Claude parser consumes assistant usage records. Claude usage is incremental, and a model directly present in the message is `reported`. Request and message identifiers are hashed before becoming normalized record IDs.

Two kinds of assistant line are not usage and are skipped without a warning: lines whose model is `<synthetic>`, which Claude Code writes itself for interruptions and similar notices, and lines flagged `isApiErrorMessage`. Neither reached a model, so counting them would add a phantom model and unbillable tokens.

Cache writes come from two fields. `cache_creation_input_tokens` is the combined total, and the optional `cache_creation` object splits it into 5-minute and 1-hour lifetimes:

| Record reports | Normalized total | Lifetimes |
| --- | --- | --- |
| Combined and a split that fits within it | Combined | As reported; any remainder has an unknown lifetime |
| Combined only | Combined | Unknown, unless the total is zero |
| Split only | Sum of the reported lifetimes | As reported |
| Split larger than combined | Combined | Unknown, with a `cache_write_split_inconsistent` warning |

An unknown lifetime stays nil; it is never assumed to be the 5-minute rate here. Pricing decides how to treat it and marks the estimate partial.

Each Claude record also says how the request was processed, in `usage.service_tier` and `usage.speed`. Together they give the normalized service tier:

| `service_tier` | `speed` | Normalized tier |
| --- | --- | --- |
| `standard` | `standard` or absent | `standard` |
| `standard` | `fast` | `fast` |
| `priority` | `standard` or absent | `priority` |
| `batch` | `standard` or absent | `batch` |
| anything else, or absent | anything | unknown |

`speed` is a later addition that marks fast mode, so a record without it predates fast mode and is taken at its `service_tier`. A value the adapter does not know proves nothing and stays unknown.

The request's prompt size is uncached input plus cache reads plus cache writes. It is recorded only when the record reports all three, because a missing counter would understate it.

The Codex parser follows `turn_context` records to attribute later token events. That attribution is `inferred`, because the token event does not directly carry the active model.

A Codex `token_count` event carries two counters: `last_token_usage`, the request that just finished, and `total_token_usage`, the session's running total. The parser emits the request usage as one incremental observation and uses the running total only to identify it:

| Situation | Handling |
| --- | --- |
| Normal request | One observation. Its identity is a hash of the session, the active turn, and the running total after it |
| Event repeats the previous running total | Skipped. Codex re-emits the last event, sometimes under a new turn; it reports no new usage |
| Forked or resumed rollout | The file starts with its own `session_meta`, then its parent's, then the parent's whole history rewritten under one timestamp. Each replayed request has the same session, turn, and running total as its original, so it gets the same identity and deduplication keeps one. The earlier copy supplies the time |
| Fork continues after the replay | Its own requests carry new turns and new running totals, so they stay distinct from what the parent does next |
| Parent file no longer exists | The replay is then the only record of those requests. They still count once, at the replay time |
| No `last_token_usage` (older rollouts) | Emitted as a cumulative snapshot and reduced by `internal/collect` after cross-root deduplication |

Codex records the service tier in `event_msg` records whose payload type is `thread_settings_applied`, under `thread_settings.service_tier`. The parser reads that one member and decodes nothing else in the settings object, which also describes the user's workspace. The tier in effect applies to every request until the next settings event:

| Settings event | Tier afterwards |
| --- | --- |
| `priority`, or the earlier spelling `fast` | `priority` |
| `flex` | `flex` |
| `default`, or `standard` as Codex Desktop writes it | `standard` |
| No `service_tier` member | Unchanged: the previous tier stays in effect |
| Any other value, including an explicit `null` | Unknown: the previous tier no longer applies |
| No settings event yet | Unknown |

Codex writes no settings event before a thread's first turn, so that turn's requests have no provable tier. The Codex configuration file is never read to fill the gap.

Each request's prompt size is `last_token_usage.input_tokens`, which already includes cached input. Only per-request usage carries a tier or a prompt size; a legacy running total spans many requests and carries neither.

Neither the event timestamp nor the file takes part in a request's identity, because a replay rewrites the first and changes the second. Only the first `session_meta` in a file names its session; a fork's second header describes the parent and must not rename it.

Reducing the running total instead would count every replayed history again, once per fork, and is why the parser does not do it when request usage is present.

Both parsers recover after malformed complete lines and defer an unterminated final line as a partial write. They keep missing token categories nil rather than inventing zeroes.

## ccusage compatibility contract

The `ccusage` package validates the release manifest and supports the optional
version probe shown by `tokenholic doctor`. Native collection does not execute
ccusage. Synthetic fixtures preserve the reviewed external JSON contract for a
future adapter without shipping an unwired collection path. See
[ccusage/README.md](./ccusage/README.md).

## Adding a source format

1. Add a sanitized synthetic fixture before parser logic.
2. Extend fixture privacy validation if the format introduces new structural keys.
3. Keep discovery separate from parsing.
4. Define stable source identity and counter semantics explicitly.
5. Preserve unknown model and token values.
6. Add malformed, partial-write, duplicate, and repeatability tests where applicable.
