# Design direction

Status: TUI revamp built (M8); website and share cards built (M9)
Last updated: November 21, 2026

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
- Uppercase labels for navigation, chips, panel titles, and table headers.
  Values use normal case and tabular alignment. The website also tracks
  labels out; the TUI does not, because spacing letters in a cell grid
  doubles their width and reads as separate characters.
- Rounded titled panels with a thin muted border; section labels followed by
  a dashed rule that fills the panel width.
- Selection is shown by a solid coral chip, never by color alone: without
  color the same cells hold brackets, so `[BOARDS]` replaces the chip and
  nothing moves when selection or color changes.
- The TUI uses the terminal's own background; it never paints the full
  viewport. The website is dark-only.
- No neon palettes, gradients, glow, ASCII-art backgrounds, or idle animation.

### Palette

The TUI never paints the viewport, so every color has a value for dark and
for light terminal backgrounds. Color is the default. At startup the TUI asks
the terminal for its background color and uses the matching column; a
terminal that does not answer is treated as dark. `TOKENHOLIC_THEME=light` or
`dark` overrides a wrong guess. The choice is made once per launch.
Each degrades to the 256-color value shown, then to the nearest of the 16
ANSI colors. `internal/tui/theme` is the source of these values; the website
and share cards use the dark column.

| Role | Dark background | Light background |
| --- | --- | --- |
| Accent (coral) | `#D9805F` (209) | `#B4502F` (166) |
| Text on an accent chip | `#1B1210` (233) | `#FFFFFF` (231) |
| Primary text | Terminal foreground | Terminal foreground |
| Muted text | `#8D929B` (245) | `#6A6F78` (242) |
| Panel border and rules | `#50555E` (240) | `#B4B8C0` (250) |
| Current-user row tint | `#33231D` (236) | `#F8E6DE` (224) |
| Rank 1 / 2 / 3 | `#D9B45B` / `#BCC2CB` / `#C08253` | `#8A6510` / `#646B76` / `#93532A` |
| Up / positive | `#62B8A5` (73) | `#1F7A68` (30) |
| Down / negative | `#E36A62` (167) | `#B3362E` (124) |
| Warning | `#D9A64E` (179) | `#8A5F00` (94) |
| Error | `#D95C53` (167) | `#A82A22` (124) |
| Web page / panel / text | `#0B1120` / `#111A2E` / `#E8EAF0` | — |

Bold marks titles, section labels, headline values, and anything in the
accent. With `NO_COLOR` the TUI emits no escape sequences at all, bold
included.

### Wordmark

`>_ TOKENHOLIC` in bold, with the `>_` caret in the accent. Wide Home views may
show a three-line version drawn in half-block characters; it always falls
back to the one-line mark on narrow terminals and without Unicode.

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
- Shares are cut to one decimal, never rounded up, so a share just under a
  threshold cannot display as having reached it: 94.99% is `94.9%`. A
  non-zero share below that displays `<0.1%`.

## TUI information architecture

| Key | Area | Purpose |
| --- | --- | --- |
| `1` | HOME | Personal headline totals, sparkline, breakdowns, freshness, claim prompt |
| `2` | BOARDS | Group and global standings, standing rail, group panel |
| `3` | MODELS | Per-model tokens, estimate, attribution quality, category detail |
| `4` | SOURCES | Coding-agent coverage and adapter health |
| `5` | SETTINGS | Account, devices, visibility, global opt-in, autosync, privacy, help |

### HOME

```text
>_ TOKENHOLIC   [HOME] BOARDS  MODELS  SOURCES  SETTINGS        scanned 4m ago · 3 to upload

╭─ YOUR USAGE ─────────────────────────────────────╮ ╭─ BY SOURCE ───────────────────────╮
│  TODAY  THIS WEEK [30 DAYS] ALL-TIME             │ │ SOURCE    TOKENS      $ EST  SHARE │
│                                                  │ │ Claude     2.92B    $820.40  68.4% │
│ TOKENS          $ EST            PRICED          │ │ Codex     ≥1.34B   ≥$360.02  31.5% │
│ ≥4.26B          ≥$1,180.42       92.3%           │ ╰───────────────────────────────────╯
│                                                  │ ╭─ BY MODEL ────────────────────────╮
│ DAILY TOKENS - - - - - - - -  PEAK ≥338M OCT 27  │ │ MODEL     TOKENS      $ EST  SHARE │
│ ▆▆▂▂▂▂▄▄██▆▆▃▃▇▇▁▁▃▃▆▆▄▄██▅▅  ▅▅▃▃▇▇▄▄██▆▆▂▂     │ │ …                                  │
│ OCT 23                              TODAY        │ │ 2 more models · press 3            │
╰──────────────────────────────────────────────────╯ ╰───────────────────────────────────╯
1–5 views · p period · d detail · j/k scroll · r refresh · ? help · q quit
```

