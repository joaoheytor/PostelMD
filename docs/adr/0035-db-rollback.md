# ADR-0035: `postelmd db rollback` and retiring `migrate.intent`

- **Status:** Accepted
- **Date:** 2026-11-13

## Context
Supersedes the **database-only rollback command** of
[ADR-0015](0015-supply-chain-and-ci.md) ("Upgrades and rollback"): its five steps and its
re-run rule.

> **Database-only rollback** is an offline command (`postelmd db rollback`), run under the
> instance lock, never by hand-copying files:
> 1. refuse unless the snapshot passes `PRAGMA integrity_check` and has schema version
>    `from`, and **neither** `post_migration_started` marker exists;
> 2. write a rollback intent (sidecar, `fsync`ed);
> 3. **move** `app.db`, `app.db-wal` and `app.db-shm` into `rollback-<timestamp>/` (never
>    delete them). A stale WAL left next to the restored file would be replayed into it and
>    corrupt it;
> 4. copy the snapshot to `app.db.tmp`, `fsync`, rename it to `app.db`, `fsync` the directory;
> 5. remove the intent.
>
> Re-running the command after a crash continues from the intent: if `app.db` is absent and
> `rollback-<timestamp>/` holds the moved files, it continues at step 4; if `app.db` already
> matches the snapshot's hash, it removes the intent.

Done exactly as written, the previous build can't start after it (#174). #173's runner,
which every build that opens `app.db` has, reads `migrate.intent` before anything else and
refuses one whose `to` isn't its own last migration (`ErrMigrateInterference`). Those steps
never remove `migrate.intent`, and they leave the snapshot in place, which the next upgrade
refuses as "a pre-migration snapshot already there without an intent". The re-run rule
also judges a step from one signal (`app.db` matching the snapshot's hash), and it doesn't
cover a migration stopped before its snapshot existed, which is the most likely failure (a
full disk while copying, `TestPreMigrateDiskFull`).

ADR-0030 requires the command no later than the first release whose migrations run against
existing databases. Migration 0002 is that migration (ADR-0031, part 1), and
`scripts/verify-release-tag.sh` refuses a tag that has it without the command.

The eligibility rule, the two forms of rollback, and when to fix forward, all in ADR-0015,
are unchanged.

## Decision

### What #173 chose, recorded
These were silent in ADR-0015 and ADR-0030; this records them as the contract (no change):
- `migrate.intent` is one JSON object, `{"from":F,"to":T,"snapshot":"pre-migrate-F-T.db"}`,
  with no other field, written by temp, `fsync`, `RENAME_NOREPLACE` and folder `fsync`.
- The markers of one migration are the `meta` row `post_migration_started:F-T` and the empty
  sidecar `post-migration-started-F-T`, written in that order, each only if missing, each
  durable, before the intent is removed.
- An intent whose `to` isn't the build's last migration is refused (written by another
  build). A snapshot, its temp or a sidecar of either found without an intent is refused.
  The snapshot is verified again (`integrity_check`, version `from`) in the "between" row.

### The command
`postelmd db rollback` is offline: it takes the instance lock (refusing next to a live
server) and checks the data format first, like every command that reads the state.

**Eligible only when all of these hold, read without changing anything** (several signals,
never one):
1. `migrate.intent` is there and names this build's last migration. No intent means no
   migration was interrupted: refused, "nothing to roll back" (with a
   `post-migration-started-*` file there, the message also says the new version started, so
   fix forward or follow the runbook).
2. Neither marker exists: no `post-migration-started-F-T` file, and no
   `post_migration_started:F-T` row in `app.db`'s committed state.
3. `app.db` is a regular file at a version from `F` to `T`; `-wal` and `-shm` beside it, if
   there, are regular files; there is no `app.db-journal`. The read is read-only: with no
   WAL, `immutable=1`, which creates no file; with a WAL, through it, so its committed
   transactions count (a marker row a killed migration committed only to the WAL is found).
   That read may rewrite or create SQLite's `-shm` index, and nothing else: the index holds
   no data and is rebuilt from the WAL.
4. No `app.db.tmp`. Then either:
   - the snapshot is there, with no temp beside it, verifies (`integrity_check`, version `F`,
     no `-journal`, `-wal` or `-shm` beside it) and the folder `rollback-<UTC time>/` doesn't
     exist yet: a **restore**; or
   - the snapshot isn't there and `app.db` is at `F` (migrations start only after the
     snapshot is in place): a **retire**.

Otherwise it refuses (`ErrRollbackRefused`) and changes nothing but, with a WAL, the `-shm`
index. A snapshot that doesn't verify, or an `app.db` that can't be read, leaves nothing to
restore: the refusal names fixing forward or the runbook, never the damaged-database fix of
ADR-0034 (moving `app.db` aside would leave `migrate.intent`, so the previous build would
still refuse).

**Restore**, each step durable before the next:
1. write `rollback.intent`, `{"from","to","snapshot","dir"}`, by temp, `fsync`,
   `RENAME_NOREPLACE` and folder `fsync`;
2. create `rollback-<UTC time>/` (0700);
3. **move** `app.db`, then `app.db-wal`, `app.db-shm` and `app.db-journal` if there, into it
   (`RENAME_NOREPLACE`, never deleted), then `fsync` both folders;
4. copy the snapshot byte for byte to `app.db.tmp` (0600, `O_EXCL`, `fsync`), verify it
   (`integrity_check`, version `F`), check that no `-wal`, `-shm` or `-journal` is beside
   `app.db`, rename it to `app.db` (`RENAME_NOREPLACE`) and `fsync` the folder;
5. move the snapshot into the rollback folder, then `fsync` both folders: the next upgrade
   takes a new one;
