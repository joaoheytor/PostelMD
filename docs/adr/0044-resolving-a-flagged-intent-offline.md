# ADR-0044: Resolving a flagged intent offline

- **Status:** Accepted
- **Date:** 2026-11-18

## Context
[ADR-0033](0033-v02-intents.md) flags an intent when recovery finds interference. "A flagged
intent stays until the operator decides": the sweep spares its temp, and the scan leaves the
paths it names alone. Nothing let the operator decide, though. The web app part,
[#199](https://github.com/joaoheytor/PostelMD/issues/199), is later. Until then, the only way
out was `db rebuild`, which replaces every file ID, or editing SQL by hand.

This ADR adds the offline way out. It extends ADR-0033 and supersedes nothing in it.

## Decision
- **Who may end an intent.** `postelmd intents resolve <op-id>` ends a **flagged** intent
  only. An open intent belongs to the start's recovery and is refused. Resolve runs
  **offline**:
  - under the instance lock (ADR-0014), so it refuses while a server runs;
  - after the vault check a start runs (`ids.CheckVault` at the build's format). Without that
    check, a wrong mount or a vault the marker doesn't name would let resolve remove a temp
    that isn't the intent's, and end the intent while the real temp stays. The next correct
    start would sweep that temp, and the edit would be lost. A failed check refuses, naming
    `vault claim` or the mismatch.

  The op ID must have its kind's format (32 lower-case hex digits; a delete's trash stamp)
  and no control character. Resolve checks it before reading anything, and again once it
  knows the kind.
- **Listing.** `postelmd intents list` prints every intent under the instance lock. It reads
  only `app.db` and `recovery/`, so it skips the vault check, which may write a marker. An op
  ID that fails its format check prints as `<invalid op ID>`, and a kind or path holding a
  control character is quoted, so `app.db` can't send control sequences to the terminal.
- **What ending does, per kind.**
  - **save:** the temp is removed only once a copy of it is durable in `recovery/<op-id>/`.
    A copy with the same bytes counts as that copy; otherwise the temp is copied first, as
    `temp-1`, `temp-2` and so on when a `temp` with other bytes is already there. Then the
    intent row goes. Nothing else began with a save. A temp that isn't a regular file is left
    for the next start's sweep.
  - **a kind this build doesn't know** (written by a later build, then rolled back): only the
    row goes, with no vault I/O. The command says so. This build can't know what the later
    build began, so it touches nothing on disk.
  - **create, move, delete, restore: refused** ("this kind can't be resolved by this build;
    see the runbook"), changing nothing. Ending them like a drop is wrong:
    - a delete whose item already reached the trash would leave an entry with no record,
      orphaned for good;
    - a restore set back to `trashed` while its trash entry and its destination share an inode
      would let the purge delete the restored files.

    Each later slice of ADR-0033 (creates, moves, deletes and restores) extends resolve for its
    kind, with that kind's rule, runbook steps and tests, in the same PR as its recovery.

  The intents layer refuses these kinds before any vault I/O. The store's `Resolve` refuses
  them again in the transaction that ends the intent, so no other caller can end one as a
  drop.
- **Audit.** Resolve records `intent.resolving` (op ID, kind, whether the temp was removed)
  before the transaction that ends the intent, and `intent.resolved` after it commits. A crash
  between them leaves `intent.resolving` with no `intent.resolved`. Then `intents list` shows
  whether the intent is still there: if it is, the transaction didn't commit, and resolve may
  run again.
- **Crash safety** (CLAUDE.md rule 14). The flagged intent is the durable record; resolve needs
  no intent of its own:

  | Crash point | State left | Next `resolve` |
  |---|---|---|
  | Before or during the copy | Flagged intent, temp there; maybe a partial `.copying` file | Removes the leftover, copies, goes on |
  | After the copy, before the temp's unlink is synced | Flagged intent, copy durable, temp there or gone | Same copy found (or no temp), goes on |
  | After the temp's unlink is synced, before the commit | Flagged intent, copy durable, no temp | Ends the intent |
  | After the commit | No intent; the copies stay | `ErrNoIntent`, changing nothing |

## Consequences
- Positive:
  - An operator can end a flagged save without `db rebuild`, and without losing an edit.
  - A resolve on the wrong vault is refused before any vault I/O.
- Negative / costs:
  - Create, move, delete and restore intents can't be resolved until their slices ship.
    `noRecoveryYet` flags them (ADR-0033), and the release gate blocks a format-2 release
    until it is gone, so no release has such a flagged intent that can't be resolved.
  - An open intent the start can't recover still has only the runbook's steps: fix the cause,
    move the unreadable side out of the vault, or `db rebuild` as the last resort.
- Follow-ups:
  - [#199](https://github.com/joaoheytor/PostelMD/issues/199): the web app;
  - [#204](https://github.com/joaoheytor/PostelMD/issues/204): an operator's flag for an open
    intent the start can't recover (needs its own ADR).

## Alternatives considered
| Option | Why not |
|---|---|
| End every kind as Drop does | Orphans a trash entry (delete) or lets the purge remove restored files (restore) |
| Resolve without the vault check | A wrong mount ends the intent without copying the real temp, and the next correct start sweeps it |
| Resolve online, through an admin socket | No admin socket exists yet; offline under the instance lock is how every other repair command runs |
| A rerun records the lost `intent.resolved` when it finds `recovery/<op-id>/` but no intent | A copies folder can outlive many intents, and the rerun can't tell that it ended this one |
