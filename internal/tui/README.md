# Terminal interface

`internal/tui` is presentation only. It renders what the application service
and the multiplayer service return and never discovers sources, reads
storage, calls the API, ranks, or prices. The visual direction is specified in
[docs/design.md](../../docs/design.md).

## Layers

| Package | Role |
| --- | --- |
| `theme` | Design tokens: the palette for dark and light backgrounds, the text styles built from it, and the glyph set with its ASCII fallbacks |
| `widget` | Stateless building blocks drawn from a theme: chips, tabs, titled panels, dashed rules, tables, sparklines, movement marks, notices, and the key bar |
| `tui` | The Bubble Tea model, key handling, and the views composed from widgets. `values.go` is the one place stored values become text, so an unknown value cannot turn into a zero in one view and not another |
| `goldentest` | Test support for comparing rendered frames with golden snapshots |

Numbers are formatted by [`internal/numfmt`](../numfmt), kept outside the TUI
so the share cards can apply the same rules.

## Rules a widget keeps

- Take plain text and a width; return lines no wider than that width.
- Never rely on color alone. A selected chip is bracketed without color, a
  notice names its level in a word, and movement has a glyph.
- Use only glyphs from `theme.Glyphs`, so every one has an ASCII fallback.
- Leave "unknown" to the caller. A widget draws the text it is given; it
  never turns a missing value into `0`.

## Golden snapshots

Two sets of frames are fixed at 48, 88, and 120 columns, with color and
without, plus a light background and ASCII:

- `widget/gallery_test.go` lays every component out in one frame.
- `golden_test.go` renders each screen and state of the application from the
  synthetic dashboard in `fixture_test.go`, driving the model with the same
  keys a user would press.

```bash
go test ./internal/tui ./internal/tui/widget -update
```

rewrites them after an intended change; review the diff before committing.
`goldentest` holds the comparison and the width check both sets share.
The color snapshots hold the real escape sequences, so printing one shows the
frame as a terminal draws it:

```bash
cat internal/tui/widget/testdata/gallery_120_color.golden
```

Snapshots contain synthetic names and numbers only.
