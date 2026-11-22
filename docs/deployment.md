# Free demo deployment

The hosted API is optional. Local collection, SQLite persistence, diagnostics,
and the first four TUI views work without it. Deploy the API only to exercise
GitHub login, multi-device synchronization, private groups, and leaderboards.

The repository's free demo configuration uses one Render web service and one
Supabase PostgreSQL project:

```text
tokenholic client                GitHub Pages website (optional)
        |                                |
        | privacy-limited HTTPS requests | public, read-only JSON
        v                                v
Render free Go API  <--------------------+
        |
        | encrypted PostgreSQL connection
        v
Supabase free PostgreSQL
```

This environment is suitable for personal testing and interviews. It is not a
production service: free instances can sleep or pause, and the free database
does not include automatic backups. Do not publish availability, durability,
latency, or adoption claims from this environment.

## 1. Create the Supabase database

Create a free Supabase project near the Render region you plan to use. Keep the
generated database password in a password manager.

In the Supabase project's **Connect** dialog, copy the **Session pooler** URI.
The Go API is a persistent backend, so use session mode on port `5432`, not the
transaction pooler on port `6543`. Append `sslmode=require` and cap this small
demo's application pool:

```text
postgresql://postgres.<project-ref>:<encoded-password>@<pooler-host>:5432/postgres?sslmode=require&pool_max_conns=4
```

Copy the host and username from the dashboard rather than constructing them.
Percent-encode reserved characters in the password. Never commit the completed
URI or paste it into an issue, test, log, or documentation.

The API applies the repository's embedded, checksummed migrations during
startup. Do not run automated tests against this database.

## 2. Create the GitHub OAuth app

Create a GitHub OAuth app owned by the account that will operate the demo:

- use the repository URL as the homepage until the Render URL exists;
- use a loopback callback URL such as `http://127.0.0.1` because Device Flow
  does not use the callback; and
- enable **Device Flow** in the OAuth app settings.

The API needs only the OAuth app's client ID. Do not create, store, or configure
a client secret, and do not request repository or organization scopes.

## 3. Deploy the Render Blueprint

In Render, create a Blueprint from this repository. The root [`render.yaml`](../render.yaml)
builds [`deploy/render/Dockerfile`](../deploy/render/Dockerfile), configures the
existing `/healthz` endpoint, and generates the 32-byte authentication-state
key. Render prompts for the remaining secrets:

- `DATABASE_URL`: the Supabase Session pooler URI from step 1;
- `GITHUB_CLIENT_ID`: the OAuth app client ID from step 2.

Render then deploys the service again on every push to `main`. There is no
separate deploy step: merging a pull request that changes `server/`,
`migrations/`, or a package the API imports puts it in front of users within
minutes, and its migrations run as the service starts. GitHub records each
deploy; `gh api 'repos/<owner>/<repository>/deployments?per_page=5'` lists
them with their commits. To deploy by hand instead, turn auto-deploy off in
the service's settings on Render.

The container reads Render's `PORT` and binds to all container interfaces.
`TOKENHOLIC_LISTEN_ADDR` remains available for an explicit override and takes
precedence over `PORT`; do not set it on Render.

