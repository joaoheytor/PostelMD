# ADR-0039: A held credential is retried at most every five seconds

- **Status:** Accepted (with the fix for [#189](https://github.com/joaoheytor/PostelMD/issues/189), the first code that depends on it)
- **Date:** 2026-11-17

## Context
Supersedes the **"Held in memory, saved later"** bullet of
[ADR-0038](0038-a-credential-change-that-cant-be-saved.md), which says: "While a change is
held only in memory, every login and every session check tries again to save the current
credential (one small atomic write, under the credential's mutex; no background
goroutine)." The rest of ADR-0038 stands, including the rest of that bullet (the first
success ends it and is logged; one warning, not repeated).

ADR-0038 accepted that cost because "a write that fails for lack of space is quick". The
security re-review of [#188](https://github.com/joaoheytor/PostelMD/pull/188) found that it
isn't always ([#189](https://github.com/joaoheytor/PostelMD/issues/189)): with `EIO` or an
`fsync` that hangs, each write can be slow, and since it runs under the credential's mutex,
every request (unauthenticated ones and each SSE check included) queues behind it. The
same review found that a temp file whose `fsync` failed was left behind, one per retry;
the store now removes it, as it already did after a failed write (no decision changes:
ADR-0003 §5 never retries a failed `fsync`, and the startup sweep still removes what a
crash leaves).

## Decision
- **Held in memory, saved later, at most every five seconds.** While a change is held only
  in memory, a login or a session check tries again to save the current credential (one
  small atomic write, under the credential's mutex; no background goroutine) only when at
  least five seconds have passed since the last failed save, the change's own included.
  The requests in between don't touch the disk. The first success ends it and is logged.
  The first failure logs one warning that a security change is held only in memory, with
  the error described without its text (`errattr.Pathless`, CLAUDE.md rule 15); it isn't
  repeated until a later change fails again after a successful save.
- The interval is measured on the service's clock (monotonic in production, injected in
  tests). A password change or "log out everywhere" still writes at once, whatever the
  interval: the user asked for it.

## Consequences
- Positive:
  - On a failing disk, at most one request every five seconds waits on a write; the others
    see only the in-memory credential, which already fails closed (ADR-0038).
  - A slow or hanging write no longer stalls every request in turn.
- Negative / costs:
  - Once space is freed, a held change is saved up to five seconds later than before. A
    restart in that window still undoes it, as ADR-0038 already says of any held change.
  - One request in five seconds still waits on a slow write under the mutex, and the
    requests that arrive meanwhile queue behind it. A hanging `fsync` would need a
    background save, which ADR-0038 rejected for a reason that stands here.
- Follow-ups: v0.3 moves the credential into SQLite (ADR-0018), where the same bound
  applies to a failed commit's retry.

## Alternatives considered
| Option | Why not |
|---|---|
| Keep retrying on every request (ADR-0038 as written) | A slow `EIO` or a hanging `fsync` makes every request wait on the mutex in turn (#189). |
| Retry with exponential backoff | More state for little gain: a five-second floor already bounds the cost, and backoff delays the save once the disk recovers. |
| Retry outside the mutex | Two saves could race and the older credential land last; the write must stay ordered with changes. |
| Retry in a background goroutine | Another lifecycle to cancel and test (ADR-0038); the next request after five seconds is soon enough. |
