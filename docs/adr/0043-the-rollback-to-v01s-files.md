# ADR-0043: The rollback to v0.1: the files it moves and how it writes the format

- **Status:** Accepted
- **Date:** 2026-11-17

## Context
[ADR-0030](0030-v02-storage-sqlite.md)'s "Rollback to v0.1" is a runbook, not a command. Its
step 1 moves `app.db`, `-wal` and `-shm` into `rolled-back-<time>/`, and its step 4 writes
`1` into `format`, truncating and writing the existing file so that its owner and mode don't
change. Writing the runbook (`docs/operations/upgrading-to-format-2.md`, #194) showed two
gaps:

- After the first migration that runs on an existing database,
  [ADR-0035](0035-db-rollback.md) leaves `pre-migrate-<F>-<T>.db` and
  `post-migration-started-<F>-<T>` beside `app.db`, and SQLite may leave `app.db-journal`.
  [ADR-0040](0040-db-rebuild-and-vault-claim.md) moves the journal, and an interrupted
  migration's files, in `db rebuild` for the same reason. Left in place after a rollback, they describe a database that is no longer there:
  0.1's `db rollback` and the next v0.2 start would read them against the new `app.db` that
  0.1 creates.
- A crash during a write that truncates first can leave `format` empty, and an empty
  `format` is refused as damaged at every start (ADR-0025).

## Decision
This supersedes two parts of ADR-0030's "Rollback to v0.1": the file list of step 1, and how
step 4 writes the format. Nothing else changes. That covers steps 2 and 3, the helper
container run as the data's owner, the marker truncated in place, and what is lost.

1. **Step 1 moves** `app.db`, `app.db-wal`, `app.db-shm`, `app.db-journal`, every
   `pre-migrate-*` and every `post-migration-started-*` in `.postelmd` that exists, into
   `rolled-back-<time>/`: all of them, finished migration or not, since none of them
   describes the database 0.1 will create. None
   of them holds a credential: `rolled-back-*/` never holds `auth.json` or `secret.key`.
2. **Step 4 writes `1` into `format.new`**, a copy of `format` made with its owner and mode
   (`cp -p`, as the data's owner). It then syncs, renames `format.new` over `format`, and
   syncs again. The format is never empty or half-written. Owner and mode stay as they were,
   which is what ADR-0030's in-place write was for.
3. **Before its first move**, the rollback refuses, changing nothing, in four cases:
   - the format isn't `2`;
   - an `upgrade`, `rebuild`, `migrate` or `rollback` intent exists, a dangling symlink
     included;
   - the marker isn't a regular file;
   - the user can't write the marker or `.postelmd`.

   Each check is one that would otherwise stop it part way. After a stop, every step is
   safe to repeat, and the format stays `2` until the last one.
4. **The rollback never restores `pre-format-2/`.** It moves that folder aside, as step 2
   says. The credentials in use stay in `.postelmd`.

## Consequences
- Positive: after a rollback, 0.1 and the next v0.2 start see no migration files from a
  database that was moved away; a crash can't leave an empty `format`; a rollback can't stop
  half done for a reason it could have checked first.
- Negative / costs: `rolled-back-<time>/` holds more files. The runbook needs a paragraph,
  and a block, for an empty `format` that came from elsewhere.
- Follow-ups: part D's image upgrade smoke test runs this rollback, after the real first
  upgrade.

## Alternatives considered
| Option | Why not |
|---|---|
| Leave the migration files in place | 0.1's `db rollback` and the next v0.2 start would judge the new `app.db` by another database's snapshot and markers |
| Delete them | The runbook never deletes: they are part of the only copy of the old database |
| Keep the in-place write of `format` | A crash between the truncation and the write leaves an empty `format`, refused at every start |
| Offer a restore of `pre-format-2/`'s credentials | It brings back an old password and revives sessions revoked since. `reset-password` already covers a lost password |