The API rate-limits each client address and each credential; the
[server guide](../server/README.md#rate-limits) lists the limits. Render
terminates connections at its own proxies, so the API sees the proxy's address
unless `TOKENHOLIC_TRUSTED_PROXY_HOPS` tells it how many proxies append to
`X-Forwarded-For`. The Blueprint leaves it unset, which is safe but coarse:
every client then shares one address allowance, and only the per-credential
limit separates users. Set it only after confirming, from the platform's
documentation or the headers a request actually arrives with, how many
entries its proxies add. A value that is too high lets a client pick its own
address. Measured on November 21, 2026: a request sent without the header
reached this Render service with three entries, so the value here is `3`,
and the service is set to it. Measure again if the hosting changes.

To measure it, the API writes one log line the first time a request arrives
with each number of `X-Forwarded-For` entries, with the count and never an
address:

```text
tokenholic api: a request arrived with 2 X-Forwarded-For entries; TOKENHOLIC_TRUSTED_PROXY_HOPS is 0
```

Right after a deploy, send one request yourself without that header and read
the service log:

```sh
curl --silent --output /dev/null https://<service>.onrender.com/v1/public/users/nobody
```

The count on that line is the number of proxies in front of the API, and the
value to set. The line is written once per distinct count, so it may already
be in the log from earlier traffic. `/healthz` is not counted, because the
platform's own probes do not come through its proxies. A line with a larger
count is a client that sent its own header, not a longer chain: when there
are several, the smallest count is the one to use, because a client can add
entries but cannot remove a proxy's. After setting the
variable, the same request must log the same count with the new setting
beside it; then confirm the limit keys on you and not on the proxy by
checking that a request from a second network is not slowed by the first.

`TOKENHOLIC_WEB_ORIGIN` is optional and belongs to
[publishing the website](#6-publish-the-website). Without it the API returns
invite codes without links and no web page can read it.

After the first deployment completes, verify the generated HTTPS origin:

```sh
curl --fail --show-error https://<service>.onrender.com/healthz
```

The response should be `{"status":"ok"}`. A free service sleeps after an idle
period, so the first request can take noticeably longer while it wakes.

## 4. Exercise the complete journey

Release builds carry one API address, `hostedAPI` in
[`cmd/release/main.go`](../cmd/release/main.go). Users never set anything.
When this deployment's origin differs from that constant, change the constant
in a pull request before tagging the release; the release build refuses an
address that is not a bare `https` origin and checks that every binary
carries it.

`TOKENHOLIC_API_URL` overrides the built-in address. It is for the operator
and for development, for example to try a second deployment before moving
the constant:

```sh
export TOKENHOLIC_API_URL='https://<service>.onrender.com'
tokenholic doctor   # reports "API address: set by TOKENHOLIC_API_URL"
tokenholic login
tokenholic sync --dry-run
tokenholic sync
tokenholic group create 'Demo group'
tokenholic
```

Use a second GitHub identity or a second tester to validate invitations and
ranking. Confirm that a repeated `sync` does not increase unchanged totals.
The full two-account checklist is in
[release validation](./release-validation.md). Autosync enabled while the
override is set keeps that address in its private schedule definition;
enabled without it, the job uses the address built into its executable.

## 5. Operate or remove the demo

Before an interview or manual test:

1. resume the Supabase project if its dashboard shows it as paused;
2. request `/healthz` and wait for the Render service to wake;
3. run `tokenholic login` or `tokenholic sync`; and
4. inspect Render and Supabase logs without copying request bodies or secrets.

Deploying the sync v2 server resets demo usage. Its migration replaces the
`usage_buckets` table and discards every uploaded bucket; accounts, devices,
groups, and memberships are kept. Clients older than 0.2 receive
`426 Upgrade Required` and must upgrade, after which they rebuild their local
buckets and upload their full history again. Nothing local is lost.

A deploy that changes the bundled prices reprices stored usage while the
service starts. The log line `repriced N usage buckets to pricing revision R`
confirms it. Startup takes longer in proportion to the stored buckets, and the
service does not answer `/healthz` until the pass commits.

Free Supabase projects have no automatic backups. Treat synchronized records as
disposable copies of local aggregates. The source evidence and local SQLite
database remain authoritative; deleting the Render service and Supabase project
removes the demo without affecting local usage.

## 6. Publish the website

The website in [`web/`](../web/README.md) is static and read-only. Nothing is
published until every step below is done by the repository's owner; merging
the workflow file alone publishes nothing.

1. In the repository's **Settings → Pages**, set the source to **GitHub
   Actions**.
2. In **Settings → Secrets and variables → Actions → Variables**, add the
   repository variable `TOKENHOLIC_API_ORIGIN` with the API's HTTPS origin,
   such as `https://<service>.onrender.com`. It is not a secret: it is
   written into the published pages.
3. Run the **Website** workflow, or push a change under `web/` to `main`.
   The workflow copies `web/` without its development files, writes the API
   origin into `assets/js/config.js`, and deploys the result. Until the
   variable exists the job is skipped, not failed.
4. On Render, set `TOKENHOLIC_WEB_ORIGIN` to the address the site is served
   from: `https://<owner>.github.io/<repository>` for a project site, or the
   bare origin for a user site or custom domain. The API then lets pages
   from that origin, and no other, read its public routes, serves share
   pages and cards under `/s/`, and adds a link to each new invitation.

Check it with a browser: the landing page loads without the API; a profile
or board address shows `WAKING THE API` while the service starts and then
either the page or the not-found panel. A page that stays unreachable while
`/healthz` answers usually means step 4 names a different origin from the
one in the address bar.

To check sharing, make a profile public, run `tokenholic share`, and open
the printed `Card:` address: a browser should land on the website's profile
page, and the address plus `/card.png` should show the card. Paste the
address into a link-preview checker to confirm the large image appears; wake
the API first, because a crawler does not wait for a cold start.

The site keeps no data and needs no migration. To take it down, disable Pages
and unset `TOKENHOLIC_WEB_ORIGIN`.
