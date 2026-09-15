# Oculus telemetry Worker

Receives anonymized diagnostic batches from the daemon (`daemon/telemetry`) and writes them to
Cloudflare Analytics Engine so failures in the wild (a session-create hang, a provider that won't
start) are traceable without asking a user to dig logs out by hand.

## Privacy

The daemon sends **only**: a locally-generated random install id, daemon version, OS/arch, and per
event `{event, provider, dur_ms, ok, error}` where `error` is a **scrubbed class** (home dir +
absolute paths redacted, truncated). It never sends file paths, repo/branch names, prompts, tokens,
or message content. On by default with an in-app toggle (Settings ⋯ → "Send anonymous diagnostics").

## Deploy (one-time, needs your Cloudflare auth)

```sh
cd cloudflare/telemetry-worker
npx wrangler login        # if not already authenticated
npx wrangler deploy
```

The ingest URL is `https://telemetry.ironrain.app/ingest` (the `[[routes]]` custom domain in
`wrangler.toml`), and it must match `telemetry.DefaultEndpoint` in `daemon/telemetry/telemetry.go`
— update whichever is wrong.

The old `oculus-telemetry.<subdomain>.workers.dev/ingest` address must keep serving. `DefaultEndpoint`
is a single URL with no fallback list, so every build already in the field posts to whatever address
it shipped with, forever. Telemetry stopping produces no error on either side — it just goes quiet —
so removing that route is a failure nobody is told about until a graph flatlines.

### The key

`INGEST_KEY` is optional and the check only engages when it is bound, which is what makes the
rollout order not matter: deploy the worker first and nothing breaks, ship the daemon first and
nothing breaks. Set it with `npx wrangler secret put INGEST_KEY`, matching `telemetry.IngestKey`.
It is a cost-and-noise filter, not authentication — the key ships inside a public binary and anyone
who wants it can read it out.

## Look at the data

`https://telemetry.ironrain.app/stats` — the browser prompts for a password (any username).
`?days=1|7|30|90` changes the window. It shows events and their failure counts, the distinct errors
behind them, version and platform spread, install counts, and p50/p95 duration per provider.

Telemetry nobody reads is just a bill, which is why this exists as a page rather than a documented
SQL query: the API below needs a shell, a token and remembered SQL, so in practice it gets read once.

Counts are `SUM(_sample_interval)`, never `count()`. Analytics Engine samples under load and reports
each surviving row's weight in `_sample_interval`; `count()` returns how many rows survived sampling,
which under-reports exactly when traffic is high enough to matter.

### Configuration

All optional, all skipped when unset, and none of them affect ingest. Set them as GitHub repository
secrets and the deploy workflow syncs them to the Worker — that way the configuration is reproducible
instead of remembered, and no terminal is involved.

| GitHub secret | Worker var | What it does |
|---|---|---|
| `TELEMETRY_INGEST_KEY` | `INGEST_KEY` | turns on the ingest key check; must equal `telemetry.IngestKey` in the daemon (`ir-telemetry-v1`) |
| `TELEMETRY_STATS_PASSWORD` | `STATS_PASSWORD` | the `/stats` password. Unset, `/stats` refuses rather than opening up |
| `CLOUDFLARE_ACCOUNT_ID` | `CF_ACCOUNT_ID` | already set for deploys; reused to read the dataset |
| `CF_ANALYTICS_TOKEN` | `CF_ANALYTICS_TOKEN` | API token with **Account → Account Analytics → Read** |

## Query the data directly

Use the Analytics Engine SQL API (needs an API token with Account Analytics Read):

```sh
curl "https://api.cloudflare.com/client/v4/accounts/<ACCOUNT_ID>/analytics_engine/sql" \
  -H "Authorization: Bearer <API_TOKEN>" \
  -d "SELECT blob1 AS event, blob3 AS error, count() AS n
      FROM oculus_telemetry
      WHERE double2 = 0
      GROUP BY event, error ORDER BY n DESC LIMIT 50"
```

Column mapping (Analytics Engine stores by position):

| Column   | Meaning        |
|----------|----------------|
| blob1    | event name     |
| blob2    | provider       |
| blob3    | scrubbed error |
| blob4    | daemon version |
| blob5    | os             |
| blob6    | arch           |
| blob7    | install id     |
| double1  | duration ms    |
| double2  | ok (1/0)       |
| double3  | client ts      |
