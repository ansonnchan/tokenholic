# Development guide

This guide covers the native Go workspace and the pnpm release workspace. pnpm
is needed only for npm metadata, launcher, and package-preparation checks; it
does not participate in local collection builds.

## Prerequisites

- Go 1.25 or newer.
- Node.js 20 or newer plus the repository-pinned pnpm through Corepack, only for npm distribution work.
- macOS, Linux, or Windows to exercise the implemented default discovery roots.
- Git for the repository hygiene check.
- Docker or another PostgreSQL 18 installation only for hosted schema tests.

ccusage is optional. CI validates only synthetic contract fixtures and the
release lock, so verification never executes an installed binary or inspects
real local agent data.

## Common commands

```sh
make check       # formatting, vet, race tests, build, and diff hygiene
make test        # standard Go tests
make test-race   # race-enabled Go tests
make build       # build every Go package
make prices      # regenerate the vendored price snapshot from its pin (network)
make release VERSION=1.2.3 # reproducible macOS, Linux, and Windows archives
pnpm check                 # npm metadata and launcher validation
pnpm test                  # npm preparation, launcher, and website tests
```

Run the hosted schema integration test against an isolated PostgreSQL database:

```sh
TOKENHOLIC_TEST_DATABASE_URL='postgres://tokenholic:password@localhost:5432/tokenholic_test?sslmode=disable' make test-postgres
```

The test creates a random schema, applies the embedded migrations twice, checks
ownership and bucket constraints, and removes that schema afterward. Never use
a production database URL. Regular `make check` does not require PostgreSQL;
CI runs the integration target separately against PostgreSQL 18.

Build and run the command directly:

```sh
go build -o ./bin/tokenholic ./cmd/tokenholic
./bin/tokenholic --version
./bin/tokenholic             # interactive local TUI
./bin/tokenholic doctor
```

## Local account API

Running account commands locally requires PostgreSQL and a GitHub OAuth app
with Device Flow enabled. The app needs only public identity; do not request
repository or organization scopes.

Configure the API process with:

```sh
export DATABASE_URL='postgres://tokenholic:password@localhost:5432/tokenholic?sslmode=disable'
export GITHUB_CLIENT_ID='your-github-oauth-client-id'
export TOKENHOLIC_AUTH_STATE_KEY="$(openssl rand -base64 32)"
export TOKENHOLIC_LISTEN_ADDR='127.0.0.1:8080'
go run ./server/cmd/api
```

A build from source carries no API address, so it can never reach the hosted
service by accident. Point the native client at the loopback API with the
override that release builds leave unset:

```sh
TOKENHOLIC_API_URL='http://127.0.0.1:8080' go run ./cmd/tokenholic login
TOKENHOLIC_API_URL='http://127.0.0.1:8080' go run ./cmd/tokenholic logout
TOKENHOLIC_API_URL='http://127.0.0.1:8080' go run ./cmd/tokenholic sync --dry-run
TOKENHOLIC_API_URL='http://127.0.0.1:8080' go run ./cmd/tokenholic sync
TOKENHOLIC_API_URL='http://127.0.0.1:8080' go run ./cmd/tokenholic account delete --confirm DELETE
TOKENHOLIC_API_URL='http://127.0.0.1:8080' go run ./cmd/tokenholic autosync enable
go run ./cmd/tokenholic autosync status
go run ./cmd/tokenholic autosync disable
```

Plain HTTP is accepted only for loopback development. Non-loopback API origins
must use HTTPS. The state key encrypts short-lived GitHub device codes and must
be provided through secret management in a deployed environment; it is not a
client credential.

`sync` first runs the normal local collection transaction and absolute-bucket
rebuild. It then uploads committed outbox records in batches of at most 100.
`sync --dry-run` follows the same path through serialization, but prints one
exact request body per line without contacting the API or changing outbox state.
Account deletion is permanent for hosted data and removes the local device
session only after the server confirms deletion. It does not erase local SQLite
usage or provider evidence.

