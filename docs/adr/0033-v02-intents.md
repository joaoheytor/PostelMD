# ADR-0033: v0.2 intents: saves, creates and structural operations

- **Status:** Accepted (with the intents slice's first part, the intents table and its store
  API, the first code that depends on it)
- **Date:** 2026-11-12; the slice's open points settled 2026-11-18, before acceptance

## Context
From v0.2 every PostelMD write changes a file **and** the database
([ADR-0031](0031-v02-file-identity.md)): two side effects, so each needs a durable intent
before its first filesystem step and a recovery table per crash point (CLAUDE.md rule 14).
[ADR-0003](0003-save-protocol.md) §2 and §5 designed them; this ADR fixes the subset v0.2
builds, and holds every recovery row it relies on (ADR-0003 is still Proposed).

Supersedes these parts of Accepted ADRs, and nothing else in them:
- [ADR-0020](0020-mvp-scope.md), "Saving": "Its server side is replaced, for the MVP, by
  single-rename operations that need no intents".
- [ADR-0022](0022-mvp-trash-layout.md): "The first side effect holds no data, so no intent is
  needed".
- [ADR-0024](0024-restore-from-the-trash.md): the "**No intent**" bullet, and, in its
  "**Listing**" bullet, what is listed and from where (the trash folder's entries in both
  layouts): the list now reads the trash records (below). Its order, cap and fields stand.
- [ADR-0027](0027-landed-retries-and-the-adoption-marker.md)'s sweep bullet, "with the marker:
  removes **every other** entry in `.postelmd-tmp/` … and keeps the marker": the sweep now runs
  after intent recovery and spares what an open or flagged intent names (below); and, with
  it, the sweep at the vault's opening.

## Decision

