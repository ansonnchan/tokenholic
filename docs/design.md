# Design direction

Status: MVP views implemented; retro-terminal revamp (M8) and website (M9) accepted
Last updated: November 20, 2026

## Intent

tokenholic should feel like a polished retro terminal: dark, dense, precise,
and a little playful, with one warm coral accent doing the work that
decoration would otherwise do. It is a metrics application with a scoreboard
attitude, not a simulated hacker console.

The supplied reference is a coral-on-navy, pixel-type leaderboard dashboard.
tokenholic follows it closely in hierarchy and mood: uppercase spaced labels,
titled panels with dashed rules, solid coral chips for selection and primary
actions, a highlighted current-user row, a standing rail, and ▲▼ rank
movement. tokenholic keeps its own name, wordmark, copy, and assets; nothing
is copied from the reference product's code, text, or images.

Presentation begins after local persistence and model resolution. The TUI and
website consume presentation-ready snapshots and never own discovery,
collection, persistence, synchronization, or pricing. `tokenholic doctor`
remains plain diagnostic output.

## Visual language

### Principles

- One coral accent for selection, primary actions, the current user, and the
  wordmark caret. Everything else is foreground, muted gray, or a semantic
  status color.
- Uppercase, letter-spaced labels for navigation, chips, panel titles, and
  table headers. Values use normal case and tabular alignment.
- Rounded titled panels with a thin muted border; section labels followed by
  a dashed rule that fills the panel width.
- Selection is shown by a solid coral chip, never by color alone: selected
  items are also bracketed in no-color mode.
- The TUI uses the terminal's own background; it never paints the full
  viewport. The website is dark-only.
- No neon palettes, gradients, glow, ASCII-art backgrounds, or idle animation.

### Palette

Exact values are tuned in the design-system PR and must keep readable contrast
on dark and light terminal backgrounds through adaptive colors.

| Role | Direction |
| --- | --- |
| Accent | Coral/terracotta (about `#D9805F`; 256-color fallback 209) |
| Accent text on chip | Near-black on coral |
| Primary text | Terminal foreground / off-white on the web |
| Muted text and rules | Neutral gray |
| Panel border | Dim gray |
| Rank 1–3 | Muted gold, silver, bronze |
| Up / positive | Muted teal |
| Down / negative | Coral-red |
| Warning / error | Muted amber / restrained red |
| Web background | Deep navy with slightly lighter panels |

### Wordmark

`>_ TOKENHOLIC` in bold, with the `>_` caret in the accent. Wide Home views may
show a three-line block-letter version; it always falls back to the one-line
mark on narrow terminals and without Unicode.

## Number and money formatting

- Token counts are compact with at most three significant digits and trailing
  zeros removed: `987`, `1K`, `111K`, `1.35M`, `2.1B`, `4.27T`. Rounding never
  produces `1000K`; it promotes to the next unit (`999,950` → `1M`).
- Exact counts remain available in category detail and `show-data`.
- Money uses two decimals with grouping: `$1,180.00`. Non-zero values below
  one cent display `<$0.01`.
- Every cost carries an `EST` label or the metric chip `$ EST`. Never write
  "spent".
- Lower bounds display `≥$640.00` with the priced share (`92% priced`).
  Unavailable values display `—` (ASCII `-`) with a reason, never `$0.00`.

## TUI information architecture

| Key | Area | Purpose |
| --- | --- | --- |
| `1` | HOME | Personal headline totals, sparkline, breakdowns, freshness, claim prompt |
| `2` | BOARDS | Group and global standings, standing rail, group panel |
| `3` | MODELS | Per-model tokens, estimate, attribution quality, category detail |
| `4` | SOURCES | Coding-agent coverage and adapter health |
| `5` | SETTINGS | Account, devices, visibility, global opt-in, autosync, privacy, help |

### HOME

- Period chips: `TODAY | THIS WEEK | 30 DAYS | ALL-TIME`.
- Headline: total tokens and the API-equivalent estimate for the period.
- A 30-day sparkline of daily tokens (`▁▂▃▄▅▆▇█`, ASCII fallback `_.-=#`).
- Breakdown by source and by model, each with tokens, estimate, and share.
- Category detail on demand: uncached input, cache read, cache write 5m and
  1h, output, and reasoning (shown as part of output).
- Freshness (`synced 4m ago`, `3 buckets pending`) and coverage warnings.
- When signed out, a primary chip: `[ c  CLAIM YOUR SPOT ]`.

### BOARDS

Wide layout (120 columns or more):

```text
>_ TOKENHOLIC   HOME  [BOARDS]  MODELS  SOURCES  SETTINGS              synced 4m ago

╭─ NIGHT-SHIFT ──────────────────────────────────────╮ ╭─ YOUR STANDING ──────────╮
│  TODAY [THIS WEEK] 30 DAYS ALL-TIME   [$ EST] TOKENS │ │ @you            RANK #5  │
│                                                      │ │ $640.00 EST        ▲ 2   │
│ STANDINGS - - - - - - - - - - - - -  ▲▼ VS LAST WEEK │ ╰──────────────────────────╯
│   1  @dana                 $1,180.00          ▲ 1    │ ╭─ NIGHT-SHIFT ────────────╮
│   2  @kai                  $1,090.00          ▼ 1    │ │ MEMBERS               12 │
│   3  @rio                    $840.50          –      │ │ INVITE  Q7M2KX   y copy  │
│ ▌ 5  @you  ★ YOU             $640.00          ▲ 2    │ │ RESETS MON 00:00 LOCAL   │
│   6  @sam                ≥$560.75  71% priced        │ ╰──────────────────────────╯
│                                                      │  [ s  SHARE ]
│ 1–6 OF 12 · RESETS MON 00:00 America/Vancouver       │
╰──────────────────────────────────────────────────────╯
1–5 views · g scope · p period · m metric · j/k move · y copy · s share · ? help · q quit
```

