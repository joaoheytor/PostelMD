# ADR-0033: v0.2 intents: saves, creates and structural operations

- **Status:** Proposed (Accepted with the intents slice, the first code that depends on it)
- **Date:** 2026-11-12

## Context
From v0.2 every PostelMD write changes a file **and** the database
([ADR-0031](0031-v02-file-identity.md)): two side effects, so each needs a durable intent
before its first filesystem step and a recovery table per crash point (CLAUDE.md rule 14).
[ADR-0003](0003-save-protocol.md) §2 and §5 designed them; this ADR fixes the subset v0.2
builds and the rows ADR-0003 lacks.

Supersedes these parts of Accepted ADRs, and nothing else in them:
- [ADR-0020](0020-mvp-scope.md), "Saving": "Its server side is replaced, for the MVP, by
  single-rename operations that need no intents".
- [ADR-0022](0022-mvp-trash-layout.md): "The first side effect holds no data, so no intent is
  needed".
- [ADR-0024](0024-restore-from-the-trash.md): the "**No intent**" bullet.
- [ADR-0027](0027-landed-retries-and-the-adoption-marker.md)'s sweep bullet, "with the marker:
  removes **every other** entry in `.postelmd-tmp/` … and keeps the marker": the sweep now runs
  after intent recovery and spares what an open intent names (below).

## Decision

