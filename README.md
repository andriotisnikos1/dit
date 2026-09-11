# dit — Docker Image Tracker

`dit` watches Docker/OCI image references across public and private registries,
detects **digest drift** on pinned tags and **newly published tags** matching a
glob, and notifies over email and/or ntfy.

Two deliverables, one Go module:

| Binary | Where it runs | What it owns |
|---|---|---|
| `dit` | your machine | Nothing but `~/.config/dit/config.yaml`. A pure remote API client. |
| `dit-server` | a server | SQLite, registry credentials, the check scheduler, notification delivery. |

The server is the single source of truth. The CLI is stateless: every watch,
channel, credential and event lives on the server.

---

## Quickstart

### 1. Run the server

```bash
cp .env.example .env

# The shared secret the CLI authenticates with. Required.
echo "DIT_API_TOKEN=$(openssl rand -base64 32)" >> .env
# The key that seals registry credentials and channel secrets at rest.
echo "DIT_MASTER_KEY=$(openssl rand -base64 32)" >> .env

docker compose up -d
curl -s localhost:8080/healthz
# {"status":"ok","version":"dev"}
```

Or without Docker:

```bash
make build
export DIT_API_TOKEN=$(openssl rand -base64 32)
export DIT_MASTER_KEY=$(openssl rand -base64 32)
./bin/dit-server
```

### 2. Point the CLI at it

```bash
./bin/dit config set-server http://localhost:8080
./bin/dit config set-token --password-stdin <<< "$DIT_API_TOKEN"
./bin/dit status
```

### 3. Watch something

```bash
# Pinned tag: report digest drift.
./bin/dit watch add nginx:1.27

# Several tags at once: one watch per tag.
./bin/dit watch add ghcr.io/owner/app --tag v1 --tag v2

# Pattern: report tags that appear matching a glob.
./bin/dit watch add ghcr.io/owner/app --pattern 'v1.*'
```

### 4. Add a notification channel

```bash
./bin/dit channel add ntfy --name ops --topic my-alerts
./bin/dit channel test ops

./bin/dit channel add email --name mail \
  --smtp-host smtp.example.com --smtp-port 587 --starttls \
  --from dit@example.com --to ops@example.com \
  --username dit --password-stdin

# Watches with no explicit subscription notify the channels flagged default.
./bin/dit channel default ops
```

---

## Private registries

`dit watch add` probes the repository anonymously first. When the registry
answers 401/403, the server replies `428 credentials_required`, the CLI prompts
for a username and a hidden password, stores them, and replays the request:

```
$ dit watch add ghcr.io/owner/private-app:v1

Registry ghcr.io requires authentication.
Username: andriotis
Password:
Stored credentials for ghcr.io.
Created 1 watch(es) for ghcr.io/owner/private-app:v1
```

Non-interactively (CI, scripts), store them up front instead:

```bash
echo "$GHCR_TOKEN" | dit creds set ghcr.io --username andriotis --password-stdin
```

> **Docker Hub answers 401 for both private and nonexistent repositories.** The
> `428` message says so: a 401 does not prove the repository is private, only
> that anonymous access failed. A credential failure *after* supplying
> credentials is reported explicitly as `unauthorized`.

Credentials are sealed with NaCl secretbox and are **write-only over the API**:
`GET /api/v1/registries/{host}/credentials` returns metadata, never the secret.

---

## CLI reference

Config precedence: `--server`/`--token` flags → `DIT_SERVER_URL`/`DIT_API_TOKEN`
environment → `~/.config/dit/config.yaml` (written `0600`, path overridable via
`DIT_CONFIG`).

Global flags: `--server`, `--token`, `--config`, `-o/--output table|json|yaml`,
`--no-color`, `--verbose`, `--non-interactive`.