- Period chips: `TODAY | THIS WEEK | 30 DAYS | ALL-TIME`, opening on
  30 days. The period is shared with MODELS and SOURCES.
- Headline tiles: total tokens, the API-equivalent estimate under a `$ EST`
  label, and the share of tokens the estimate covers. A period with no usage
  shows `—` in each tile and says so; it never shows `0`.
- A sparkline of the last 30 local days of total tokens (`▁▂▃▄▅▆▇█`, ASCII
  fallback `_.-=#`), two cells a day where there is room, with the peak day
  named. A day with no usage is the lowest bar. A day whose records carry no
  counts is a gap, because its total is unknown.
- Breakdown by source and by the five largest models, each with tokens,
  estimate, and share of the period's tokens.
- `d` adds the exact categories: uncached input, cache read, cache write
  with its 5-minute and 1-hour parts, and output with its reasoning part.
  Parts are indented under their parent and have no share of their own. A
  category a source never reports reads `not reported`.
- The header shows freshness of the local scan (`scanned 4m ago`) and, when
  wide, how many buckets wait to upload. It says `scanned`, not `synced`:
  this device does not record when it last uploaded.
- When signed out, a primary chip under the panels: `[ c  CLAIM YOUR SPOT ]`.

At 120 columns the breakdowns sit in a 50-column rail beside the usage
panel. Below that they stack under it.

### MODELS, SOURCES, and SETTINGS

- MODELS lists every model in the period with a cursor (`j`/`k`). Beside it,
  or below it when narrow, the selected model shows the identifier the source
  recorded, how it was attributed, the priced share, why any usage is
  unpriced, and its exact categories. The attribution column is the first to
  go when the list is narrow; the detail panel always states it. Inferred and
  unknown attributions are named as such.
- SOURCES shows each adapter's state in words (`detected`, `not found`,
  `no permission`, `no records`), its record count, and when it was last
  read, then the period's usage by source and by surface. It never shows a
  path.
- SETTINGS states what is stored, what never is, what uploading sends, this
  device's timezone, scan and database state, and the display switches.
- A view taller than the terminal scrolls with `j`/`k` and says how many
  lines are above and below. MODELS instead keeps its list short enough to
  fit and drops the detail panel's categories before it drops rows.

### BOARDS

Wide layout (120 columns or more):

```text
>_ TOKENHOLIC    HOME [BOARDS] MODELS  SOURCES  SETTINGS              scanned 4m ago

╭─ NIGHT-SHIFT ─────────────────────────────────────────╮ ╭─ YOUR STANDING ──────────╮
│  TODAY [THIS WEEK] 30 DAYS  ALL-TIME   [$ EST] TOKENS  │ │ @you             RANK #5 │
│                                                        │ │ $640.00 EST          ▲ 2 │
│ STANDINGS - - - - - - - - - - - - - -  ▲▼ VS LAST WEEK │ │ LAST WEEK        $512.10 │
│    1  @dana                   $1,180.00  ▲ 1           │ ╰──────────────────────────╯
│    2  @kai                    $1,090.00  ▼ 1           │ ╭─ GROUP ──────────────────╮
│    3  @rio                      $840.50  –             │ │ SCOPE    1 of 3   g next │
│ ▸  5  @you  ★ YOU               $640.00  ▲ 2           │ │ MEMBERS  8               │
│    —  @sam                     ≥$560.75  71% priced    │ │ ROLE     owner           │
│                                                        │ │ INVITE            y copy │
│ 1–5 OF 8 · RESETS MON 00:00 America/Vancouver          │ │ ovt_…                    │
│                                                        │ ╰──────────────────────────╯
│ @YOU - - - - - - - - - - - - - - - - - - - - - - - - - │
│ Rank #5, was #7 last week · $640.00 EST · last week …  │
│ mostly claude-opus-5-5 (resolved) · Claude 1.92B, …    │
╰────────────────────────────────────────────────────────╯
1–5 views · g scope · p period · m metric · j/k move · y invite · c create · i join · ? help · q quit
```

- The board is rendered exactly as the server ranked it. The TUI never
  orders rows, assigns a rank, or judges coverage.
- `g` cycles the user's groups and then `GLOBAL`. The panel title names the
  scope. An account in no group can still open `GLOBAL`.
