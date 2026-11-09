# ADR-0005: Pluggable auth providers, local accounts by default

- **Status:** Proposed
- **Date:** 2026-11-09

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
    - login rate limit and lockout;
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

  The `credentials` key wraps per-record data keys (envelope encryption).
- **Versioning:** every key has an ID. Ciphertexts and tokens carry the key ID.
- **Rotation:**
  - `postelmd keys rotate <purpose>` adds a new active key and keeps the old ones for
    verification and decryption;
  - re-encryption of credentials runs in the background, after which old keys can be retired;
  - rotating `session` revokes all sessions.
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