```
dit version
dit config show|path|set-server <url>|set-token
dit status

dit watch add <image>[:tag] [--tag <t>]... [--pattern <glob>] [--channel <name>]...
                      [--no-notify-on-failure] [--disabled]
dit watch list [--enabled|--disabled] [--registry <host>] [--search <q>] [--limit N]
dit watch show <id>
dit watch check <id>
dit watch enable|disable <id>
dit watch channels <id> <channel>...
dit watch events <id> [--limit N]
dit watch rm <id> [--yes]

dit channel add email --name <n> --smtp-host <h> [--smtp-port 587] [--starttls|--implicit-tls]
                      --from <a> --to <a>... [--username <u>] [--password-stdin]
dit channel add ntfy  --name <n> [--url https://ntfy.sh] --topic <t> [--token <t>]
                      [--priority default] [--tags <t>]
dit channel list | show <id> | test <id> | default <id> [--off] | rm <id>
dit channel enable|disable <id>

dit creds set <registry> [--username <u>] [--password-stdin] [--kind basic|token]
dit creds list
dit creds rm <registry>

dit events [--limit N] [--watch <id>] [--type <type>]
dit notifications [--limit N]
dit notifications retry <id>
```

`--tag` is repeatable and creates one watch per tag. Output defaults to aligned
`tabwriter` tables; `-o json` emits raw API responses for scripting.

---

## REST API

Base path `/api/v1`. Auth is `Authorization: Bearer <DIT_API_TOKEN>`, compared
in constant time. `GET /healthz` is unauthenticated.

| Method | Path | Purpose |
|---|---|---|
| GET | `/healthz` | Liveness (no auth) |
| GET | `/api/v1/status` | Version, uptime, watch counts, last/next tick |
| POST | `/api/v1/watches` | Create watch (runs the privacy probe) |
| GET | `/api/v1/watches` | List; filters `enabled`, `registry`, `q`, pagination |
| GET/PATCH/DELETE | `/api/v1/watches/{id}` | Read / enable-disable + channels + notify_on_failure / delete |
| POST | `/api/v1/watches/{id}/check` | Force a check, return the diff |
| GET | `/api/v1/watches/{id}/events` | Per-watch history |
| GET | `/api/v1/events` | Global event feed; filters `watch`, `type` |
| GET/POST | `/api/v1/channels` | List / create |
| GET/PATCH/DELETE | `/api/v1/channels/{id}` | Manage; `PATCH` sets `is_default`, `enabled` |
| POST | `/api/v1/channels/{id}/test` | Send a test notification |
| GET | `/api/v1/registries` | Known registries + whether credentials are stored |
| PUT/GET/DELETE | `/api/v1/registries/{host}/credentials` | Set / metadata-only read / delete |
| GET | `/api/v1/notifications` | Delivery log |
| POST | `/api/v1/notifications/{id}/retry` | Re-deliver a failed notification |

Errors use one envelope:

```json
{"error":{"code":"credentials_required","message":"…","registry":"ghcr.io"}}
```

| Status | Code | Meaning |
|---|---|---|
| 400 | `bad_request` | Malformed request |
| 401 | `unauthorized` | Missing or wrong API token |
| 404 | `not_found` | Unknown resource |
| 409 | `conflict` | Duplicate watch or channel name |
| 422 | `validation_failed` | Invalid input |
| **428** | `credentials_required` | **The prompt-and-retry trigger** |
| 502 | `upstream_error` | The registry failed |

---

## How checks work

- One scheduler goroutine polls a global interval (default `15m`) with ±10%
  jitter to avoid a thundering herd, then fans due watches over a bounded worker
  pool (`check_concurrency`, default `4`) with a per-check timeout
  (`check_timeout`, default `30s`).
- Checks are **serialized per watch**, so a manual `dit watch check` landing
  mid-tick cannot produce duplicate notifications.
- **Tag watch** — resolve `repository:tag` to a digest. The first successful
  check records a baseline and stays silent; a later change emits
  `digest_changed`.
- **Pattern watch** — list tags, glob-filter (case-sensitive `path.Match`), diff
  against the stored baseline. New tags emit a single `new_tags` event carrying
  the list and their digests. Tags that disappear are pruned silently.
- **Failures** — `unauthorized` (401/403), `not_found` (404) and transient
  network/5xx are distinguished. `consecutive_failures` increments and
  `next_attempt_at` backs off exponentially (base = interval, cap 24h, reset on
  success). `check_failed` fires on the **first** failure only, and
  `check_recovered` on return to health, so a watch that stays broken does not
  spam the interval. Delivery of failure notifications is suppressed when
  `notify_on_failure` is false.