Autosync is opt-in and uses the system's per-user scheduler: a launch agent on
macOS, a systemd user timer on Linux, and a Task Scheduler task on Windows. Enabling it copies the current native
executable to a stable private location, so use a built release binary rather
than `go run` for a lasting schedule. Re-enable it after an upgrade to refresh
that copy. Disabling removes the schedule, copy, and logs without deleting
local usage or credentials. The [autosync guide](../internal/autosync/README.md)
lists the files each platform uses.

On Linux, signing in needs a Secret Service keyring (GNOME Keyring, KWallet,
or KeePassXC) on the session D-Bus, and autosync needs a systemd user session.
A container or a bare SSH session usually has neither; local collection,
`show-data`, and `doctor` work without them. To exercise the Linux build from
another system, run the suite in a container:

```sh
docker run --rm -v "$PWD":/src:ro -w /src golang:1.25 go test ./...
```

Windows has no such shortcut. `GOOS=windows go vet ./...` catches compile
errors from any system; behavior is proven by the Windows CI job and by the
manual run in [release validation](./release-validation.md). On Windows, run
the Makefile from Git Bash and point `GOCACHE` and `GOPATH` at Windows paths,
as the CI job does.

An authenticated client can inspect the account-level aggregation endpoint:

```sh
curl -H 'Authorization: Bearer TOKENHOLIC-DEVICE-CREDENTIAL' \
  http://127.0.0.1:8080/v1/account/usage
```

The response aggregates active devices and reports every device's revocation
and freshness state. Never paste a real device credential into documentation,
shell history, bug reports, or test fixtures.

The Makefile places `GOCACHE` and `GOPATH` under `/tmp` by default. Override those variables when a development environment needs different writable cache locations.

## Test boundaries

- Parser tests consume only committed synthetic fixtures or inline synthetic records.
- Discovery and doctor tests construct temporary home-directory layouts.
- Fixture privacy tests scan every file under `fixtures`, including malformed and non-JSON cases.
- Collection tests cover repeatability, overlap, cumulative counters, resets, partial writes, malformed records, and attribution quality.
- Application and TUI tests inject storage, collection, terminal-size, and keyboard events; they never launch against real provider roots.
- PostgreSQL integration tests use only synthetic identities and values in a disposable schema.
- Authentication tests use synthetic GitHub responses and never contact a real account.
- Synchronization tests use allowlisted synthetic buckets and verify retry, replay, replacement, conflict, and acknowledgement behavior.
- Account integration tests cover multi-device revocation and complete account-owned data deletion without affecting unrelated accounts.

Do not point automated tests at a real home directory or provider-owned storage. Real compatibility investigation requires explicit user approval and must produce a newly constructed sanitized regression fixture rather than a copied transcript.

## Adding code

1. Read `AGENTS.md`, the roadmap, architecture, and design guidance.
2. Keep provider rules inside adapters and interfaces at their consuming boundary.
3. Preserve nil versus reported zero and observed versus canonical model identity.
4. Add table-driven tests and synthetic fixtures for source-format behavior.
5. Update the closest package or subsystem documentation when a contract changes.
6. Run `make check` and `git diff --check` before committing.

For stacked work, open and report each parent PR before starting its child branch. Never merge a PR without an explicit request for that PR.

## CI

GitHub Actions runs `make check` and a release build on macOS, Linux, and
Windows,
validates the npm release workspace, builds the production API container, and
runs the hosted schema integration test against PostgreSQL 18 on Linux. On
Linux the autosync tests also ask `systemd-analyze` to parse the generated
units, and on Windows they register the generated task with the real Task
Scheduler and remove it. That last test changes system state, so it runs
only where `TOKENHOLIC_TEST_TASK_SCHEDULER` is set, as CI sets it. Go jobs use the version declared in `go.mod`. A local clean checkout should pass the same applicable commands before
a PR is marked ready.
