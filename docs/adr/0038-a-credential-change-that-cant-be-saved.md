# ADR-0038: A credential change that can't be saved

- **Status:** Accepted (with [#188](https://github.com/joaoheytor/PostelMD/pull/188), the first code that depends on it); its "Held in memory, saved later" bullet is superseded by [ADR-0039](0039-a-held-credential-is-retried-at-most-every-five-seconds.md)
- **Date:** 2026-11-17

## Context
Supersedes, when `auth.json` can't be saved, this sentence of
[ADR-0020](0020-mvp-scope.md)'s "No database" section: "Changing the password or 'log out
everywhere' bumps the epoch, which revokes every session." ADR-0020 says the credential is
replaced with the atomic write path, but not what happens when that write fails. The rest
of ADR-0020 stands, including the sentence itself whenever the save succeeds.

The first implementation saved first and changed the credential in memory only after a
successful save. When the save failed (a full disk, a read-only volume), "log out
everywhere" and a password change answered `500` and changed nothing: a stolen session
cookie, and after a password change the old password, stayed valid
([#187](https://github.com/joaoheytor/PostelMD/issues/187)). That fails open, at the moment
the owner is trying to cut an attacker off.

Making the change in memory anyway raises three more problems (the security review of
#188):
- a restart reads the older `auth.json` and undoes the change;
- epochs count up by one, so after a restart the first saved bump can land on the epoch of
  a cookie issued while the change was held in memory, and bring that revoked cookie back;
- after a restart the old password works again, and the new one doesn't.

## Decision
- **Fail closed.** When a password change or "log out everywhere" can't save `auth.json`,
  the new credential (the bumped epoch, and for a password change the new password's hash)
  becomes current in memory anyway. Every earlier session, and the old password, stop
  working at once. No new session is issued: the caller logs in again.
- **An epoch is never reused.** The in-memory epoch of a change that wasn't saved is moved
  forward again by `1 +` a random 32-bit number (`crypto/rand`). After a restart, the bumps
  from the saved epoch would have to pass about 2^31 values, on average, before reaching
  the epoch of a cookie issued in between; cookies expire after 7 days.
- **Held in memory, saved later.** While a change is held only in memory, every login and
  every session check tries again to save the current credential (one small atomic write,
  under the credential's mutex; no background goroutine). The first success ends it and is
  logged. The first failure logs one warning that a security change is held only in
  memory, with the error described without its text (`errattr.Pathless`, CLAUDE.md
  rule 15); it isn't repeated until a later change fails again after a successful save.
- **Audited.** The change's audit record is written either way; one that wasn't saved
  carries `saved=false` (`sessions.revoked saved=false`, `password.changed ... saved=false`).
- **The response says what to do.** The request answers `500`, clears the caller's (already
  revoked) cookie, and logs the error without its text. Its message says the change took
  effect but wasn't saved, so a restart would undo it, and to free space, log in and repeat
  it. For a password change it adds: "if the server restarted before you repeated it, the
  old password is back: log in with it and change the password again".
- **A failed directory `fsync`** after a successful rename is reported the same way, as
  "not saved", although `auth.json` then probably holds the change. That errs on the safe
  side: the change is in effect in memory and is saved again on the next check.
- **`postelmd reset-password` is unchanged.** It runs with the server stopped, so there is
  no in-memory credential; a failed save fails the command, changes nothing (or, after a
  failed directory `fsync`, probably the whole change), and writes no audit record.

## Consequences
- Positive:
  - A revocation the owner asked for takes effect even when the disk is full; a stolen
    cookie stops working at once.
  - A held change saves itself once space is freed, usually before anyone repeats it.
- Negative / costs:
  - Until it is saved, a restart undoes the change: the old sessions and the old password
    are back. The response, the warning and the audit record say so; the operator must
    free space and the user repeat the change.
  - While a change is held, every request makes one failing write under the credential's
    mutex. Requests serialize on it, but a write that fails for lack of space is quick.
- Follow-ups: v0.3 moves the credential into SQLite (ADR-0018), where a failed commit
  needs the same rule.

## Alternatives considered
| Option | Why not |
|---|---|
| Keep the old behaviour: change nothing when the save fails | Fails open: the stolen cookie stays valid exactly when the owner tries to revoke it (#187). |
| Revoke in memory, but count epochs up by one | A restart then reuses an epoch a revoked cookie carries (the #188 review's scenario). |
| Refuse every session (lock everyone out) until the save succeeds | Locks out the owner too, with no way to log in to fix it from the app. |
| Retry the save in a background goroutine | Another lifecycle to cancel and test; the next request is soon enough, and costs one small write. |
| Shut the server down on a failed save | A full disk would turn a revocation into an outage, and the restart would undo the revocation anyway. |