- The board opens on `THIS WEEK` and `$ EST`.
- Metric chips: `$ EST | TOKENS`.
- Trend legend names its comparison: `VS YESTERDAY`, `VS LAST WEEK`,
  `VS PRIOR 30 DAYS`, or `VS 24H AGO`. Comparisons use the previous period
  at the same elapsed time; help text explains this.
- Movement: `▲ 2`, `▼ 1`, `–`, `NEW`; ASCII fallback `+2`, `-1`, `=`, `new`.
- The current-user row has an accent edge (`▌`), a `★ YOU` tag, a subtle
  adaptive background, and accent text. If it falls outside the visible page,
  it is pinned after the list.
- `j`/`k` move a cursor (`▸`, bold, no tint; the tint belongs to the
  viewer's row). The cursor starts on the viewer's row and the list scrolls
  around it, with as many rows as the terminal's height leaves.
- Under the standings, the row under the cursor is explained in words: its
  rank and previous rank, its value in the comparison window, the server's
  reason when it has no rank, its most-used model with attribution, its
  source mix, when it last synced, and any coverage limits.
- Rows below the cost-coverage threshold show a lower bound and coverage
  without a rank (`—`). The last column holds movement for a ranked row and
  the reason for an unranked one: `71% priced`, `no estimate`, `no usage`. A
  reason this build does not know is shown as sent.
- The global board shows values without ranks until three members can be
  ranked, and says so under the list; a private group ranks any number of
  members.
- A member whose daily total is above the plausibility limit carries a `!`
  after their name. The tags after a name are never cut off by a long name.
- YOUR STANDING shows the viewer's rank, value, movement, and value in the
  comparison window, or says why they have no rank.
- The scope panel shows position among scopes, members, role, and board
  visibility. `y` copies an invite: the owner's first press creates one
  (24 hours, 10 uses) and later presses copy the same code. Copying uses
  OSC 52; because a terminal may ignore it, the whole code is also shown,
  never truncated. The server keeps only invite hashes, so a code is
  available only in the session that created it.
- On `GLOBAL`, `o` joins or leaves the board. Either one is a visibility
  change and asks first; only `y` confirms.
- `c` creates a group and `i` joins with a code. While a prompt is open
  every key is text for it. An invite code is never echoed.
- A success (`Invite copied`) shows for a few seconds as a `DONE` notice.
- A failed load keeps the last board on screen under the failure.

From 88 to 119 columns the rail stacks below the standings as two panels
side by side. From 48 to 87 columns the view is one column: chips wrap and
the rail collapses to one summary line (`@you · RANK #5 · $640.00 EST ·
▲ 2`), plus a line for an invite created in the session.

### Onboarding

A launch on a machine that has never been scanned scans without being asked,
with a single spinner, then reveals the headline. This greeting has no tabs;
`enter` or any view key opens the dashboard. Later launches go straight to
HOME.

```text
>_ TOKENHOLIC                      (three-line block mark from 88 columns)

SCANNED  Claude CLI ✓ · Claude Desktop Code ✓ · Codex CLI and Desktop ✓

YOU BURNED ≥4.26B TOKENS · ≥$1,180.42 EST · LAST 30 DAYS

[ c  CLAIM YOUR SPOT ]
Nothing leaves this machine until you claim.
enter dashboard · c claim · r scan again · s share · ? help · q quit
```

- The headline uses the same rules as every other number: `≥` for a lower
  bound and an `EST` label, never `~`.
- Sources that exist are listed; one that was simply not found is left to
  SOURCES. A machine with no supported agent gets instructions, not a
  headline of zero.
- With no API configured there is no claim to offer; the chip opens the
  dashboard instead.

Claiming (`c`, offered only to a device known to be signed out) runs the
GitHub device flow inside the TUI:

1. A confirmation states what is uploaded, what never is, and that
   `tokenholic show-data` prints the exact payload. Nothing has started;
   `enter` continues and `esc` backs out.
2. The verification address and the code are shown. `o` opens the page, `y`
   copies the code, `esc` cancels. A code approved after a cancel signs
   nobody in.
3. Once approved, pending usage uploads, then the view moves to BOARDS with
   `Signed in as @login · N buckets uploaded`.

Nothing uploads before step 3. A failed start or an expired code offers
`enter` to try again. If the upload fails after sign-in, `enter` retries only
the upload and `esc` continues signed in; local usage is kept either way.
While the claim is open it takes every key, so none quits or navigates by
accident. HOME keeps a `[ c  CLAIM YOUR SPOT ]` chip for a signed-out
device, and the BOARDS sign-in gate offers the same.

### Share

`s` on HOME, BOARDS, or the greeting drafts a post from the period HOME is
showing: the user's own tokens and estimate, with "at least" for a lower
bound and the estimate called an estimate. It names no group and no rank. The
overlay shows the draft and its `x.com/intent/post` link; `o` opens it in the
browser, where the user reviews and posts it, and `y` copies it. Because a
wrapped link in a panel cannot be selected cleanly, the link is printed again
after the TUI exits. tokenholic sends nothing itself.

`tokenholic share` prints the same draft and link without opening the TUI,
for the last 30 days or a `--period`. When the account's profile is public
and the server serves share pages, the post also carries the profile's share
page, so it unfurls with a card; otherwise the post is text only and the
command says why in one line. The link is always the last line of output.

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
| `d` | Show exact token categories on HOME |
| `c` | Claim when signed out; create group in BOARDS |
| `i` | Join with an invite code (input masked) |
| `y` | Copy invite, creating one first for an owner |
| `o` | Join or leave the global board, after confirming |
| `s` | Share: draft a post and its `x.com/intent/post` link |
| `enter` | Leave the first-launch greeting; continue a claim |
| `esc` | Close a prompt, the claim, or the share overlay |
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

The website (M9) shares the TUI's tokens and voice and stays read-only. It
has no sign-in, sets no cookie, and loads nothing from another host except
GitHub avatars. [`web/README.md`](../web/README.md) is the working guide.

### Pages

| Page | Address | Shows |
| --- | --- | --- |
| Landing | `/` | Headline, copy-able `npx @tokenholic/cli`, a sample board, how it works, and what is read, sent, and never sent |
| Public profile | `/u/?login=<login>` | Avatar, login, and tokens, `$ EST`, and priced share for the last 30 days and all-time |
| Public board | `/b/?slug=<slug>` | Standings with period and metric chips, a standing rail, and board facts |
| Invite | `/invite/#<code>` | The group a code joins, when it expires, and the two commands that join it |

A static host serves files, not routes, so a profile and a board are named
by a query string and every link is relative. The site therefore works at a
domain's root or under a path.

### Look

- Dark-only. The page is `#0B1120`, panels are `#111A2E`, and text is
  `#E8EAF0`; every other color is the dark column of the palette table.
- Type: Silkscreen, a pixel face, for the wordmark, headline, panel titles,
  and step numbers, set at multiples of 8 pixels; JetBrains Mono for
  everything else. Both are OFL-licensed and self-hosted, with their licenses
  beside them in `web/assets/fonts/`.
- Titled panels carry their title in the top border, as the TUI draws them.
  Section labels are uppercase, tracked out, and followed by a dashed rule.
- Solid coral marks the primary button and the selected chip. A page has one
  primary action.
- The landing page's board is a static sample, labelled as sample data.

### Board

- Opens on `THIS WEEK` and `$ EST`, as the terminal's does. Chips are plain
  links, so a period or metric is an address that can be shared.
- Rows are drawn exactly as the server ranked them: rank or `—`, avatar,
  `@login` linking to that member's profile, value, and movement or the
  reason there is no rank. A member above the plausibility limit carries `!`.
- The first three ranks use the gold, silver, and bronze tokens.
- The site cannot know who is looking, so nothing is ever labelled `★ YOU`.
  A link may name a member in its fragment (`#login`); that row gets the
  coral edge, tint, and accent text, and the rail shows that member's
  standing. Without one the rail shows the leader.
- The rail also states how many public members the board lists, when the
  period started, the timezone, and when the board was read, and repeats the
  install command.
- Notes under the standings state the rules in words: the priced share an
  estimate needs to be ranked, that estimates are never spending, when ranks
  are hidden, and what `!` means.
- An avatar that is missing or fails to load is a tile with the login's
  first letter.

### States

Every data page draws one of these, and never an empty layout:

| State | When | Shows |
| --- | --- | --- |
| Loading | First 2.5 seconds | `LOADING` with a blinking caret |
| Waking | No answer yet, or the API is starting | `WAKING THE API`: free hosting sleeps when idle; the page fills in by itself |
| Loaded | The API answered | The page |
| Not found | The API said so, or the address could not name a record | One panel, in the same words for a private record and a missing one |
| Too many requests | The API refused the request | How long to wait, and `TRY AGAIN` |
| Unreachable | Two minutes of trying failed | `ERROR API UNREACHABLE` and `TRY AGAIN` |

A not-found invite says it may have expired, been used up, or been mistyped.
A value that is unknown is `—`, a lower bound carries `≥`, and each panel
says which in words. `$ EST` labels every estimate.

### Phone width

Layouts are one column below 720 pixels and never scroll sideways. The board
rail drops under the standings, chips wrap, a long login is cut with an
ellipsis, and a long command wraps inside its box so an invite code is always
shown whole.

### Accessibility

- Text meets WCAG AA (4.5:1) on the page, panels, and the highlighted row; a
  test fails if a token change breaks that. Panel borders are decoration.
- Every link and button has a visible coral focus ring, and a skip link
  leads the tab order.
- Movement, lower bounds, and status never rely on color: each has a glyph or
  a word, and movement is also announced in words.
- The only motion is the loading caret, which stops under
  `prefers-reduced-motion`.

### Sharing from the site

A public profile and a public board each carry one quiet `SHARE ON X`
button. It is a plain link to X's compose page with a draft the visitor
reviews and sends; the site posts nothing. The draft is in the third person,
because the visitor may not be the owner, names the estimate as an estimate,
and is not offered when there is no token total to state. The link attached
to the draft is the API's share page, so the post unfurls with a card.

### Share cards

A share link (`/s/u/<login>` or `/s/b/<slug>` on the API) answers a crawler
with Open Graph tags and a 1200×630 PNG card, and sends a browser on to the
matching website page. Cards use the website's palette and typefaces.

```text
>_ TOKENHOLIC                                        PUBLIC PROFILE
╭─ @DANA ─────────────────────────────────────────────────────────╮
│ LAST 30 DAYS - - - - - - - - - - - - - - - - - - - - - - - - -  │
│ TOKENS          $ EST                PRICED                     │
│ ≥4.26B          ≥$1,180.42           92.3%                      │
│ All-time  ≥21.9B tokens · ≥$6,204.18 est                        │
╰─────────────────────────────────────────────────────────────────╯
$ npx @tokenholic/cli        EST = API LIST PRICES, NEVER SPENDING · SELF-REPORTED
```

- Every card: the wordmark with its coral caret, what the card is, a titled
  panel, the install command, and small print that calls an estimate an
  estimate.
- A profile card headlines the last 30 days, as the terminal's share action
  does: tokens, `$ EST`, and the priced share, with all-time on one line
  under them.
- A board card shows the period and metric, the first three rows as the
  server ranked them with gold, silver, and bronze rank numbers, and how many
  public members the board lists. It opens on `THIS WEEK` and `$ EST`, and a
  link can name another period or metric.
- A board whose ranks are hidden shows `—` for each rank and says when ranks
  appear. A profile with no usage in the period shows `—` in each tile and
  says so. No card ever shows an unknown value as zero.
- Headline values shrink to fit, a long login or group name is cut with an
  ellipsis, and nothing is drawn outside the panel. A title the pixel face
  cannot spell is set in the monospace instead.
- Cards carry no avatar: the API does not fetch images from other hosts.
- The share page itself is a few lines of text with the card and a link, in
  case a browser does not follow the redirect.

## Accessibility

- Support `NO_COLOR`: brackets mark selection, text labels carry status.
  This is a fallback a user opts into, never the default look.
- Test light and dark terminal backgrounds.
- Do not use color alone for rank movement, coverage, or errors.
- Provide ASCII fallbacks for every Unicode glyph used for meaning. The
  fallback set is one switch for the whole interface, independent of color,
  chosen by `TOKENHOLIC_ASCII` or a terminal that declares itself `dumb`:

  | Meaning | Unicode | ASCII |
  | --- | --- | --- |
  | Panel frame | `╭─╮│╰╯` | `+-+|+-+` |
  | Rank up / down / unchanged / new | `▲ 2` `▼ 1` `–` `NEW` | `+2` `-1` `=` `new` |
  | Current-user edge and tag | `▌` `★ YOU` | `\|` `* YOU` |
  | List cursor | `▸` | `>` |
  | Lower bound / unavailable | `≥` `—` | `>=` `-` |
  | Sparkline levels, low to high | `▁▂▃▄▅▆▇█` | `_.-=#` |
  | Phrase separator / range / truncation | `·` `–` `…` | `|` `-` `~` |
  | Step done / failed | `✓` `✗` | `ok` `x` |

- A sparkline cell with no recorded value is blank, which is not the lowest
  bar: an unknown day is not a zero day.
- Notices name their level in words (`WARNING`, `ERROR`, `DONE`, `NOTE`).

## Non-goals

- Images or terminal image protocols in the TUI (avatars are web-only).
- Mouse-first interaction.
- Theme customization before the default experience is stable.
- A web dashboard that duplicates TUI functionality or accepts writes.
