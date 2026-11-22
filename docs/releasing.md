# Release artifacts

tokenholic releases checksummed native archives for macOS, Linux, and Windows,
each on arm64 and x64, named `tokenholic_<version>_<os>_<cpu>.tar.gz`. Windows
archives use the same format; Windows 10 and later unpack them with the
built-in `tar`. Release builds
disable CGO, remove local filesystem paths and VCS metadata, inject the
semantic version into `tokenholic --version`, and verify each binary before
packaging it: its format and CPU, and for Linux that it is statically linked,
so the same file runs on glibc and musl systems.

Each archive contains the native `tokenholic` executable (`tokenholic.exe` on
Windows) and `ccusage.lock.json`. The lock file is the pinned external-parser
contract: the one ccusage release tokenholic runs, its arguments, and the
digest of its executable for every platform. The build refuses a lock that
differs from the contract compiled into the binary. An archive carries no
ccusage executable and the application never downloads one; the npm launcher
package installs the pinned one, and the binary runs it only when its digest
matches. Keeping the contract beside the binary makes that requirement
auditable without expanding the release's trust boundary.

Every release build also carries the hosted API address, the `hostedAPI`
constant in `cmd/release`, so a user never configures one. The build refuses
an address that is not a bare `https` origin and checks that each binary
holds it. A build made with plain `go build` carries none.

Build artifacts from a clean checkout with:

```sh
make release VERSION=1.2.3
```

The command writes every archive and `SHA256SUMS` under `dist/`. Archive entry
ordering, ownership, modes, timestamps, and gzip headers are normalized. Given
the same source, Go toolchain, version, and pinned inputs, it produces the same
bytes.

Pushing an existing semantic-version tag such as `v1.2.3` runs the release
workflow, in four steps that each wait for the one before:

1. **Build.** A macOS runner cross-compiles every archive and uploads them as
   workflow artifacts. It can execute only the macOS binary for its own CPU.
2. **Smoke.** Linux x64, Linux arm64, and Windows x64 runners each download
   the artifacts, check their archive against `SHA256SUMS`, and confirm the
   binary starts and reports the tagged version. A failure here stops the
   release before anything is public. Windows arm64 has no runner; it is
   covered by the manual run in [release validation](./release-validation.md).
3. **Release.** The archives and `SHA256SUMS` are attached to the matching
   GitHub release.
4. **npm.** The workflow verifies the checksums again, prepares exact-version
   platform packages, tests the npm workspace, and publishes every
   `@tokenholic/<os>-<cpu>` package and finally `@tokenholic/cli`. The
   Windows packages are named `win32`, as Node and npm name that system.

Configure the GitHub Actions `NPM_TOKEN` secret with publish access to the
`@tokenholic` scope before creating a release tag. The publisher is
restart-safe and skips exact package versions that an earlier attempt
completed. Verify a published version from a clean account on each system
with `npx --yes @tokenholic/cli@VERSION --version`.

To add a platform, add it to `targets` in `cmd/release`, to
`scripts/platforms.mjs`, to the launcher's table in
`packages/cli/bin/tokenholic.js`, and to the umbrella package's optional
dependencies, and create its package directory. Tests fail until all five
agree.

Before tagging, work through [release validation](./release-validation.md).
The server side needs no release step: Render deploys every push to `main`,
so the hosted API already speaks the protocol a tagged client needs. That
also means a merge that changes the server is live before any tag.

## Notes to include in the next release

GitHub generates release notes from merged pull requests. Add these by hand,
because users must act on them:

- **Linux and Windows.** `npx @tokenholic/cli` now runs on Linux and Windows
  as well as macOS, on x64 and arm64. On Linux, signing in needs a Secret
  Service keyring and autosync needs a systemd user session. Windows is new:
  report anything that looks wrong.
- **Nothing to configure.** The client knows the hosted API's address.
  `TOKENHOLIC_API_URL` is now only an override; remove it from your shell
  profile unless you run your own API. If autosync was enabled with it set,
  run `tokenholic autosync enable` again after unsetting it.
- **Re-enable autosync after upgrading**, as always: the schedule runs its
  own copy of the executable.

- **Demo-data reset.** Sync protocol v2 replaces the server's uploaded usage.
  Every bucket uploaded before this release is discarded; accounts, devices,
  groups, and memberships are kept. After upgrading, each device rebuilds its
  local buckets and uploads its full history again on the next sync.
- **Upgrade required to sync.** Clients older than this release receive
  `426 Upgrade Required`. Upgrade with `npx @tokenholic/cli@latest`. Local
  usage is not affected.
- **Local estimates are a preview.** The server now resolves models and derives
  estimates. The values the CLI shows before a server reply come from its own
  bundled catalog and can differ from the server's.
- **Five more agents, through ccusage, on by default.** GitHub Copilot CLI,
  Gemini CLI, Grok Build CLI, OpenCode, and Qwen Code are counted as daily
  totals. To read them tokenholic runs ccusage, a third-party program, at one
  pinned and digest-checked release that is installed with the npm package.
  It reads those agents' local session records and returns token counts.
  `tokenholic ccusage off` stops tokenholic from running it.