- **Baselines** — a watch records its baseline on its first *successful* check,
  tracked explicitly in `baseline_at`. Inferring it from the data a check
  produced would be wrong twice over: a pattern watch created before any
  matching tag exists legitimately records an empty tag set (and would
  re-baseline forever, never reporting the first tags to appear), and a tag
  watch whose early checks all failed has no digest yet (and would report a
  phantom change from nothing).
- **Delivery** — up to `notify_attempts` attempts with backoff, each recorded in
  `notifications` (`pending` → `sent`/`failed`). A failed delivery never affects
  the stored check result and can be re-sent with
  `POST /api/v1/notifications/{id}/retry` or `dit notifications retry <id>`.

Registry access goes through one interface, so tests substitute a fake:

```go
type Client interface {
    Probe(ctx context.Context, repo name.Repository) (ProbeResult, error)
    ResolveDigest(ctx context.Context, ref name.Reference, auth authn.Authenticator) (v1.Hash, error)
    ListTags(ctx context.Context, repo name.Repository, auth authn.Authenticator) ([]string, error)
}
```

It is backed by `github.com/google/go-containerregistry`; image refs are
normalised so `nginx:1.27` ⇒ registry `index.docker.io`, repository
`library/nginx`.

---

## Security

- Registry secrets and channel secrets (SMTP password, ntfy token) are sealed
  with `golang.org/x/crypto/nacl/secretbox`; `nonce||ciphertext` is base64 in
  SQLite. Sealed values carry a `sealed:` marker so plaintext and ciphertext can
  never be confused.
- The master key comes from `DIT_MASTER_KEY` (base64, 32 bytes) or
  `DIT_MASTER_KEY_FILE`. When neither is present, one is generated into
  `<data-dir>/master.key` (`0600`) and a loud warning is logged. **Back that key
  up**: losing it makes every stored secret unrecoverable.
- Secrets are write-only over the API. `GET` endpoints return metadata and a
  `has_secret` flag, never values.
- The API token is compared in constant time over SHA-256 digests. Requests are
  logged without the `Authorization` header. The CLI config file is `0600`.

---

## Development

```bash
make build        # both binaries into bin/
make test         # go test ./...
make test-race    # with the race detector (needs cgo)
make lint         # gofmt check + go vet
make docker       # container image
make help         # list targets
```

CI (`.github/workflows/ci.yml`) runs `gofmt -l`, `go vet`, `go build` and
`go test ./... -race -cover`, then builds both binaries and smoke-tests the CLI.

### Layout

```
cmd/dit/            CLI entrypoint
cmd/dit-server/     server entrypoint
internal/apitypes/  shared request/response DTOs (single source of truth)
internal/apiclient/ typed HTTP client used by the CLI
internal/cli/       cobra command tree, prompts, table/JSON/YAML rendering
internal/config/    YAML + env config loading for both binaries
internal/crypto/    secretbox seal/open, master key resolution
internal/store/     SQLite: embedded migrations, schema, queries
internal/registry/  OCI client: parse, resolve digest, list tags, probe
internal/notify/    Channel interface + email (SMTP) and ntfy senders
internal/check/     check engine: scheduling, diffing, events, delivery
internal/server/    HTTP routing, auth middleware, handlers
internal/testutil/  fixtures shared by the test suites
migrations/*.sql    embedded via go:embed
```

---

## Known limitations

- The privacy probe cannot distinguish "private repository" from "nonexistent
  repository" on registries that return 401 for both (Docker Hub does). The
  `428` message says so, and a credential failure after supplying credentials
  reports `unauthorized` explicitly.
- Globs use `path.Match`, whose `*` does not cross `/` — which is what tag names
  need.
- A watch tracks tags, not digests: adding one for a digest-addressed image is
  rejected, because an immutable digest cannot drift.
- No `/metrics` endpoint; `GET /healthz` is the only unauthenticated surface.
