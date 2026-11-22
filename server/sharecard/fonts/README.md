# Share-card fonts

The share-card renderer embeds these fonts so the API can draw cards without
reading anything at run time. They are the website's two families, licensed
under the SIL Open Font License 1.1, and are unmodified upstream binaries; the
license texts beside them are the upstream copies.

| File | Family | Use | Source |
| --- | --- | --- | --- |
| `Silkscreen-Regular.ttf`, `Silkscreen-Bold.ttf` | Silkscreen, © 2001 The Silkscreen Project Authors | Wordmark and panel title | `ofl/silkscreen/` in [google/fonts](https://github.com/google/fonts/tree/main/ofl/silkscreen) |
| `JetBrainsMono-Regular.ttf`, `JetBrainsMono-Bold.ttf` | JetBrains Mono, © 2020 The JetBrains Mono Project Authors | Everything else | `fonts/ttf/` in [JetBrains/JetBrainsMono](https://github.com/JetBrains/JetBrainsMono/tree/master/fonts/ttf) |

Licenses: [`Silkscreen-OFL.txt`](./Silkscreen-OFL.txt) and
[`JetBrainsMono-OFL.txt`](./JetBrainsMono-OFL.txt).

The Silkscreen files are the same bytes as the website's copies in
`web/assets/fonts/`. The website serves JetBrains Mono as WOFF2; the
renderer needs TrueType, so these are the same release's TTF builds.

Retrieved November 21, 2026. SHA-256 of each file as committed, checked by a
test:

```text
c845473330b94c2079ce9af01c51ac8ba2d99c24f4d14c039843bbb8e642ebd8  Silkscreen-Regular.ttf
768476aa712d4f5c3e18d3bce80f980a8bd3f72b7094d22ec5e768df3acfed61  Silkscreen-Bold.ttf
e6fd0d7e91550b3ed2b735d4312474362c4716edc4fc0577a0f61ed782d5aed1  JetBrainsMono-Regular.ttf
d22c4f3821d725eb01210d278d95dfcfcaadc34699a06658d47c8a5cc5830ada  JetBrainsMono-Bold.ttf
```
