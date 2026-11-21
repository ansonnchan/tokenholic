# Internal packages

The packages under `internal` form the native application's private implementation. Their boundaries follow the local data flow, so source-specific behavior cannot leak into persistence, synchronization, or presentation.

```text
local evidence
    -> adapters
    -> normalize
    -> collect
    -> store
    -> application
    -> TUI

doctor reads discovery, parser, and storage health without collecting records
fixturecheck protects committed test data independently of runtime collection
account owns hosted login and OS credential-vault storage beside the local flow
multiplayer owns authenticated group and leaderboard HTTP calls
```

## Package ownership

| Package | Owns | Does not own |
| --- | --- | --- |
| `account` | tokenholic API login in one step or two, credential rotation/revocation, and OS vault storage | GitHub tokens, source collection, or server persistence |
| `adapters` | Source discovery, source formats, and external parser contracts | Cross-source totals, persistence, or UI |
| `normalize` | Provider-neutral records, capability vocabulary, and validation | Filesystem discovery or provider parsing |
| `collect` | Application scan orchestration, deduplication, and cumulative reduction | SQL implementation or uploads |
| `application` | Committed dashboard queries and manual refresh sequencing | Source parsing, SQL, or terminal rendering |
| `autosync` | Opt-in macOS launch agent lifecycle and cross-command synchronization lock | Collection, uploads, credentials, or deployment |
| `doctor` | Redacted source, parser, and storage diagnostics | Usage collection or repair |
| `fixturecheck` | Privacy validation for committed fixtures | Runtime validation of user data |
| `modelcatalog` | Versioned canonical model and API-equivalent pricing resolution | Source parsing |
| `multiplayer` | Native group and global-board commands, leaderboard DTOs, and authenticated API calls | Credentials, server policy, aggregation, ranking, or terminal rendering |
| `numfmt` | Compact token counts, estimate amounts, and shares as every surface displays them | Deciding how an unknown or unavailable value is shown |
| `share` | Compose links for a user's own post, and the draft text | Sending anything, or calling a social network's API |
| `store` | SQLite lifecycle, migrations, and local persistence | Source-specific behavior |
| `sync` | Allowlisted absolute-bucket batches, retry, dry run, and acknowledgements | Raw local records or provider credentials |
| `tui` | Keyboard-first presentation of committed local state | Collection, SQL, or synchronization logic |
| `tui/theme` | Palette, text styles, and the glyph set with its ASCII fallbacks | Layout or content |
| `tui/widget` | Stateless building blocks: chips, panels, rules, tables, sparklines, movement marks, key bar | Usage, ranking, or availability semantics |

Interfaces should be declared at the package that consumes them. Provider-specific identifiers may pass through normalized contracts as observed values, but provider-specific filesystem and schema rules stay in adapters.

## Documentation convention

Every package has a package comment describing its boundary. Complex subsystems may also have their own README when developers need a map before reading individual files. Implementation comments document privacy constraints, source semantics, and invariants that are not clear from types and control flow; they should not narrate ordinary Go syntax.