6. remove `migrate.intent`, after checking that it still names `F → T` and that `app.db`
   still has the moved snapshot's bytes with nothing beside it;
7. remove `rollback.intent`.

**Retire** (no snapshot): remove the snapshot's temp and its `-journal` if there, `fsync`
the folder, remove `migrate.intent`. `migrate.intent` is the durable record: each step is
idempotent and the next run reads the same state, so no second intent is needed.

**While `rollback.intent` is there, `Open` refuses** (`ErrRollbackInterrupted`) before it
creates or opens anything: the new build would otherwise resume the migration on a
half-restored state. A build without this command can't know the file. One whose last
migration is older than `migrate.intent`'s refuses on it until step 6, and after step 6 the
database it finds is the restored one. One with the same last migration (every build since
#177, before this command) resumes the migration it names and can finish it; the re-run then
finds `migrate.intent` gone while the snapshot hasn't moved, or the snapshot in both places,
and refuses (below): the migration ran again, so the fix is forward.

**A re-run with `rollback.intent` there** reads the files, never one signal alone:

| Killed at | Left | Next run |
|---|---|---|
| the intent's temp synced | the temp only | checks eligibility again, starts over (the temp is removed) |
| intent written | the intent | creates the folder, goes on |
| folder created | the intent, an empty folder | moves, goes on |
| after each move | `app.db` in the folder; the WAL files in either place | moves what is left, goes on |
| moves synced | all moved | copies, goes on |
| copy written, or verified | `app.db.tmp` | removes it, copies again, goes on |
| restored, or its folder synced | `app.db` the snapshot's copy | `app.db` with the snapshot's bytes and nothing beside it: moves the snapshot, goes on |
| snapshot moved | the snapshot in the folder | checks `app.db` against it, removes `migrate.intent`, then its own |
| `migrate.intent` removed | only `rollback.intent` | removes it, without judging `app.db` (the previous build may have opened it since) |
| `rollback.intent` removed | done | "nothing to roll back" |

| Retire killed at | Left | Next run |
|---|---|---|
| temp removed | `migrate.intent` | removes it |
| `migrate.intent` removed | done | "nothing to roll back" |

While `migrate.intent` is there no build opens `app.db` as its own, so every run checks
`app.db` against the snapshot, wherever the snapshot is (state directory or folder), before
retiring it; with `app.db` gone and the original in the folder, it copies the snapshot
again from there.

Anything else on a re-run (the intent not parsing or naming another build, the snapshot in
both places or neither, `app.db` in neither place, a file at `app.db` that isn't the
snapshot's copy, a WAL beside it before the rename or before `migrate.intent` goes, a name
taken in the folder, `migrate.intent` naming another migration) is `ErrRollbackInterference`:
it stops, keeps `rollback.intent`, and changes nothing more.

**An older build started during steps 3 to 6** (after a crash; it doesn't know
`rollback.intent`) creates a new, empty `app.db` where the original was moved, or opens the
restored copy and changes its header, and then refuses on `migrate.intent`. The re-run
refuses that file as interference (it isn't the snapshot's copy, or a WAL, shared-memory or
journal file is beside it). The fix, in the runbook: move `app.db`, `app.db-wal`,
`app.db-shm` and `app.db-journal` (those there) into a new `.postelmd/stray-<date>/` folder,
then run `db rollback` again; it finds `app.db` gone with the original in the folder, and
copies the snapshot again. If the original's WAL was still beside `app.db` (a crash between
two moves), SQLite deleted it when it created the new database beside it; it held only the
failed migration's work, which the rollback discards anyway. The stray folder is still moved
aside, never deleted.

## Consequences
- Positive: after a failed or interrupted migration, one command brings back the database
  the previous build wrote, and that build starts. A re-upgrade then runs the migration again
  with a new snapshot. The command deletes nothing: the database it replaces, its WAL files
  and the snapshot go into the rollback folder.
- Negative / costs: after a migration finished (its markers exist) the command refuses, by
  design: at format 1 the runbook's move-aside still applies (the database holds only the
  boot ID). From format 2 on, a finished migration means fixing forward or ADR-0017's
  coordinated full-instance restore, as ADR-0015 says. If an older build starts during steps
  3 to 6 (after a crash), it leaves a file at `app.db` that the re-run refuses as
  interference; the runbook says what to move (above). The copy reads the whole snapshot
  once more than a rename would, to keep the snapshot for the folder.
- Follow-ups: the power-loss model of these crash points (with the pre-migration one,
  roadmap v0.2); the image-level upgrade smoke test that runs `db rollback` in a container
  (ADR-0031, part 4).

## Alternatives considered
| Option | Why not |
|---|---|
| ADR-0015's steps as written | The previous build refuses `migrate.intent` and the next upgrade refuses the snapshot left behind (#174); the re-run trusts one hash match |
| Record each phase in `rollback.intent` (rewritten per step) | Six more durable writes, and a re-run must still check the files, since the phase alone is one signal |
| Rename the snapshot to `app.db` instead of copying it | Leaves nothing to compare a restored file with after a crash, and the snapshot is the record of what was restored |
| Let `serve` roll back on its own when a migration fails | A failed migration is an operator's decision (fix forward or go back); an automatic restore could hide a bug and would run while an image the operator didn't choose is starting |

## Clarifications
- **2026-11-17, `db rebuild` and `migrate.intent`.** From data format 2, `db rebuild` can move
  an interrupted migration's `migrate.intent`, snapshot and `post-migration-started` files
  aside, into its `damaged-<time>/` folder, when this command refuses it because `app.db` is
  absent or damaged, the new version started, or the snapshot doesn't verify. Any other
  refusal of this command stops `db rebuild` too. See
  [ADR-0040](0040-db-rebuild-and-vault-claim.md). This is a cross-reference only.
