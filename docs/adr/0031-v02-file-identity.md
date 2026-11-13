# ADR-0031: v0.2 file identity: IDs, generations, renames and tombstones

- **Status:** Proposed (Accepted with the IDs slice, the first code that depends on it)
- **Date:** 2026-11-12

## Context
v0.2 brings [ADR-0003](0003-save-protocol.md) §2's identity into the product, on the
database of [ADR-0030](0030-v02-storage-sqlite.md). Without it, a note deleted and recreated
with the same bytes matches an old autosave (ABA), an outside rename looks like a delete, and
an open tab can't follow its note. The roadmap's v0.2 criteria: "an external rename keeps the
file ID when the content is unchanged; delete + recreate with the same content → the old
autosave gets 410/412". The ETag and API that carry the ID are
[ADR-0032](0032-v02-etag-and-api.md); the intents that keep it right across crashes are
[ADR-0033](0033-v02-intents.md).

Supersedes, in [ADR-0027](0027-landed-retries-and-the-adoption-marker.md), only the words
"an empty regular file" in "PostelMD owns a vault's reserved folders once
`.postelmd-tmp/.created-by-postelmd`, an empty regular file, exists": the marker now carries
the vault's identity. Ownership still depends only on its existence, and v0.1 reads it the
same way (it ignores the content).

## Decision

### File IDs
- Every regular file in a vault that `/api/v1/files` would list (reserved paths and symlinks
  excluded) has a **file ID**: a ULID, 48 bits of milliseconds and 80 bits from `crypto/rand`
  each time (never the monotonic mode, which derives one ID from the last), Crockford base32,
  made in `internal/store/sql` without a dependency. Folders have no ID. An ID is never
  reused. Its timestamp reveals when PostelMD first saw the file; that is accepted.
- `files` holds the live rows: ID, path (bytes as on disk, unique), generation, full SHA-256,
  size, `(device, inode, mtime, ctime)` last seen, and a state: `live` or `suspect`. Files
  over the store's 64 MiB read limit are hashed in a stream like any other; reading or saving
  them gets [ADR-0029](0029-a-save-over-a-file-too-large-to-read.md)'s answer.
- **First assignment** (the upgrade and `db rebuild`, ADR-0030): walk the vault; for each file
  `stat`, hash, `stat` again, and hash again if the two differ (a writer was busy; after
  three tries the file is recorded without a hash and the scan finishes it). Then **one
  transaction** inserts every row at generation 1, the vault UUID, the trash records below and
  the "assigned" flag.
