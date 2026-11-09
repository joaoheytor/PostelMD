# ADR-0009: Zero-infra contract: one image, one volume, secure first run

- **Status:** Proposed
- **Date:** 2026-11-09 (revised after design review)

## Context
People with little infrastructure must be able to run PostelMD with one `docker run`: no
Compose, no external DB, no IdP, no reverse proxy. The same artifact must run in K8s.

## Decision

### One image
A multi-arch image (amd64 + arm64), based on `gcr.io/distroless/static:nonroot`, built from the
same binary GoReleaser ships. Its `HEALTHCHECK` is `["/postelmd","healthcheck"]`, because the
image has no shell or curl.

### One volume
`/data` holds everything (layout in `architecture.md` §3). The canonical command, also used in
the README and all docs:

```sh
mkdir -p ~/postelmd
docker run -d --name postelmd --restart unless-stopped --stop-timeout 60 \
  -p 127.0.0.1:8080:8080 \
  --user "$(id -u):$(id -g)" \
  -v ~/postelmd:/data \
  docker.io/joaoheytor/postelmd:0.1
docker logs postelmd   # one-time setup URL
```

- `--user` is **required for bind mounts**: the image cannot fix ownership, because it has no
  shell and doesn't run as root. Named volumes work without it.
- If `/data` isn't writable, the app exits with an error that prints the exact fix.
- `--stop-timeout 60`: Docker's default (10 s) is shorter than the 45 s shutdown deadline
  (architecture §8). Compose files use `stop_grace_period: 60s`; the Helm chart uses
  `terminationGracePeriodSeconds: 60`.
- The tag is the **minor** version (`:0.1`). Before 1.0 every minor may break, and upgrades go
  one minor at a time (ADR-0015), so there's no floating `:0` tag.
- Named volumes work without `--user` only because the image ships `/data` owned by UID/GID
  65532 (an image requirement, checked by the `image` CI job).
- **Using an existing notes folder:** add a second mount at a vault path
  (`-v ~/notes:/data/vaults/notes`). Setup then offers it as the admin's vault, subject to the
  adoption checks in ADR-0006. If `.md` files are found **at the root of `/data`**, setup stops
  and explains how to mount them as a vault instead. App state must never live inside a vault.
- One process per `/data`, enforced by the instance lock (ADR-0014 §1).

### Secure first run
- **Pre-setup routing.** This is the one definition, which the roadmap repeats:

  | Path | Response |
  |---|---|
  | `/setup` and `/api/v1/setup` | Served |
  | `/healthz`, `/readyz` | Served |
  | `/assets/*` (static files needed by the setup page) | Served |
  | HTML navigation to any other path | `303` redirect to `/setup` |
  | Any other `/api/*` path | `503` |
  | `/metrics` | `404` (as after setup without a token; see Metrics below) |

- A single-use **setup token** (expires after 30 min) is printed to stdout and logs only.
- **Headless bootstrap:** `POSTELMD_ADMIN_USER` + `POSTELMD_ADMIN_PASSWORD_FILE` create the admin
  and skip the token.
- `postelmd reset-admin` prints a new setup token for the existing admin.
- **Before an admin exists**, an expired or lost setup token is replaced in two ways: every
  start prints a fresh one, and `postelmd reset-admin` prints a fresh one too (online or
  offline). Each new token invalidates the previous one. Both need container or volume
  access, which is the same trust as reading the logs.
- **Metrics.** `/metrics` exposes operational detail, so it is never open on the main
  listener:
  - on the main listener it requires `Authorization: Bearer <token>`
    (`POSTELMD_METRICS_TOKEN_FILE`, compared in constant time), and returns `404` when no
    token is configured;
  - or `POSTELMD_METRICS_LISTEN` (for example `:9090`) serves only `/metrics` on a separate
    port without a token, for in-cluster scraping behind a NetworkPolicy (the Helm chart's
    default). `docker run` exposes only the main port, so it isn't reachable by default.
- **There is no option to run without authentication** (ADR-0005).

### Transport
- `X-Forwarded-*` headers are ignored unless the request comes from `POSTELMD_TRUSTED_PROXIES`
  (empty by default).
- Plain HTTP from a non-loopback client produces a permanent UI banner, a log warning and a
  metric.
- Optional built-in TLS: cert/key files, or `POSTELMD_TLS=self-signed`.

### Configuration and limits
- **Precedence:** flags > `POSTELMD_*` env (with a `_FILE` variant for every secret) >
  `/data/.postelmd/config.yaml` > defaults. Running with zero config works.
- **Default limits:**
  - notes: 5 MB;
  - uploads: 25 MB (`POSTELMD_MAX_UPLOAD_MB`), content-sniffed against an allow-list (PNG,
    JPEG, GIF, WebP, PDF; SVG is served only as an attachment);
  - request headers: 64 KB;
  - 20 concurrent SSE streams per user;
  - 10 login attempts per 15 min per IP and per account.

### Subcommands: online vs offline
The instance lock means a second process can never write to `/data` while the server runs. So
administrative commands are split:

| Kind | Commands | How they run |
|---|---|---|
| **Online** (delegate to the running server) | `backup`, `reindex`, `reset-admin`, `keys rotate`, `vault lock/unlock`, `plugin disable/import-revocations` | Connect to the admin socket `/data/.postelmd/admin.sock` (mode 0600, owned by the app user). Being able to open the socket is the authentication: you can only do it from inside the container (`docker exec postelmd /postelmd …`, `kubectl exec`) or as the same Unix user. Every call is audit-logged as `actor: cli`. |
| **Online or offline** | Same as above | If no server holds the instance lock, the command takes the lock and runs directly (offline). |
| **Offline only** | `restore`, `db rollback` (database-only rollback, ADR-0015) | Refuse if the instance lock is held. |
| **Stateless** | `healthcheck`, `version`, `audit verify` | Only read HTTP, a given file (with the `audit` key), or nothing. |

### Shutdown
The shutdown sequence and its deadlines are in architecture §8. There are no log or debug HTTP
endpoints; logs go to stdout.

### CI gate
A `docker run` smoke test on the release digest, for both architectures:
1. boot;
2. read the token from the logs;
3. complete setup;
4. save a note;
5. restart;
6. verify the note and the session persist;
7. start a second container on the same `/data` and confirm it refuses to start.

## Consequences
- Positive: the lowest possible barrier to entry while staying secure; one artifact everywhere.
- Negative: we own TLS-awareness logic and the setup flow; distroless makes debugging harder
  (use `kubectl debug` or ephemeral containers).

## Alternatives considered
| Option | Why not |
|---|---|
| Print a generated admin password to the logs | Stays valid forever, in log storage |
| Compose as the minimum | Fails the one-command requirement |
| Mount the notes folder as `/data` directly | Mixes app state into a vault (ADR-0006) |
| Auth off on localhost | Port mapping makes "localhost" ambiguous inside a container |
