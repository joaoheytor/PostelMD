# ADR-0005: Pluggable auth providers, local accounts by default

- **Status:** Proposed
- **Date:** 2026-11-09
- **MVP scope (ADR-0020):** the MVP has one local account with a password; passkeys, invites, OIDC and trusted-header auth come later.

## Context
PostelMD is multi-user and will be public. Many self-hosters, including the maintainer's
homelab today, run **no identity provider and no reverse proxy**. FrankMD shipped with auth
off by default and a single shared token.

## Decision
- An `auth.Provider` interface; a user can link several identities.
  - **`local`** (always available, default):
    - passwords hashed with argon2id, with parameters sized for small nodes (~64 MB, 1–2
      passes);
    - WebAuthn passkeys (go-webauthn); optional TOTP;
    - **login backoff, never a lockout.** A hard lockout lets anyone lock out the admin by
      guessing. After 5 failures, each further attempt for that account waits twice as long
      (1 s, doubling, capped at 15 min), and a per-source-IP limit applies on top. A success
      resets it. Passkey logins aren't slowed by password failures, so the real user can
      always get in with a passkey;
    - **no open signup**: users join through admin-issued invite links.
  - **`oidc`** (optional): Authorization Code + PKCE (coreos/go-oidc). Tested against Pocket ID,
    Authentik, Keycloak and tsidp. A groups claim can map to the instance-admin role. **Admin status
    has two independent sources:**
    - `admin_local`, granted by an admin in the UI or CLI. OIDC never changes it.
    - `admin_oidc`, derived from the configured group. When a mapping is configured it is
      **authoritative** for this source:
      - recomputed on every OIDC login, and revalidated through a token refresh at least
        every `POSTELMD_OIDC_ROLE_TTL` (default 1 h);
      - if the group is gone, or the IdP can't be reached when the TTL expires, `admin_oidc`
        is removed and the session is rotated without admin privileges;
      - so the **maximum stale-privilege window is the TTL**.

    Effective admin = `admin_local` OR `admin_oidc`. Demotion is audit-logged. This must be
    finalized before OIDC ships (v0.5).
  - **`trusted-header`** (opt-in; contract finalized before it ships, post-MVP):
    - accepted **only** when the TCP peer is in `POSTELMD_TRUSTED_PROXIES`;
    - `X-Forwarded-For` is parsed right-to-left, skipping trusted hops;
    - the identity header name is configured explicitly, and the request is rejected if that
      header is present from an untrusted peer;
    - the proxy must strip and replace the header (documented per proxy: Tailscale Serve,
      Authelia, oauth2-proxy);
    - identities map to users only through explicit linking (ADR-0018);
    - the Helm chart ships a NetworkPolicy so the pod is reachable only from the proxy.
- **Sessions:**
  - stored server-side in SQLite (scs);
  - cookies are `HttpOnly` and `SameSite=Lax`; `Secure` and the `__Host-` prefix are added
    when the request arrived over TLS;
  - CSRF defense: Origin check + token + a custom header required on mutations.
- **Host allowlist (DNS rebinding).** A request is served only when its `Host` is
  `localhost` (or a name under `.localhost`), an IP literal, or a name listed in
  `allowed_hosts` (`POSTELMD_ALLOWED_HOSTS`, exact names, no wildcards); anything else gets
  `421 Misdirected Request` naming the setting, and a rate-limited warning with the host
  escaped and truncated. A site the user visits can point its own name at the server's
  address, but its requests then carry that name. Every response also carries
  `Cross-Origin-Opener-Policy: same-origin` and `Cross-Origin-Resource-Policy: same-origin`
  (the diagram frame's script is `cross-origin`: the sandboxed frame loads it from an opaque
  origin, ADR-0021).
- **Hashing is a scarce resource.** One argon2id at a time (~64 MB each). A request that will
  be refused (an invalid setup token, setup already done, a source still waiting) is refused
  before it hashes, and the backoff is checked again once an attempt may hash, so parallel
  guesses from one source can't run more hashes than sequential ones. The failure that
  starts a wait carries `Retry-After` with its `401`, like the `429` that follows.
