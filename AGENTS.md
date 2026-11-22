# tokenholic contributor guidance

Read these documents before changing the project:

1. [ROADMAP.md](./ROADMAP.md) for scope, milestones, and delivery order.
2. [docs/architecture.md](./docs/architecture.md) for boundaries and data flow.
3. [docs/design.md](./docs/design.md) for TUI and website behavior and visual direction.

## Product boundaries

tokenholic tracks coding-agent usage from supported local evidence. It must not upload prompts, responses, source code, file paths, project names, repository names, branch names, command text, or provider credentials.

Ordinary desktop/web chat is outside the product. Claude Desktop local Code/Agent/Cowork sessions are in scope when they expose compatible local usage records.

Unknown and unavailable values are not zero. Preserve the distinction between reported, resolved, inferred, estimated, and unsupported data.

The terminal is the primary product. The website is read-only and never signs users in or accepts writes. Profiles, groups, and global-board participation are private until the user or owner opts in; public endpoints must treat private and nonexistent records identically. Uploading always requires a claimed account. Sharing uses client-side `x.com/intent/post` links, never the paid X API.

Costs are API-equivalent estimates, never spending.

## Architecture rules

- Keep source discovery, parsing, normalization, persistence, synchronization, and presentation separate.
- Keep observed model IDs separate from canonical model IDs and pricing keys.
- Model resolution and pricing are server-authoritative. Client resolution is an offline preview and must never cause an upload to be rejected.
- Vendor external price data from a pinned, checksum-verified source. Every manual price override cites an official provider URL and retrieval date; never guess a price.
- Normalize and commit local state before attempting network upload.
- Make collection and synchronization safe to repeat.
- Prefer absolute versioned buckets over additive upload commands.
- Keep provider-specific behavior inside adapters.
- Do not add distributed infrastructure without a measured need.
- Treat ccusage output as an external versioned contract. Pin it and validate it.

## Implementation style

- Use Go for the native CLI, collector, TUI, API, share pages, and share-card rendering.
- Build the static website in `web/` with plain HTML, CSS, and minimal JavaScript; no framework or build step.
- Use pnpm only for the npm distribution workspace and release tooling.
- Prefer the standard library until a dependency materially reduces complexity.
- Keep packages small and named after one responsibility.
- Define interfaces at the consuming boundary.
- Wrap errors with actionable context.
- Pass `context.Context` through I/O boundaries.
- Avoid mutable package globals.
- Use table-driven tests for parsers, normalization, and protocol behavior.
- Add a sanitized regression fixture for each source-format bug.

## Comments and documentation

- Keep code comments short.
- Explain architectural intent, privacy boundaries, unusual source semantics, and non-obvious invariants.
- Give every Go package an accurate package comment and document exported declarations with Go-style comments.
- Audit every changed source and configuration file for documentation impact; add focused comments where behavior, constraints, or failure modes would otherwise be ambiguous.
- Add README files at meaningful subsystem boundaries, not mechanically in every leaf directory. Prefer `doc.go` for a small Go package whose boundary is already clear.
- Do not restate what a function or line already says.
- Keep cross-package decisions in `docs/`, subsystem operating guidance near the code, and link to the authoritative source instead of duplicating it.
- Update architecture or design documentation when a change alters their decisions.
- Do not copy prompts, transcript text, paths, or credentials into tests, documentation, commits, or PRs.

## TUI direction

- Follow the retro-terminal direction in `docs/design.md`: one coral accent, uppercase spaced labels, titled panels with dashed rules, solid coral chips for selection and primary actions, and a highlighted current-user row.
- Keep the interface keyboard-first, dense, and aligned. The TUI paints its own canvas, light by default with a saved dark alternative; with `NO_COLOR` it paints nothing.
- Avoid neon palettes, gradients, glow, ASCII-art backgrounds, and idle animation.
- Preserve meaning without color and on narrow terminals; every glyph used for meaning needs an ASCII fallback.
- Cover layout changes with golden snapshots at the documented widths, with and without color.
- Do not let presentation own collection, synchronization, or pricing logic.

## Git workflow

- Use short-lived branches named `<type>/<short-kebab-description>`, such as `feat/local-collector`, `fix/community-invites`, or `docs/privacy-model`.
- Keep PRs focused and small enough to review as one coherent change.
- GitHub issues are optional; the roadmap is the planning source.
- Do not include roadmap or workstream IDs in branch names, PR titles, commit subjects, or trailers.
- Use conventional commit subjects: `<type>(<optional-scope>): <imperative summary>`, such as `fix(web): validate community invites`.
- Multiple commits are welcome when they separate meaningful implementation, specification, architecture, or design steps within the same focused PR.
- Use the repository's configured author for all commits.
- Do not add `Co-authored-by`, generated-by, or agent-attribution trailers.
- Keep commit subjects concise and avoid noisy checkpoint commits.
- Agents may push, open, and monitor PRs, but must not merge them without an explicit user request for that PR.
- Leave completed PRs open and report their URLs, check status, dependencies, and review notes to the user.
- When sequential work must continue before review, use stacked PRs: branch from the preceding unmerged branch and set that branch as the PR base.
- Complete, validate, commit, and push each stacked branch, then open its PR before starting work on the next child branch.
- Opening a stacked PR does not authorize merging it; leave every PR open for review unless the user explicitly requests that PR's merge.
- Keep each stacked PR limited to its incremental diff and state its parent PR dependency clearly.
- After the user merges a parent PR, rebase or retarget its child PR as needed and wait for the user to merge it.
- When merging is explicitly requested, prefer rebase merge to retain meaningful commits in a linear history and delete the remote branch afterward.
- Never merge a draft PR, a PR with failing checks, or a PR with unresolved review feedback, even when a general merge request exists.
- Direct commits to `main` are reserved for small repository-policy or documentation adjustments explicitly requested by the user.
- Do not rewrite or discard unrelated user changes.
- Run relevant formatting, tests, and `git diff --check` before committing.

## Review checklist

- Does repeated collection or sync preserve totals?
- Are missing capabilities visible instead of reported as zero?
- Can the payload contain private source material?
- Are model aliases and inferred mappings labelled honestly?
- Can a public endpoint reveal a private user, group, or opt-out?
- Does the change work without color and at the documented minimum width?
- Is the behavior covered by unit tests or sanitized fixtures?
- Did the change add complexity that belongs in a later milestone?
