# Website

The static, read-only website: plain HTML, CSS, and a little JavaScript. There
is no framework and no build step. It never signs anyone in and never writes;
collection, accounts, groups, and every change stay in the terminal.
[`docs/design.md`](../docs/design.md) owns the look and
[`docs/architecture.md`](../docs/architecture.md) the boundary.

## Pages

| Address | Shows | Reads |
| --- | --- | --- |
| `/` | Landing: what it is, the install command, what is read, sent, and never sent | Nothing |
| `/u/?login=<login>` | A public profile | `GET /v1/public/users/{login}` |
| `/b/?slug=<slug>[&period=][&metric=][#login]` | A public group board | `GET /v1/public/groups/{slug}/leaderboard` |
| `/invite/#<code>` | Which group an invite joins, and how to join from the terminal | `GET /v1/public/invites/{code}` |
| `404.html` | Any other address | Nothing |

Addresses use a query string or fragment because a static host serves files,
not routes. Every link between pages is relative, so the site works at a
domain's root or under a path such as a GitHub Pages project site.

A board's fragment names a member to highlight. The site has no sign-in and
cannot know who is looking, so that is a pointer someone chose to share,
never an identity. An invite's code sits in the fragment so it is not sent to
the server hosting the page; it goes only to the API.

## What a page may show

- Only what the API's public routes return: a login, an avatar, aggregate
  totals, ranks, and trends that their owner made public.
- A private, deleted, or nonexistent record is one state, in the same words,
  because the API answers them identically.
- An unknown value is `—` and a lower bound `≥`, never zero. Every cost is
  labelled an estimate. `assets/js/values.js` is the one place an API value
  becomes text.
- While the API wakes from a cold start, an explicit wake-up panel, never an
  empty layout. `assets/js/api.js` retries for two minutes before giving up.
- Text from the API is set as text and never parsed as HTML. Avatars load
  only from GitHub's avatar host.

A public profile and board each have a `SHARE ON X` link. It opens X's
compose page with a draft and the API's share page for that record; the site
itself posts nothing.

Each page carries a Content-Security-Policy that allows this site's own
scripts, styles, and fonts, GitHub avatars, and HTTPS requests. Nothing else
loads: no third-party script, font, or analytics.

## Layout

```text
index.html, u/, b/, invite/, 404.html   pages
assets/tokens.css                       colors and type, shared with the TUI
assets/site.css                         the one stylesheet
assets/fonts/                           self-hosted OFL fonts and their licenses
assets/js/config.js                     the API origin; empty in the repository
assets/js/format.js                     port of internal/numfmt
assets/js/values.js                     API values to display text
assets/js/api.js                        public reads and the wake-up state
assets/js/ui.js                         DOM helpers and state panels
assets/js/{profile,board,invite}.js     one entry point per data page
tests/                                  node:test suites; not published
```

`tokens.css` takes its role colors from the dark column of the palette in
`internal/tui/theme`. A Go test there fails if a value drifts or if any text
color falls below WCAG AA contrast on the site's backgrounds. `format.js`
runs the same vectors as the Go formatter, from
`internal/numfmt/testdata/vectors.json`.

## Working on it

```sh
node --test web/tests/*.test.mjs   # also part of `pnpm test`
python3 -m http.server --directory web 8000
```

Module scripts need an HTTP server; opening a file directly will not run
them. A checkout has no API origin, so the data pages say they are not
connected. The API lets only its configured HTTPS website origin read it
from a browser, so a page on localhost cannot read a deployed API. To see the
data pages locally, serve this directory and a local API from one origin
behind any reverse proxy and set `assets/js/config.js` to that origin. Do
not commit a changed `config.js`.

Check a layout change at 390 and 1280 pixels wide, with the keyboard alone,
and with reduced motion on.

## Publishing

`.github/workflows/pages.yml` publishes to GitHub Pages. It is skipped until
the repository variable `TOKENHOLIC_API_ORIGIN` names the API.
`scripts/assemble-website.mjs` copies this directory without its development
files and writes that origin into `config.js`; nothing is compiled.
[`docs/deployment.md`](../docs/deployment.md#6-publish-the-website) lists
the steps, including the API setting that lets the site read it.
