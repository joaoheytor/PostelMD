# ADR-0018: Account, session and permission lifecycle

- **Status:** Proposed
- **Date:** 2026-11-09

## Context
ADR-0005 defines how people get in. This ADR defines how access changes and ends:
- session expiry and rotation;
- password reset;
- suspension and deletion;
- identity linking;
- ownership transfer;
- immediate effect of permission changes, including on live SSE streams and event replay.

## Decision

### Sessions
- Stored server-side (SQLite). The cookie holds only a random session ID.
- **Idle timeout** 7 days; **absolute lifetime** 30 days (configurable).
- The session ID is **rotated** on login, privilege change (becoming admin), password change
  and identity linking.
- **Every session is revoked** on:
  - a password change or reset (the current session is re-issued);
  - suspension;
  - deletion;
  - admin "sign out everywhere";
  - restore from backup;
  - rotation of the session-signing key.
- Users can list and revoke their own sessions (device, IP, last seen).

### Account states
`invited` → `active` ⇄ `suspended` → `deleted`
- **Invites:** single-use, expire after 72 h, bound to the vault and role. They can be revoked.
- **Suspend** (admin): blocks login immediately, kills sessions and SSE streams, keeps the
  data, and suspends any vault remote credentials this user stewards (ADR-0010).
- **Delete** (admin, or self-service if enabled):
  - the user's sessions and passkeys are removed;
  - **personal vaults** must be transferred to another user or explicitly deleted (with a final
    backup offered);
  - git history keeps the author name (history is not rewritten);
  - acknowledgements and audit entries keep the user ID with the state marked `deleted`.
- **Password reset:**
  - with no email in v1, an admin issues a single-use reset link that expires after 24 h;
  - `postelmd reset-admin` covers the admin themselves;
  - a reset revokes all sessions. Before the reset completes, the user **reviews every
    registered passkey and TOTP device** and keeps or removes each one: a reset often follows
    a lost or stolen device.

### Credential changes
- **Re-authentication within the last 5 minutes** is required for every credential change:
  password, passkeys, TOTP, OIDC links, and git remote credentials (ADR-0010).
- **Removing a credential revokes every other session** of that user (the current one is
  kept), because one of them may have come from the credential being removed.

### Identity linking
- A user can link OIDC identities to their local account **while logged in** and after
  re-authenticating within the last 5 minutes.
- **No automatic linking by email:** an OIDC login whose email matches a local account does
  **not** log in as that account unless the identity was linked explicitly. This prevents
  account takeover through an IdP that doesn't verify emails.
- Unlinking requires at least one remaining login method.

### Memberships and ownership
- Every vault must keep **≥ 1 owner**. Removing or demoting the last owner fails with `409`.
  Ownership transfer is an explicit action.
- Only owners manage memberships. The instance admin can act as an owner in any vault
  (audit-logged).

### Immediate permission revocation
- Authorization is checked **per request**. Membership or role changes take effect on the next
  request; there is no role caching in sessions.
- **SSE:**
  - each stream is bound to a session; the event bus checks the subscriber's current
    permissions **when delivering each event**;
  - on membership removal, suspension or session revocation, the server sends an
    `access.revoked` event and closes the stream;
  - **replay** (`Last-Event-ID`) re-checks the current permission and never replays events
    from a vault the user can no longer read;
  - cursors are scoped to `(session, vault-set)` and carry the server's boot epoch; an epoch
    mismatch forces `resync`.
- Open documents of a revoked vault are closed by the client. Unsaved drafts for that vault are
  kept locally but cannot be saved, and the UI explains why.

### Browser data on logout or shared browsers
- IndexedDB drafts are namespaced `instanceID/userID/vaultID` (ADR-0003 §7).
- On **explicit logout**:
  - if drafts exist, the UI asks "Keep unsaved drafts on this device?";
  - the default is **delete**, after offering to save them first;
  - suspension or revocation wipes the local namespace on the next load.
- Drafts are **never purged automatically by age**. Drafts older than 30 days are flagged in
  the orphaned-drafts list (ADR-0003 §7), and the user decides; logout, suspension and
  revocation above are the only cases that remove drafts.
- A "shared computer" checkbox at login disables draft persistence (memory only).

### Trusted-header and OIDC sessions
- Trusted-header identities create normal server sessions with the same lifecycle. The
  header is re-checked on each request and a change of identity ends the session.
- OIDC: the PostelMD session doesn't outlive `POSTELMD_OIDC_MAX_SESSION` (default 24 h)
  without re-authentication. Back-channel logout support is post-1.0.

## Consequences
- Positive: access can be removed instantly and verifiably; no account takeover through email
  matching; shared browsers are handled.
- Negative: more admin UI and more tests (lifecycle × SSE × replay matrix).

## Alternatives considered
| Option | Why not |
|---|---|
| Stateless JWT sessions | Can't be revoked immediately |
| Auto-link OIDC by email | A known account-takeover vector |
| Permission check only at SSE connect | A revoked user would keep receiving events |