### Common rules
- An intent is a row in `intents` (op ID, kind, state `open` or `flagged`, payload, time;
  migration 0003), committed with `synchronous=FULL`
  ([ADR-0030](0030-v02-storage-sqlite.md)) **before** the first filesystem step, and deleted in
  the transaction that records the result. It holds no note content. An op ID is 32 random
  hex digits (the MVP's temp names), except a delete's, which is its trash stamp (ADR-0022).
  The payload is JSON the store writes and reads strictly (unknown fields refused); paths in
  it are their bytes as on disk (base64), since a name needn't be UTF-8. A save's or create's
  temp is `.postelmd-tmp/<op-id>`, so the payload doesn't repeat it.
- Every transaction that begins, ends or flags an intent is one commit. A transaction that
  begins a delete also writes its `moving` trash record; one that begins a restore moves the
  record `trashed` → `restoring` by compare-and-set. Ending an intent as **not applied** also
  undoes what began with it (the `moving` record is removed; a `restoring` record goes back
  to `trashed`).
- **No outbox rows yet.** The completion transactions below record no outbox row: the outbox
  slice (after the ETag switch, [roadmap](../roadmap.md)) adds them to these same
  transactions. Until then nothing reads a change from the database but the next request.
- The **one vault mutex** of ADR-0020 stays (no per-file locks), held from the precondition
  check to that transaction. No filesystem I/O happens inside a transaction (ADR-0030).
- Recovery runs at start, before the sweep, the scan and HTTP (ADR-0030's order): opening the
  vault no longer sweeps `.postelmd-tmp/` (ADR-0027's sweep at open); the start opens the
  vault, runs the first upgrade if the data needs it, then recovery, then the sweep, then the
  scan. It judges each intent on **the intent, the target's hash, and the temp's hash and
  inode**, never on one of them.
- **Temp names.** Recovery accepts an intent's temp only as a direct child of
  `.postelmd-tmp/` whose name is the 32-hex op ID; anything else in an intent is interference.
  Temps are opened through `os.Root` with `O_NOFOLLOW`.
- **The sweep** (replaces ADR-0027's bullet): after recovery, every entry of `.postelmd-tmp/`
  that no open or flagged intent names is removed, except the marker.
- **Mechanisms.** The vault refuses filesystems without `RENAME_NOREPLACE` (ADR-0020), so v0.2
  implements only ADR-0003's `rename-noreplace` tables; `link-unlink` and `rename-checked`
  come with ADR-0006's fallbacks.
- **Recovery tables.** Every row recovery relies on is in this ADR (Recovery tables, below),
  each with one action: **drop**, **drop, keeping the temp**, **roll forward** or **flag**. ADR-0003's tables were the
  design; where they differ, these govern.
- **Interference** (a state an operation can't produce by itself) keeps both sides in the
  state directory's `recovery/<op-id>/` (`/data/.postelmd/recovery/`, beside `db rebuild`'s
  `recovery/rebuild-*/`), marks the intent `flagged`, and logs an error (without paths,
  CLAUDE.md rule 15) and an audit event. A flagged intent stays until the operator decides:
  the sweep spares its temp, the scan never makes a path it names suspect or tombstones it,
  and its trash record (a delete's `moving`, a restore's `restoring`) can be neither restored
  nor purged. ADR-0003 §2's other parts, the recorded paths kept read-only and the web app
  asking the user, are [#199](https://github.com/joaoheytor/PostelMD/issues/199). Copies in
  `recovery/` are kept 7 days (ADR-0003), counted from their folder's time; the purge job
  removes older ones, `db rebuild`'s included.
- **Giving a path a row.** An operation that puts a file at a path its no-clobber rename
  proved absent (a create, a move's or restore's destination, and every path under it)
  first resolves what the database still has there, in its completion transaction, since
  `files.path` is unique ([ADR-0031](0031-v02-file-identity.md)). A row there (a suspect, or
  a live row whose file went since the last scan, such as a `mv` in a shell) is **vacated**,
  never tombstoned: it keeps its ID, generation, hash and stat, becomes suspect (a suspect
  keeps its clock), and moves off the path to a placeholder no file can have (a NUL byte and
  its ID; `files.displaced_from` keeps the path it had). The scan then decides for it exactly
  as ADR-0031 says for any missing row: rule 1 or rule 2 moves its ID to the file's new path,
  or the second pass tombstones it, under the path it had. A hold on those paths, whose file
  went too, is released. This is ADR-0031's two-pass absence applied to a row the database
  can't leave on its path; it supersedes nothing in ADR-0031. Refusing instead would leave
  recovery unable to finish the intent.
- **The scan and intents.** The scan never makes a path an open or flagged intent names
  suspect, nor tombstones or moves its row (ADR-0003 §2, "no intent names it"): those paths
  are the intent's until recovery or the operator ends it.

### Save
Intent: op ID, file ID, base generation and full hash, new full hash, path, the target's
`stat` at the check, temp name. Steps:
1. under the mutex, read and hash the target (ADR-0032's precondition rules). Until the
   ETag slice, the wire keeps the MVP's content-hash ETag and its answers (a missing path is
   `404`); on the database side, bytes that differ from the row's hash are first recorded as
   an outside change (generation + 1, its own transaction), so the intent's base is the file
   as read. A path with no row gets one first: the save handler calls the scan's read hook
   (ADR-0031's `Seen`) before the mutex, as reads do, so an outside file the scan hasn't seen
   gets its ID, and a held path answers `503` with `Retry-After`. A path still without a row
   under the mutex answers `503` with `Retry-After`; the save never invents an ID;
2. commit the intent;
3. temp `.postelmd-tmp/<op-id>` (`O_EXCL`, `0600`, the target's mode by `fchmod`), write,
   `fsync`;
4. re-`stat` the target; if it changed, remove the temp and drop the intent, then read the
   target again and record the outside change (generation + 1, its own transaction, as in
   ADR-0032), so the `412` carries the current note and an ETag that describes it (or
   `file-missing` if the target is gone);
5. `renameat` onto the target;
6. `fsync` the target's folder, then `.postelmd-tmp/`;
7. one transaction: generation + 1, hash, stat, intent deleted (the outbox row comes with the
   outbox slice, above);
8. `204` with the new ETag.

**Restore from history** is a save of the version's bytes, by these steps.

A crash before (2) commits leaves no intent and at most a temp, which the sweep removes; the
client never got `204`, and its retry goes through. A crash after (7) is done: a retry with
the old ETag sends equal bytes and gets `204`. Every state in between is in the save's
recovery table (below).

The synchronous secret scan of ADR-0003 §5 step 5, and findings in the save response, are
**deferred** to the secrets slice (ADR-0010 gate 3); until then step 7 records no findings.

### Create
Intent: op ID, destination path, new full hash, temp name. Steps: temp + `fsync`;
`renameat2(RENAME_NOREPLACE)` onto the destination; `fsync` the destination's folder, then
`.postelmd-tmp/`; one transaction: a new ID at generation 1, intent deleted (the outbox row
comes with the outbox slice);
`201`. The transaction resolves a row or hold on the destination first (Common rules,
"Giving a path a row"). Recovery: the create's table (below).

### Structural operations
Each intent holds op ID, kind, source and destination, the source's `(device, inode)`, every
affected `(file ID, generation, relative path)`, and the mechanism.
- **Move or rename** (file or folder): the move's table (below). Applied:
  every affected row gets its new path and a generation bump, IDs kept, and the moved files'
  new stats; holds on paths under the source are released (the next scan sees those paths
  again); rows and holds on the new paths are resolved (Common rules).
- **Delete**: ADR-0022's steps, its stamp as the op ID, and a trash record (`moving` →
  `trashed`) committed before each step; the delete's table (below). The stamp (with its `-1`,
  `-2` suffix) is chosen under the vault mutex from the trash folder and the records, then
  the intent and the record are committed, then the `mkdir` runs: an `EEXIST` there (a folder
  made outside PostelMD meanwhile) ends the intent as not applied and fails the delete with a
  retryable error; nothing retries it by itself. The affected `(file ID, generation, path)`
  list is in the intent's payload, and the tombstones keep it once applied. Applied: every
  affected ID is tombstoned (`trashed`) and holds under the item are released.
- **The trash list** reads the `trashed` records (their name, kind and removal date, the clock
  plus 7 days) and the disk only for a file's size. An entry on disk without a record (one the
  upgrade's import skipped, ADR-0031) is neither listed nor restorable; it stays for the
  operator.
- **Restore from the trash**: ADR-0024's steps, with the record moved `trashed` → `restoring`
  by compare-and-set first; the restore's table (below). Applied: new IDs, the record dropped,
  rows and holds on the new paths resolved (Common rules). A record in `purging` or
  `restoring` (or `moving`) refuses with `409`.
- **Purge** (emptying the trash by hand, and automatically 7 days after the delete, as
  ADR-0003 designed; for records imported at the upgrade, 7 days after the import,
  ADR-0031): a background job, hourly and after readiness, without the vault mutex (the trash
  is reserved and the record's state excludes a restore):
  1. compare-and-set the record `trashed` → `purging`, committed **before the first removal**;
  2. remove the entry bottom-up (idempotent, resumes after a crash);
  3. delete the record. Tombstones stay.

  Recovery: the purge's table (below).
  The job also removes **empty stamp folders that no record names**
  (left by a restore that crashed after dropping its record, or by a delete that crashed
  after its `mkdir`, ADR-0022), with a remove that only takes an empty folder. That cleanup
  alone takes the vault mutex, briefly: a delete commits its record before its `mkdir`, so
  under the mutex a fresh read of the records always names a folder a delete is filling. The
  job also removes `recovery/` copies older than 7 days (Common rules).

  Emptying the trash by hand (an API and the web app's action) is not in this slice:
  [#200](https://github.com/joaoheytor/PostelMD/issues/200). The automatic purge ships with
  it.

### Recovery tables
Recovery reads each open intent and the disk, finds the intent's row below, and takes its
one action. A flagged intent is left as it is. An intent whose payload doesn't decode, a temp
that isn't a regular file named by its op ID directly under `.postelmd-tmp/`, and **any state
no row names**, are interference. The actions:
- **Drop** (not applied): remove the intent's temp, if any, and, for a delete, its stamp
  folder if empty; end the intent as not applied (a `moving` record removed, a `restoring`
  one back to `trashed`), in one transaction. Nothing is copied (but see the next action). Drop is for states the
  operation or an ordinary race leaves (a `412` or `409` race, an outside change the scan
  records): the operation never answered success, so the client still has its draft, and
  the operation's bytes or item are nowhere but where the user left them.
- **Drop, keeping the temp**: as drop, except that a temp whose hash equals the intent's new
  hash is first copied into `recovery/<op-id>/`, durably, and a warning is logged without
  paths. No audit event and no flag: the intent ends, and the copy is only a second place
  for the client's edit, in case the client lost its draft too. The 7-day `recovery/` purge
  removes it. A partial temp (another hash) is removed, as for drop. It applies to the two
  rows where the target or destination changed outside while a complete temp holds the
  edit; where the base is intact, or the destination absent, the edit was never at risk
  and plain drop applies. Recovery that writes intents implements it with each operation:
  the save's row in the saves PR, the create's in the creates PR, each with its test.
- **Roll forward** (applied): finish the filesystem side the row names (unlink a power-loss
  duplicate's other name, then `fsync` its folder), then the completion transaction of the
  operation's section. A completion whose rows aren't as the intent recorded
  (`ErrIntentStale`, which only a writer ignoring the instance lock can cause) is flagged.
- **Flag** (interference): copy each side that exists (the temp, the target, the item) into
  `recovery/<op-id>/`, mark the intent `flagged`, log the error without paths and write an
  audit event (Common rules, "Interference"). Nothing else changes.

"Same inode" is the device and inode the intent recorded; "new" is the intent's new hash,
"base" its base hash. "Reached by" names the crash points (kill, or power loss) that leave
the state; a state reached only by an outside writer says so.

**Save** (target T, temp P):

| T | P | Reached by | Action |
|---|---|---|---|
| Base | Absent, partial or new; another inode than T | A crash in (3), (4) or (5) before the rename; power loss before (6) | Drop |
| New | Absent | A crash after (5); power loss after (6) | Roll forward |
| New | Same inode as T | Power loss after the target's folder sync, before `.postelmd-tmp/`'s | Roll forward (unlink P first) |
| New | Present, another inode | An outside writer put the same bytes on T before the rename | Drop (the scan records the outside change) |
| Other content, or absent, or not a regular file | Absent or partial | A crash in step 4 after the temp's removal; an outside change or delete of T at any point before (7) | Drop: the `412`-type race. The scan records the change; the client's retry gets `412` (or `file-missing`) with its draft |
| Other content, or absent, or not a regular file | New, another inode than T | A crash in step 4 before the temp's removal; an outside change or delete of T after (3) | Drop, keeping the temp. The rest as the row above |
| Base or other content | Same inode as T | Only an outside writer (a hard link, then a change) | Flag |

**Create** (destination D, temp P):

| D | P | Reached by | Action |
|---|---|---|---|
| Absent | Any | A crash before or during the rename | Drop |
| New | Absent | A crash after the rename; power loss after both syncs | Roll forward |
| New | Same inode as D | Power loss after D's folder sync, before `.postelmd-tmp/`'s | Roll forward (unlink P first) |
| New | Present (partial or complete), another inode | An outside writer created D with the same bytes first (the no-clobber rename failed or never ran) | Drop. The scan gives D its own ID |
| Other content, or not a regular file | Absent or partial | An outside writer created D first, or replaced the created file before the completion | Drop: the `412` race. The scan gives D its own ID; the client never got `201` and retries with its draft |
| Other content, or not a regular file | New, another inode than D | An outside writer created D first (the no-clobber rename failed or never ran) | Drop, keeping the temp. The rest as the row above |
| Other content | Same inode as D | Only an outside writer changing the created file in place before the completion | Flag |

**Move** (`rename-noreplace`, file or folder; source S, destination D):

| S | D | Reached by | Action |
|---|---|---|---|
| Same inode | Absent | A crash before the rename; power loss before D's folder sync | Drop |
| Same inode | Another inode, or not the recorded kind | An outside writer created D first (the `409` race) | Drop |
| Absent | Same inode | A crash after the rename; power loss after both syncs | Roll forward |
| Another inode | Same inode | The rename, then an outside writer recreated S | Roll forward (the scan gives S its own ID) |
| Same inode | Same inode | Power loss after D's folder sync, before S's | A **file**: roll forward (unlink S first). A **folder**: flag |
| Absent | Absent, or another inode | Only an outside writer moving or replacing the item | Flag |
| Another inode | Absent, or another inode | Only an outside writer replacing S before the rename | Flag |

**Delete** (state `moving`; source S, stamp folder F, entry E = `F/<name>`):

| S | F | E | Reached by | Action |
|---|---|---|---|---|
| Same inode | Absent | Absent | A crash before the `mkdir`; power loss before the trash's sync | Drop |
| Same inode | Present | Absent | A crash after the `mkdir`, before the rename; an `EEXIST` (F made outside) before the intent ended | Drop (F removed only if empty) |
| Absent | Present | Same inode | A crash after the rename; power loss after both syncs | Roll forward |
| Another inode | Present | Same inode | The rename, then an outside writer recreated S | Roll forward (the scan gives S its own ID) |
| Same inode | Present | Same inode | ADR-0022's power-loss duplicate | A **file**: roll forward (unlink S first). A **folder**: flag |
| Absent | Any | Absent or another inode | Only an outside writer moving the item | Flag (nothing to copy; the IDs stay) |

**Restore** (state `restoring`; entry E = `<op-id>/<name>` in the trash, destination D):

| E | D | Reached by | Action |
|---|---|---|---|
| Same inode | Absent | A crash before the rename; power loss before D's folder sync | Drop (record back to `trashed`) |
| Same inode | Another inode | An outside writer created D first (the `409` race) | Drop (record back to `trashed`; the user picks another destination) |
| Absent | Same inode | A crash after the rename; power loss after both syncs | Roll forward; the purge's cleanup removes the empty stamp folder |
| Same inode | Same inode | Power loss after D's folder sync, before the stamp folder's | A **file**: roll forward (unlink E first). A **folder**: flag |
| Absent | Absent or another inode | Only an outside writer (the trash is reserved: a shell user) | Flag |

**Purge** has no intent: the trash record's state is its intent (entry E):

| State | E | Reached by | Action |
|---|---|---|---|
| `trashed` | Present | Not yet due | None until the 7 days end |
| `purging` | Fully or partly present | A crash during the removal | Roll forward: resume the removal, then delete the record |
| `purging` | Absent | A crash after the removal, before the record's deletion | Roll forward: delete the record |
| `trashed` | Absent | Only a shell user removing it | Delete the record, with a warning log and an audit event; the tombstones stay |

A `moving` or `restoring` record follows its intent's table above. `internal/store/sql`'s
`TestIntentCrashPoints` covers each transaction these actions commit; each operation's PR
adds the crash test for its rows, under the process-kill and power-loss models.

## Consequences
- Positive: every PostelMD write is recoverable from its intent; the power-loss duplicate
  states that ADR-0003's tables left as interference are recognized by inode.
- Negative / costs:
  - two durable commits per save (ADR-0030);
  - deleted items now leave the trash after 7 days on their own, where v0.1 kept them until
    emptied by hand;
  - a folder move writes one row per file under it, in one transaction.
- Follow-up: **the intents slice** (after ADR-0031's IDs slice): save, create, move, delete,
  restore, purge and the sweep under intents, with a crash test for every row above under the
  process-kill and power-loss models (the roadmap lists them). It ships in parts, each with
  the recovery of the operations it brings:
  1. migration 0003 (`intents`), the store's transactions for every kind (begin, complete,
     drop, flag; the trash record's states), each with a kill-point test before and after its
     commit; and the power-loss model's file identity (`faultfs` writes a hard link where a
     power loss keeps one file under two names). Flips this ADR;
  2. saves (and restores from history) under intents; recovery, then the sweep, at start;
     interference into `recovery/`;
  3. creates;
  4. moves and renames of files and folders;
  5. deletes to the trash, the trash list from the records, and restores from the trash;
  6. the purge (hourly, and once ready), the empty stamp folders, `recovery/`'s 7 days.

  Follow-up issues: [#199](https://github.com/joaoheytor/PostelMD/issues/199) (interference:
  read-only paths and asking the user), [#200](https://github.com/joaoheytor/PostelMD/issues/200)
  (emptying the trash by hand).

## Alternatives considered
| Option | Why not |
|---|---|
| Keep the MVP's no-intent single renames, and fix the database after | A crash between the rename and the database leaves an ID on the wrong path or a missing tombstone, and only a hash would tell: one signal |
| Intents in a sidecar file per operation | A second crash-safe format beside a database that already has transactions |
| Judge a temp by its hash only | A power loss can leave the temp's entry beside the renamed target; only the inode says it is the same file |
| Purge under the vault mutex | Large trees would block saves; the record's state already excludes a restore |
| Keep the trash until emptied by hand | ADR-0003's 7 days bound the disk the trash takes; v0.1's rule was for a release with no records |

## Decision the maintainer may revisit
- **The automatic 7-day purge** (ADR-0003) ships with the intents slice. It is user-visible:
  v0.1 kept deleted items until the trash was emptied by hand. The CHANGELOG says so, and
  that items deleted under v0.1 get 7 days from the upgrade (ADR-0031's import clock).

## Clarifications
- **2026-11-18, how the operator decides.** See
  [ADR-0044](0044-resolving-a-flagged-intent-offline.md) (`postelmd intents resolve`).

