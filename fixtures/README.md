# Compatibility fixtures

This directory contains synthetic examples of supported local source behavior. Fixtures make parser, counter, deduplication, recovery, and external-contract changes reproducible without reading a contributor's real agent logs.

Only synthetic or explicitly approved sanitized source records belong here. Fixtures must not contain prompts, responses, source code, tool payloads, command text, credentials, identifying paths, project names, repository metadata, branch names, or raw transcripts.

## Layout

| Directory | Contract exercised |
| --- | --- |
| `claude-code` | Claude Code incremental assistant usage, duplicates, malformed-line recovery, cache-write lifetimes, service tier and speed, and excluded local notices |
| `claude-desktop/code` | Claude Desktop Code records and overlap with CLI-compatible evidence |
| `claude-desktop/agent` | Claude Desktop local Agent records |
| `claude-desktop/cowork` | Claude Desktop Cowork audit records |
| `codex-cli` | Codex context attribution, per-request usage, service-tier settings events, prompt sizes on each side of the price ladder, a forked rollout replaying its parent, legacy cumulative snapshots, resets, and malformed-line recovery |
| `codex-desktop` | Codex Desktop/shared records and full or midstream overlap |
| `ccusage` | The pinned ccusage release's daily report for Copilot CLI, Gemini CLI, Grok Build CLI, OpenCode, and Qwen Code, produced from invented records, plus its two empty shapes. The Claude and Codex files are reference shapes tokenholic never requests |

Model names, session IDs, dates, and token counts are synthetic test values. They are examples of record shape and semantics, not a list of models supported by tokenholic.

## Privacy validation

`internal/fixturecheck` validates every file during `go test ./...`. It performs a raw scan before JSON decoding so intentionally malformed files cannot hide prohibited keys or secret/path patterns. Valid JSON fields must also pass a structural allowlist; dynamic Codex model-map keys must explicitly contain `synthetic`.

Do not weaken the validator to admit a copied production record. If a new source field is necessary, reduce the example to the smallest synthetic structure, add the field deliberately to the allowlist, and add a test proving that nearby private fields are still rejected.

## Adding a regression fixture

1. Construct the record manually with synthetic identifiers and timestamps.
2. Retain only fields required to reproduce the source behavior.
3. Name malformed or partial-write cases accordingly so syntax failure is intentional.
4. Add the parser or collection assertion that consumes the fixture.
5. Run `go test ./internal/fixturecheck` and then `make check`.

Never generate fixtures by copying a real home-directory record and redacting it afterward. Unknown embedded fields are too easy to miss.
