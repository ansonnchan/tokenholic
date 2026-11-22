# npm distribution workspace

`@tokenholic/cli` is a small CommonJS launcher that selects one optional native
package from the current operating system and CPU. It forwards arguments
directly without invoking a shell. Unsupported platforms and incomplete native
packages fail with an actionable message instead of falling back to a different
binary.

When a user explicitly enables autosync, the native application copies itself
out of the package-runner cache before installing the schedule. npm package
installation never enables background work automatically.

The native package directories intentionally do not contain committed
binaries. During a tagged release, `scripts/prepare-npm-release.mjs` verifies
the GitHub artifact checksums, extracts the matching release archives, copies
only the native binary and pinned parser contract, and assigns one exact version
to every package. The native packages publish before the umbrella package so
an install can never resolve a new launcher before its binaries. The Linux
binaries are statically linked, so one package serves glibc and musl systems
and no `libc` field is needed. The Windows packages are named `win32`, as
Node and npm name that system, and carry `tokenholic.exe`; the launcher
appends the extension when it resolves the binary.

`scripts/platforms.mjs` is the list of shipped platforms. The launcher's own
table, the umbrella package's optional dependencies, and the package
directories repeat it because a published file cannot import it; tests in
`scripts/platforms.test.mjs` fail when any of them disagree.
Publication is restart-safe: the publisher checks each exact registry version,
skips packages already completed by an earlier attempt, and preserves the same
native-first ordering on retry.

Run metadata, launcher, and preparation tests with:

```sh
pnpm check
pnpm test
```