- Scope switcher cycles the user's groups and `GLOBAL` (opt-in).
- Metric chips: `$ EST | TOKENS`.
- Trend legend names its comparison: `VS YESTERDAY`, `VS LAST WEEK`,
  `VS PRIOR 30 DAYS`, or `VS 24H AGO`. Comparisons use the previous period
  at the same elapsed time; help text explains this.
- Movement: `▲ 2`, `▼ 1`, `–`, `NEW`; ASCII fallback `+2`, `-1`, `=`, `new`.
- The current-user row has an accent edge (`▌`), a `★ YOU` tag, a subtle
  adaptive background, and accent text. If it falls outside the visible page,
  it is pinned after the list.
- Rows below the cost-coverage threshold show a lower bound and coverage
  without a rank. The global board shows values without ranks until three
  members can be ranked; a private group ranks any number of members.
- The group panel shows members, the invite code (owners), the reset
  boundary, and copy via OSC 52 where supported, otherwise prints the code.

From 88 to 119 columns the rail stacks below the standings. From 48 to 87
columns the view is one column: chips wrap, the trend column shortens, and
the rail collapses to one summary line.

### Onboarding

The first launch scans with a single spinner, then reveals the headline:

```text
>_ TOKENHOLIC

SCANNING  claude code ✓   claude desktop ✓   codex ✓

YOU BURNED 4.2B TOKENS · ~$1,180.00 EST · LAST 30 DAYS

[ c  CLAIM YOUR SPOT ]    r refresh · 1–5 views · q quit
```

Claiming runs the GitHub device flow inside the TUI: it shows the user code,
offers to open the verification page, and returns to BOARDS after the first
sync. Nothing uploads before the user claims.

### Primary actions

Primary actions are coral chips that include their key: `[ c  CLAIM YOUR
SPOT ]`, `[ y  COPY INVITE ]`, `[ s  SHARE ]`. At most one primary chip is
visible per panel. Destructive actions require an inline typed confirmation.

## Responsive rules

- Minimum width is 48 columns. Below it, show a resize instruction with help
  and quit still available.
- Remove decorative columns and letter spacing before truncating labels or
  values. Abbreviate consistently.
- Never scroll horizontally in a primary view.

## Interaction

| Key | Action |
| --- | --- |
| `1`–`5` | Switch area |
| `tab` / `shift+tab` | Move between controls |
| `j` / `k`, arrows | Move selection or scroll |
| `p` / `m` / `g` | Period / metric / scope |
| `c` | Claim when signed out; create group in BOARDS |
| `i` | Join with an invite code (input masked) |
| `y` | Copy invite |
| `s` | Share (prints an `x.com/intent/post` link) |
| `r` | Refresh |
| `?` | Help |
| `q` | Quit |

Never block navigation on background work. Keep the last stable data visible
while refreshing.

## Motion and feedback

- One small spinner only during collection, sync, or network loads.
- No continuous animation while idle.
- Show success briefly, then return to freshness text.

## Content style

- Short uppercase labels; plain-language messages.
- `synced 4m ago`, not protocol terms.
- Name what failed before the technical cause: `Could not read Codex sessions`.
- Label inferred, partial, and unavailable values explicitly.

## Website

The website (M9) shares the TUI's tokens and voice and stays read-only.

- Pages: landing (copy-able `npx @tokenholic/cli`, what is collected and
  what never leaves the machine), public profile, public board, and invite
  landing.
- Dark-only navy background, the coral accent, solid coral primary buttons,
  chip tabs, titled panels with dashed rules, rank tables with GitHub
  avatars, the standing rail, and `★ YOU` when viewing one's own public page.
- Type: an OFL-licensed pixel display face for the wordmark and titles and an
  OFL-licensed monospace for everything else, self-hosted.
- While the API wakes, show an explicit loading state rather than empty data.
- Share cards (1200×630 PNG) use the same palette: wordmark, scope, the
  headline value with `EST`, rank, and period.
- Accessibility: WCAG AA contrast, visible focus rings, reduced-motion
  support, and layouts that work at phone width.

## Accessibility

- Support `NO_COLOR`: brackets mark selection, text labels carry status.
- Test light and dark terminal backgrounds.
- Do not use color alone for rank movement, coverage, or errors.
- Provide ASCII fallbacks for every Unicode glyph used for meaning.

## Non-goals

- Images or terminal image protocols in the TUI (avatars are web-only).
- Mouse-first interaction.
- Theme customization before the default experience is stable.
- A web dashboard that duplicates TUI functionality or accepts writes.
