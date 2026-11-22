# Free demo deployment

The hosted API is optional. Local collection, SQLite persistence, diagnostics,
and the first four TUI views work without it. Deploy the API only to exercise
GitHub login, multi-device synchronization, private groups, and leaderboards.

The repository's free demo configuration uses one Render web service and one
Supabase PostgreSQL project:

```text
macOS tokenholic client          GitHub Pages website (optional)
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
address. This has not been measured against a live Render service yet.

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

Point a release binary at the HTTPS origin without writing the value into the
repository:

```sh
export TOKENHOLIC_API_URL='https://<service>.onrender.com'
tokenholic login
tokenholic sync --dry-run
tokenholic sync
tokenholic group create 'Demo group'
tokenholic
```

Use a second GitHub identity or a second tester to validate invitations and
ranking. Confirm that a repeated `sync` does not increase unchanged totals.
Before enabling autosync, remember that it preserves the configured API origin
in the private launch-agent configuration.

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
