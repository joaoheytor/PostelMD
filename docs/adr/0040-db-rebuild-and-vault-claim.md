# ADR-0040: `db rebuild`, `vault claim` and the identity check: their mechanics

- **Status:** Accepted (with the IDs slice's fourth part, second PR, the first code that depends on it). Supersedes two parts of [ADR-0030](0030-v02-storage-sqlite.md)'s `db rebuild`: step 2's file list, and step 3's "left in place". Its step 2 file list is extended by [ADR-0041](0041-the-first-upgrades-intent.md) (an `upgrade.intent` it takes over moves too)
- **Date:** 2026-11-17

## Context
[ADR-0030](0030-v02-storage-sqlite.md) decides what `postelmd db rebuild` and
`postelmd vault claim` do, and what the identity check is ("Data format 2", "Checks at start,
and losing the database"). It leaves to the code what CLAUDE.md rule 14 asks to be written
down: the rebuild intent's content, its folder names, what "a half-built database" is, every
crash point with its next run, and what the commands refuse before their first side effect.
[ADR-0031](0031-v02-file-identity.md) ships the commands in the IDs slice's fourth part,
which [ADR-0034](0034-app-db-before-format-2.md) splits from the part that writes data
format 2. This ADR records those mechanics. Two of them change ADR-0030's Decision, and
supersede only those parts:
- **Step 2's file list.** ADR-0030 moves `app.db`, `-wal` and `-shm`. This ADR also moves an
  `app.db-journal` (SQLite would replay it into the new file, as a stale WAL), and, when a
  migration was interrupted and `db rollback` can't undo it, that migration's
  `migrate.intent`, snapshot and `post-migration-started-*` files: without them, both
  `db rollback` and `db rebuild` refused, and nothing could recover the instance.
- **Step 3's "left in place".** ADR-0030 leaves a temp-folder entry that isn't a regular file
  in place. Step 4 opens the vault, whose start-up sweep (ADR-0027) removes every entry of the
  temp folder, so this ADR says so: such entries aren't copied, and the sweep removes them.

It also states, for data format 2, decisions 6 and 7 of the startup scan's PR (#183;
[ADR-0036](0036-the-startup-scan.md), "serve before data format 2"), which said part 4 would
revisit them.

## Decision

### The identity check
- **The sample:** the live rows of `files` (suspect rows are left out), ordered by
  SHA-256 of the stored vault UUID, a zero byte and the row's ID, the first 1,000 taken. The
  same database gives the same sample on every run; another vault UUID gives another one.
- **A match:** the row's path, looked up without following a symlink and never in a
  reserved folder, is a regular file with the row's inode and size.
- **Passes** when at least 90% of the sample matches (`matched × 10 ≥ sampled × 9`). An
  empty sample passes: no ID can be misattributed.
- It reads only: it opens neither the vault (whose `Open` adopts the folder and sweeps its
  temp folder) nor the database for writing.

### `postelmd vault claim [--yes] [--force]`
Offline, under the instance lock, after the data format check. Before any change it refuses:
- with no `app.db`, or one `Open` refuses (a damaged file, `rebuild.intent` or
  `rollback.intent` present): the fix is that error's, or `db rebuild`;
- a database without the "assigned" flag: there are no IDs to claim, the first upgrade
  writes the marker;
- a marker that is a symlink, a folder or another special file (ADR-0031): "move it aside,
  then run this again".

A marker that already names the stored UUID is "nothing to do" (exit 0). Otherwise it prints
the vault's path, its file count (as `/api/v1/files` would list them), the vault UUID the
marker names when it is another one, and how many sampled rows match, then asks:
- the check passes: `--yes`, or typing `yes` (or the vault's path) at the prompt;
- the check fails: `--force`, or typing the vault's path at the prompt. `--yes` alone
  refuses, since the next scan tombstones every row that doesn't match.

Without a terminal and without the flag it needs, it refuses and names the flag. Then it
writes the marker with the stored vault UUID and the build's data format (ADR-0031's
`WriteMarker`: one rename, no intent), and one audit record, `vault.claimed`, to stdout (the
sink `serve` uses, as `reset-password`'s): the vault UUID, the one it replaced, whether the
check passed, and whether `--force`, `--yes` or a typed path confirmed it.

### `postelmd db rebuild`
Offline, under the instance lock, after the data format check. Before step 1 it refuses,
changing nothing:
- `rollback.intent` in the state directory, or a `migrate.intent` that `db rollback` can undo
  or retire (its own eligibility check, read only): that recovery comes first (ADR-0035);
- a `migrate.intent` that `db rollback` refuses for any reason but the four below, with
  rollback's own message: another build's migration ("run db rollback with the build that
  started that migration"), a stray `app.db.tmp`, a snapshot temp or `app.db-journal` ("move
  it aside"), the rollback folder already there, a version outside the intent's, or an I/O or
  permission error reading the intent, a file's type or the database. db rebuild takes the
  migration over only when rollback refuses because `app.db` is absent or not a regular
  file; `app.db` is damaged or isn't a database (SQLite says so; a permission or I/O error
  doesn't count); the new version started (a `post-migration-started` file or row); or the
  snapshot's content doesn't verify (SQLite finds it damaged, its integrity check fails, or
  its version isn't the migration's; a permission or I/O error, or a snapshot replaced while
  it was verified, doesn't count). Each of those four is marked where rollback's check finds it,
  never inferred from its text;
- the vault can't be read; the vault marker is a symlink, a folder or a special file;
- what step 4's `vault.Open` would refuse on: adoption's check (ADR-0027: a reserved folder
  without the marker holding entries PostelMD didn't create), and, where the temp folder
  exists, the `RENAME_NOREPLACE` probe (a temp created and removed, as every start does).
  Without a temp folder (a vault never served) the probe can't run without creating one, so a
  filesystem without `RENAME_NOREPLACE` still refuses at step 4, after the intent; the run
  stops there with its intent, and once the vault is on another filesystem, a rerun finishes.

- **`rebuild.intent`** (a sidecar in the state directory, written as `migrate.intent` is:
  temp, `fsync`, `RENAME_NOREPLACE`, folder `fsync`):
  `{"uuid", "time", "had_db", "moves", "migration"}`: the new rebuild UUID, the UTC time to
  the second, whether `app.db` existed, and the files step 2 moves (the original move):
  `app.db`, `-wal`, `-shm` and `-journal` as they were there, and with `migration`, an
  interrupted migration's files (`migrate.intent`, `pre-migrate-*`,
  `post-migration-started-*`) when `db rollback` can't undo it for one of the four reasons
  above.
- **Folders,** in the state directory, named from the intent's time
  (`20060102T150405Z`): `damaged-<time>/` for the moved files, `recovery/rebuild-<time>/` for
  the temps. A copy is made in `recovery/.rebuild-<time>.copying` and renamed into its folder,
  never over a file.
- **The original move is recorded.** Once every file of `moves` is in `damaged-<time>/`
  (synced), step 2 writes `damaged-<time>/moved` and syncs it. That file, never what happens to
  be in the folder (rule 14), says the original move is done.
- **A half-built database** is an `app.db` beside the intent, after the original move (or with
  nothing to move), that doesn't carry the intent's rebuild UUID in `meta`. A file that isn't a
  database, or has no `meta` table, is half-built; one that can't be read for another reason
  (permissions, I/O) stops the run with that error. An `app.db` made by an earlier rebuild
  carries that rebuild's UUID, not this one's, so it is an original. A half-built database and
  the files beside it go to `damaged-<time>/attempt-<n>/`, `n` one more than the last attempt
  folder holding an `app.db` (an empty one a crash left is used again); files a killed move
  left behind follow their `app.db` into its attempt folder.
- **Step 3's temp copies:** every regular file of the temp folder (apart from the marker), of
  any size, is streamed into the recovery folder. A copy already there with the same bytes
  counts as done (its source is then removed); with other bytes it is interference. Folders,
  symlinks and special files aren't copied: they are logged with their name and kind as
  attributes, and step 4's `vault.Open` sweep removes them (ADR-0027); when a resumed run
  skips step 4, the next start's sweep does.
- **Step 4** creates a new vault UUID: the rebuild adopts the mounted vault with new IDs.
- While `rebuild.intent` is there (a file or anything else at its name), `Open` refuses
  (`serve`, `vault claim`), naming `db rebuild` again.
- **Errors after the intent** say the run stopped and the intent stays; they never say that
  nothing was changed, since earlier steps of the same run may have.
- **The audit record** `db.rebuilt` goes to stdout once the rebuild finishes: the rebuild UUID,
  the new vault UUID, whether it resumed, whether `app.db` existed, the files given IDs and the
  temps kept.

| Killed at | Left | Next run of `db rebuild` |
|---|---|---|
| `rebuild-intent-synced` | the intent's temp | removes it, checks again, starts |
| `rebuild-intent-written` | the intent; nothing moved | moves the files of `moves`, records the move, on |
| `rebuild-moved` (after each file) | some files of `moves` moved, not recorded | moves the rest to `damaged-<time>/` (never over a moved one), records the move, on |
| `rebuild-moved-recorded`, `rebuild-moved-synced` | all moved, the move recorded | nothing more to move, on |
| `rebuild-temp-copied` | a temp's copy in `recovery/.rebuild-<time>.copying`, not renamed | removes the copy, copies the temp again, on |
| `rebuild-temp-kept`, `rebuild-temp-removed` | a temp copied, its source there or not | the same bytes count as copied; removes the source, on |
| `rebuild-db-created`, or inside step 4's transaction (`assign-before-commit`) | an `app.db` (with its `-wal`, `-shm`) without the UUID | moves them to `attempt-<n>/`, creates it again, on |
| `rebuild-moved` while a half-built database moves | its `app.db` in `attempt-<n>/`, its other files still beside the intent | moves them into the same `attempt-<n>/`, on |
| `rebuild-assigned` (step 4 committed) | the database with the UUID | step 5 |
| `rebuild-marker-written`, `rebuild-format-written` | the marker (and format) written | writes them again, removes the intent |
| `rebuild-done` | the intent's unlink not yet durable | either nothing left to do, or step 5 again |
| any, with a file of `moves` in neither the state directory nor `damaged-<time>/`; `had_db` and `app.db` in neither place; the UUID in `app.db` but the move not recorded; a database file beside the intent that `moves` doesn't list before the move is recorded, or a `-wal`/`-shm` after it with no attempt folder holding an `app.db` | | refuses: interference; the runbook decides |

The run that finishes an interrupted rebuild keeps its intent's UUID, time and folders, and
reports what it did itself (temps kept, files given IDs). Step 5's marker and format writes
are each one rename, so running them again is safe. The `fsync` steps are covered by review,
not by a power-loss test (#182 tracks the model).

### Before data format 2
Both commands write data format 2 or a marker for it, so they ship with the code but refuse
until the build's data format is 2 (the IDs slice's last PR, ADR-0034): at format 1 they say
so and change nothing, and a damaged database keeps ADR-0034's manual fix.

### The startup scan at data format 2 (ADR-0036's decisions 6 and 7)
- **6.** At format 2, `serve` no longer skips the scan with a warning when the "assigned"
  flag and the marker disagree: ADR-0030's format table runs first and refuses those starts,
  naming `vault claim` or `db rebuild`. The skip stays for format 1 only.
- **7.** At format 2, a failed first pass still doesn't stop the start: no ID is on the wire
  until ADR-0032's ETag switch, and its writer faults already put the vault read-only
  (ADR-0037). That slice decides again whether a failed first pass must refuse to serve.

## Consequences
- Positive: every step of both commands has a crash point and a test; the identity check is
  one function the format table (next PR) and `vault claim` share; an interrupted migration
  with its database lost has a way out.
- Negative / costs: until format 2 ships, the commands are tested but refuse in a release;
  `db rebuild` reads every file of the vault (the first assignment's walk) while the server
  is stopped, and needs free space for the temps it copies.
- Follow-ups:
  - the format-2 PR removes the refusal, runs the format table with this identity check, and
    switches the damaged-database message back to `db rebuild` (ADR-0034);
  - ADR-0017's backup boundary must include `damaged-*/` and `rollback-*/`;
  - the 7-day expiry of `recovery/` must say whether it covers `recovery/rebuild-*/`.

## Alternatives considered
| Option | Why not |
|---|---|
| Let both commands run at format 1, writing format 1 | A rebuilt database at format 1 has IDs the format table never checked; ADR-0034 keeps format 1's fix manual |
| Sample by `ORDER BY random()` | Two runs could disagree; ADR-0030 asks for a deterministic sample |
| Overwrite a half-built database | It may be the only copy of what a crashed attempt saw; nothing is deleted |
| Tell the original from a half-built database by what is in `damaged-<time>/` | A lost `app.db` with a stale WAL left only `-wal` and `-shm` there, so a half-built database was taken for the original and its `-wal` hit the moved one forever; the `moved` record is the only signal |
| Leave temps the rebuild doesn't copy (symlinks, folders) in place | Step 4's `vault.Open` sweeps the temp folder anyway (ADR-0027); saying "left in place" would be wrong |
| Skip temps over `fs.MaxFileSize` | ADR-0030 copies every regular file; streaming has no size limit |
| Refuse `db rebuild` beside any `migrate.intent` | With `app.db` lost, `db rollback` refuses too, and nothing could recover the instance |