- **Trash import.** Each existing `.postelmd-trash/` entry becomes a `trashed` record with its
  op ID (the stamp folder or old entry name), its name, and an unknown original path. Entries
  are read as [ADR-0024](0024-restore-from-the-trash.md)'s listing reads them: a stamp folder
  with one entry (ADR-0022's `<stamp>[-n]/<name>`) is that entry; an empty one is skipped (a
  crash's leftover; the purge removes it, ADR-0033); one with several entries, and a
  `<stamp>-<name>` entry, are old-layout entries. Symlinks and special files are skipped. Its
  files get no IDs: a restore gives new ones (ADR-0003).
- **An imported record's 7-day clock starts at the import**, not at the stamp: v0.1 kept
  deleted items until the trash was emptied by hand, so the automatic purge (ADR-0033) must
  not remove them in the first hour after the upgrade. The trash list shows each item's
  removal date; the upgrade runbook and the intents slice's CHANGELOG entry say so.

### The vault marker
- `.postelmd-tmp/.created-by-postelmd` holds `postelmd-vault 1`, the vault UUID and the
  format number, one per line. It is written by temp (`.created-by-postelmd.new` in the same
  folder), `fsync`, rename, `fsync` of the folder. `meta` stores the same UUID.
- ADR-0030's format table says what each combination of `format`, `app.db` and marker does.
  In short, startup **refuses**, changing nothing, when the marker names another UUID (a
  swapped mount), names a UUID with no `app.db`, or is missing beside a database with live
  rows. An empty marker (ADR-0027's printed fix recreates it empty) is rewritten with the
  stored UUID only if most live rows are found at their paths with the same inode and size;
  otherwise it is refused like a missing one. `postelmd vault claim` writes the marker after
  the operator confirms that this is the vault.

### The startup scan
After intent recovery (ADR-0030's order), the scan walks the vault. Per path it `stat`s
first and hashes only when `(size, mtime, ctime, inode)` changed. It applies its results under
the vault mutex, re-checking each path first (ADR-0004):
- a changed file: generation + 1, new hash and stat;
- a path in `files` that is missing: `suspect` (below);
- a new path: the rename rules below, else a new ID at generation 1.

Until the watcher slice, the scan runs at start and once more 10 s later for the suspects and
pending paths. A read or `git status` that meets a path the scan hasn't seen gives it an ID
(or resolves its pending match) in one transaction, under the mutex.

### Absence, outside renames and pending paths (ADR-0003 §2, made concrete)
- **Absence takes two passes**: a missing path is `suspect`; it is tombstoned, or its ID
  moved, only if a pass at least 10 s later still misses it and no intent names it. A save to
  a suspect gets ADR-0032's `file-missing` answer; if the path is back when the save checks,
  the suspicion is cleared and the save goes on under the normal rules.
- **The ID moves only on agreeing signals**, never on one:
  - **Rule 1 (`mv`)**: same full SHA-256, same `(device, inode)` **and** same mtime as a
    suspect or missing row. The ID moves at once, without the second pass, even when other
    files have the same bytes. (`cp` can reuse a freed inode, but sets a new mtime.)
  - **Rule 2 (copy and delete, or a tool that rewrites)**: same full SHA-256, another inode,
    and it is the **only** missing row and the **only** new path with that hash. The new path
    is held **pending**, with no ID, until the suspect is resolved: if the second pass still
    misses the old path, the ID moves; if the old path is back, the pending path gets a new
    ID. A read of a pending path resolves it under the mutex once at least 10 s have passed
    since the suspect was first seen (old path still missing → the ID moves; back → a new ID);
    before that, the read answers `503` with `Retry-After` set to the seconds left, and the
    client keeps its draft and retries.
  - Anything else (several candidates, an inode match with other bytes or another mtime, a
    rename plus an edit): new ID for the new path; the old row is tombstoned
    (`ambiguous-rename` or `missing`).
- A moved ID gets a generation bump (ADR-0032 says what open tabs see).

### Generations
Start at 1, grow by one, never reset, on: a PostelMD save that writes (a save of equal bytes
doesn't), a restore from history, a move or rename of the file or of a folder above it, an
outside content change, and an outside rename that keeps the ID. A delete ends the ID; its
tombstone records the last generation. A create and a restore from the trash give **new IDs**
at generation 1: tombstones never come back.

### Tombstones
`tombstones` keeps ID, last path, last generation, time and reason (`deleted`, `trashed`,
`missing`, `ambiguous-rename`) for **30 days**, apart from the trash's 7 (ADR-0003); a
background job removes expired rows without the vault mutex. A tombstoned ID gets `410`;
an ID never seen or purged gets `404 unknown-file` (ADR-0032).

**Notes for v0.3** (multiple users; [ADR-0018](0018-account-and-session-lifecycle.md)):
check the ID's syntax before any lookup, check membership before choosing between `410` and
`404`, and never return a tombstone's last path.

## Consequences
- Positive: the ABA case closes once the ETag carries the ID; `mv` outside keeps IDs even
  among identical files; a swapped vault mount is refused instead of renumbered.
- Negative / costs:
  - a rename plus an edit outside PostelMD, or a copy-and-delete of one of several identical
    files, gives a new ID (documented);
  - a rule-2 rename waits up to one pass before the ID moves;
  - after a rollback, going forward assigns new IDs (the runbook empties the marker, so it is
    a normal first upgrade; `db rebuild` if that step was skipped; ADR-0030);
  - a marker deleted by hand stops startup until `postelmd vault claim`;
  - a read of a pending path can wait up to 10 s.
- Follow-up: **the IDs slice** (flips this ADR): `files`, `tombstones`, trash import, the first
  assignment, the marker, the scan with suspects, pending paths and both rename rules; data
  format 2 and its runbook (ADR-0030). IDs aren't on the wire yet, and PostelMD's own writes
  don't touch `files` until ADR-0033's slice: the next scan sees them as outside changes, which
  is safe because no client holds an ID. `app.db` is wired into `serve` before it, at data
  format 1 (#175: the boot UUID, listen-early `/readyz`; the snapshot before migrating came
  with #173); this slice brings format 2, with `db rebuild` and `vault claim` (ADR-0030,
  Consequences, follow-up 2, and its clarification of 2026-11-13).

## Alternatives considered
| Option | Why not |
|---|---|
| Rename match by hash only (ADR-0003's first text) | Loses the common `mv` of one of several identical files |
| Rename match by inode only | Inodes are reused after a delete: one signal |
| Inode and hash without mtime | `cp` over a freed inode with the same bytes would steal the ID |
| Give a rule-2 path a new ID now and merge later | Clients would see two IDs for one file, and one would be tombstoned under them |
| IDs in an extended attribute or a sidecar per file | Lost by `cp`, git checkout, Syncthing and many editors; a second write per save |
| Vault identity only in `app.db` | A swapped mount can't be told from outside changes |

## Decisions on the review's open questions (maintainer may revisit)
1. **Tombstones are kept 30 days**, apart from the trash's 7.
2. **A swapped vault mount is detected** by the marker's UUID and refused.