- **First run:** see ADR-0009 for the setup token and the headless bootstrap.
- **Authentication cannot be disabled.** There is no "no-auth" or single-user bypass mode.
- The session, account and permission lifecycle (expiry, rotation, suspension, linking,
  revocation) is defined in ADR-0018.

### Keys and credentials
- A **key ring** at `/data/.postelmd/keys/` is generated on first run, or supplied from
  outside (a K8s Secret or `_FILE`). Each key is used for exactly one purpose:

  | Key | Purpose |
  |---|---|
  | `session` | HMAC for session and CSRF tokens |
  | `credentials` | AES-256-GCM encryption at rest for git remote tokens, SSH keys and OIDC client secrets |
  | `webhook` | Signing outbound webhooks (post-MVP) |
  | `fingerprint` | HMAC for secret-finding fingerprints (ADR-0010) |
  | `audit` | HMAC chain of the `file:` audit sink (architecture §9) |

  The `credentials` key wraps per-record data keys (envelope encryption).
- **Versioning:** every key has an ID. Ciphertexts and tokens carry the key ID.
- **Rotation:**
  - `postelmd keys rotate <purpose>` adds a new active key and keeps the old ones for
    verification and decryption;
  - re-encryption of credentials runs in the background, after which old keys can be retired;
  - rotating `session` revokes all sessions.
- **Crash safety of rotation and retirement** (CLAUDE.md rule 14). Both hold the instance
  backup barrier (ADR-0017) and write a durable intent first (op ID, purpose, key ID, phase).
  - **Rotate:** write the new key to a temp file in `keys/`, `fsync`, rename it to
    `<purpose>/<key-id>.key` (no-clobber), `fsync` the directory; then, in one transaction,
    make it the active key (and revoke all sessions for `session`) and clear the intent.

    | Key file | Active key in the DB | Action |
    |---|---|---|
    | Absent | Old | Not applied: remove any temp, drop the intent |
    | Present, parses, ID matches | Old | Roll forward: activate it in one transaction with clearing the intent |
    | Present | New | Applied: drop the intent |
    | Anything else (absent while the DB names it, unreadable) | | Interference: refuse to start, naming the key; the admin restores the key ring from a backup |
  - **Re-encryption** needs no intent: each record is re-wrapped in its own transaction, and a
    query for records still carrying the old key ID is the progress, so it resumes after a
    crash.
  - **Retire:** in one transaction, check that **no** record, token or webhook references the
    key and mark it retired; only then delete its file and `fsync` the directory. Recovery:
    retired with the file present → delete it; retired and absent → drop the intent; not
    retired → drop the intent (nothing was touched). A key is never deleted while anything
    references it.
  - Keys supplied from outside (a K8s Secret or `_FILE`) are read-only to PostelMD: they are
    rotated by changing the Secret, and startup refuses if the active key ID is missing.
- **Persistence:** keys are never regenerated on restart. They are included in encrypted
  backups unless supplied from outside (ADR-0017).
- **Loss:** losing `credentials` makes the stored remote credentials unrecoverable. Users
  re-enter them, and notes are unaffected.
- **Compromise:** rotate every purpose, revoke all sessions and rotate the remote credentials
  at the providers (runbook in `docs/operations/`).

## Consequences
- Positive: secure with zero infrastructure, and SSO for those who have it.
- Negative: we own password storage and passkey flows, which need careful tests (ASVS L2).
- Plain-HTTP LAN installs: passkeys and `Secure` cookies need HTTPS or localhost, so password
  login still works and the UI shows an "insecure transport" banner.

## Alternatives considered
| Option | Why not |
|---|---|
| A `--insecure-single-user` no-auth mode | Contradicts "auth always on"; one footgun flag gets copy-pasted into tutorials |
| OIDC only | Excludes the zero-infra `docker run` users |
| Forward-auth headers only | Spoofable if the Service is reached directly; no identity inside the app |
| Single shared token (FrankMD) | Not multi-user; no per-user authorization |