### Common rules
- An intent is a row in `intents` (op ID, kind, payload), committed with `synchronous=FULL`
  ([ADR-0030](0030-v02-storage-sqlite.md)) **before** the first filesystem step, and deleted in
  the transaction that records the result. It holds no note content. An op ID is 32 random
  hex digits (the MVP's temp names), except a delete's, which is its trash stamp (ADR-0022).
- The **one vault mutex** of ADR-0020 stays (no per-file locks), held from the precondition
  check to that transaction. No filesystem I/O happens inside a transaction (ADR-0030).
- Recovery runs at start, before the sweep, the scan and HTTP (ADR-0030's order). It judges
  each intent on **the intent, the target's hash, and the temp's hash and inode**, never on
  one of them.
- **Temp names.** Recovery accepts an intent's temp only as a direct child of
  `.postelmd-tmp/` whose name is the 32-hex op ID; anything else in an intent is interference.
  Temps are opened through `os.Root` with `O_NOFOLLOW`.
- **The sweep** (replaces ADR-0027's bullet): after recovery, every entry of `.postelmd-tmp/`
  that no open or flagged intent names is removed, except the marker.
- **Mechanisms.** The vault refuses filesystems without `RENAME_NOREPLACE` (ADR-0020), so v0.2
  implements only ADR-0003's `rename-noreplace` tables; `link-unlink` and `rename-checked`
  come with ADR-0006's fallbacks.
- Interference (a state an operation can't produce by itself) keeps both sides in
  `.postelmd/recovery/<op-id>/`, flags the vault and asks the user (ADR-0003 §2).

### Save
Intent: op ID, file ID, base generation and full hash, new full hash, path, the target's
`stat` at the check, temp name. Steps:
1. under the mutex, read and hash the target (ADR-0032's precondition rules);
2. commit the intent;
3. temp `.postelmd-tmp/<op-id>` (`O_EXCL`, `0600`, the target's mode by `fchmod`), write,
   `fsync`;
4. re-`stat` the target; if it changed, remove the temp and drop the intent, then read the
   target again and record the outside change (generation + 1, its own transaction, as in
   ADR-0032), so the `412` carries the current note and an ETag that describes it (or
   `file-missing` if the target is gone);
5. `renameat` onto the target;
6. `fsync` the target's folder, then `.postelmd-tmp/`;
7. one transaction: generation + 1, hash, stat, outbox row, intent deleted;
8. `204` with the new ETag.

| Crash after | On disk (kill; power loss adds) | Recovery |
|---|---|---|
| Before (2) commits | No intent; no temp | Nothing. The client never got `204`; its retry goes through |
| (2), during (3) | Target = base; temp absent or partial | Not applied: remove the temp, drop the intent |
| (3), during (4) or (5) | Target = base; temp = new | Not applied, as above |
| (5), before (6) | Kill: target = new, no temp. Power loss: also target = base, temp = new | New: applied, roll forward. Base: not applied |
| The target's folder synced, not `.postelmd-tmp/` | Power loss: target = new **and** the temp's entry back, **same inode** as the target | **Applied**: unlink the temp name, roll forward |
| (6), before (7) commits | Target = new; no temp | Applied: roll forward in one transaction |
| (7) | Done | A retry with the old ETag: equal bytes, `204` |

The rest of ADR-0003 §5's table holds: target with other content, an absent target, or a temp
with the new hash and **another** inode (another writer installed the same bytes) are
interference or outside changes as written there.

The synchronous secret scan of ADR-0003 §5 step 5, and findings in the save response, are
**deferred** to the secrets slice (ADR-0010 gate 3); until then step 7 records no findings.

### Create
Intent: op ID, destination path, new full hash, temp name. Steps: temp + `fsync`;
`renameat2(RENAME_NOREPLACE)` onto the destination; `fsync` the destination's folder, then
`.postelmd-tmp/`; one transaction: a new ID at generation 1, outbox row, intent deleted;
`201`. Recovery is ADR-0003 §2's **Create** table (destination absent → not applied;
destination with the intended hash → applied, roll forward with a new ID; other content →
appeared outside, the temp kept in recovery), plus the save's power-loss row: a temp entry
with the **same inode** as the destination is unlinked, then applied.

### Structural operations
Each intent holds op ID, kind, source and destination, the source's `(device, inode)`, every
affected `(file ID, generation, relative path)`, and the mechanism.
- **Move or rename** (file or folder): ADR-0003's "Atomic rename or move" table. Applied:
  every affected row gets its new path and a generation bump, IDs kept.
- **Delete**: ADR-0022's steps, its stamp as the op ID, and a trash record (`moving` →
  `trashed`) committed before each step; ADR-0003's delete table. Applied: every affected ID
  is tombstoned (`trashed`). Added row, for ADR-0022's power-loss duplicate: a **file** with
  the same inode at the source and in the stamp folder is unlinked at the source, then
  applied; a folder in both places is interference.
- **Restore from the trash**: ADR-0024's steps, with the record moved `trashed` → `restoring`
  by compare-and-set first; ADR-0003's restore table. Applied: new IDs, the record dropped.
  A record in `purging` or `restoring` refuses with `409`.
- **Purge** (emptying the trash by hand, and automatically 7 days after the delete, as
  ADR-0003 designed; for records imported at the upgrade, 7 days after the import,
  ADR-0031): a background job, hourly and after readiness, without the vault mutex (the trash
  is reserved and the record's state excludes a restore):
  1. compare-and-set the record `trashed` → `purging`, committed **before the first removal**;
  2. remove the entry bottom-up (idempotent, resumes after a crash);
  3. delete the record. Tombstones stay.

  ADR-0003's rows apply: `purging` with the entry partly present resumes; with the entry gone,
  the record is dropped. The job also removes **empty stamp folders that no record names**
  (left by a restore that crashed after dropping its record, or by a delete that crashed
  after its `mkdir`, ADR-0022), with a remove that only takes an empty folder.
- **Restore from history** is a save.

## Consequences
- Positive: every PostelMD write is recoverable from its intent; the power-loss duplicate
  states that ADR-0003's tables left as interference are recognized by inode.
- Negative / costs:
  - two durable commits per save (ADR-0030);
  - deleted items now leave the trash after 7 days on their own, where v0.1 kept them until
    emptied by hand;
  - a folder move writes one row per file under it, in one transaction.
- Follow-up: **the intents slice** (after ADR-0031's IDs slice; flips this ADR): save, create,
  move, delete, restore, purge and the sweep under intents, with a crash test for every row
  above under the process-kill and power-loss models (the roadmap lists them).

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

