# ADR-0003: Save protocol: identity, conditional writes, atomic rename

- **Status:** Proposed
- **Date:** 2026-11-09 (revised after design review)

## Context
FrankMD's biggest bug category was data loss and races:
- autosave recreated deleted or renamed files;
- the tree showed stale entries;
- the revision check silently did nothing for months.

PostelMD is multi-user, and **external writers** (vim, `git pull`, Syncthing) edit the same
files, so saving must be designed up front, including what we *cannot* guarantee.

## Decision

### 1. Guarantee, precisely bounded
- **Between PostelMD clients: no lost updates.** Every mutation is conditional and serialized
  by the single server process (ADR-0014 enforces "single process").
- **Against uncoordinated external writers: no guarantee.** POSIX has no mandatory locking.
  Suppose PostelMD reads A and passes its final check, then an external tool installs B, and
  then PostelMD's `rename()` installs C. In that case, **B is lost, and the loss may be neither
  detectable nor recoverable.** PostelMD never read B, and watcher events can be missed. We only
  **narrow** the window:
  - immediately before `rename()`, re-`stat` the target (device, inode, size, mtime, ctime) and
    abort with `412` if it changed;
  - if the watcher reports a foreign write overlapping the save window, emit
    `postelmd_external_overwrite_suspected_total` and notify the user. This is a hint, not a
    guarantee.

  Committed git history can recover only versions that were committed.
- **Guaranteed safety for coordinated operations.** The supported way for external tools to
  write safely is **maintenance mode** (`postelmd vault lock <slug>`, or the API).
  1. PostelMD finishes its in-flight writes and stops writing.
  2. The external operation runs (a `git pull`, a bulk edit, a Syncthing burst).
  3. On unlock, a full reconcile runs.

  Continuous external sync (for example Syncthing always running) is supported with the
  uncoordinated caveat above, and the docs say so plainly.

### 2. Identity: path + file ID + generation + content
Each file has:
- a **file ID**: a stable ULID kept in the authoritative part of the DB (not the rebuildable
  index);
- a **generation**: a counter incremented on every write, rename and delete.

The revision token (`ETag`) is `"<fileID>.<generation>.<sha256-prefix>"`. A precondition only
matches when **ID, generation and hash all match**. So a file that was deleted and recreated
with identical bytes gets a **new ID** and an old autosave fails with `412`/`410`.

**Tombstones:** a delete keeps the ID's row as a tombstone (`deleted_at`, last path) for
30 days. A request for that ID returns `410 Gone`; nothing is ever recreated from it.

**ID survival:**
- across restarts and `reindex`: IDs live in authoritative tables, and `reindex` rebuilds only
  the FTS and link tables;
- across **external renames**: the reconciler keeps the ID **only for an unambiguous match**,
  meaning exactly one disappeared path and exactly one new path with that hash in the same
  pass. Any ambiguity (for example several identical template files) gives every new path a new
  ID and tombstones the old IDs. Open tabs on a tombstoned ID see `410` with a "file moved or
  deleted externally" dialog;
- after a crash during a **content write**: IDs are written in the same SQLite transaction
  that records the generation bump, *after* the rename succeeded. On startup, the reconciler
  repairs files whose hash doesn't match their recorded generation by bumping the generation;
- after a crash during an **application-managed structural operation** (create, move or rename
  of a file or folder, delete): identity is **never** left to hash matching. Before touching
  the filesystem, the operation writes a durable **structural intent** containing:
  - the op ID and type;
  - source and destination paths;
  - every affected `(fileID, generation, relative path)`;
  - the **source inode and device**, plus the content hash for files;
  - the **mechanism** (`rename-noreplace`, `link-unlink`, `create`, `delete`);
  - the planned post-state.

  On startup, recovery applies the table for **that operation and mechanism**. Applying the DB
  side and clearing the intent happen in one transaction. **Interference** (a state the
  operation can't produce by itself) never guesses: both sides are copied into
  `.postelmd/recovery/<op-id>/`, the vault is flagged, and the user decides. IDs are
  reassigned only after that decision.

  **Create** (temp file → `link()` or `renameat2(RENAME_NOREPLACE)` onto the destination):
  | Destination | Action |
  |---|---|
  | Absent | Not applied: remove the temp file, drop the intent |
  | Present, hash = intended | Applied: roll forward (insert the ID, generation 1) |
  | Present, other content | Interference |

  **Delete: files and folders (subtrees) alike.** Nothing is deleted in place. A delete is
  an atomic `renameat2(RENAME_NOREPLACE)` of the file or folder into the vault's trash,
  `<vault>/.postelmd-trash/<op-id>/`. The trash is on the same filesystem, so a single
  rename removes the whole subtree from the vault. Physical removal (**purge**) is a
  separate, resumable step.
  - The intent records the op ID, the source path and inode, every affected
    `(fileID, generation, relative path)` in the subtree, and a phase: `moving` → `trashed` →
    `purging`. The phase changes are written durably before each step.
  - Files that an external tool added under the folder between the intent and the rename have
    no recorded ID. They are trashed with the folder, so they stay recoverable in the trash,
    and the reconciler never resurrects them.

  | Phase | Source | Trash `<op-id>` | Action |
  |---|---|---|---|
  | `moving` | Present (same inode) | Absent | Not applied: drop the intent, IDs unchanged |
  | `moving` | Absent | Present (recorded inode) | Applied: roll forward, tombstoning **every recorded ID** in one transaction; phase → `trashed` |
  | `moving` | Present (different inode) | Present (recorded inode) | Applied, then recreated externally: roll forward the tombstones; the new path is reconciled as **new** (new IDs) |
  | `moving` | Absent | Absent | Interference: flag it, keep the IDs untombstoned, and ask the user (nothing to copy) |
  | `trashed` / `purging` | (any) | Present, fully or partly | Already applied: tombstones exist; **resume the purge** when retention expires (removal is idempotent and works bottom-up) |
  | `purging` | (any) | Absent | Purge completed: clear the intent |

  - **Undo and retention:** trashed items stay for **7 days**, the same as recovery copies.
    "Restore from trash" is a no-clobber rename back, which creates **new IDs**: tombstones are
    never revived, so old autosaves still get `410`.
  - The purge runs in the background, outside the vault lock (the trash is reserved and not
    visible to anything else), and is throttled for large subtrees.
  - Git: deleting means the paths are absent from the working tree, so the next auto-commit
    records the deletion. The trash is excluded through `.git/info/exclude` and is never
    committed.

  **Atomic rename or move** (`renameat2(RENAME_NOREPLACE)`, files and folders):
  | Source | Destination | Action |
  |---|---|---|
  | Present (same inode) | Absent | Not applied: drop the intent |
  | Absent | Present (same inode) | Applied: roll forward with the recorded ID mapping (IDs preserved even for identical files) |
  | Present | Present | Interference (an atomic rename can't leave both) |
  | Absent | Absent | Interference |

  **Two-step fallback** (`link()` then `unlink()`, files only, where `renameat2` isn't
  available):
  | Source | Destination | Action |
  |---|---|---|
  | Present | Absent | Not applied: drop the intent |
  | Present | Present, **same inode** as the source | Step 1 done: **complete** by unlinking the source, then roll forward |
  | Absent | Present (recorded inode) | Applied: roll forward |
  | Present | Present, different inode | Interference |
  | Absent | Absent | Interference |

  Folders are never moved with the two-step fallback (`link()` doesn't work on directories).
  Where `renameat2` is unavailable, a folder move uses `rename()` under the vault lock after
  checking that the destination is absent. The remaining window only matters against
  uncoordinated external writers (§1).

### 3. Operations
| Operation | Request | Preconditions | Outcome |
|---|---|---|---|
| Create | `POST …/files` `{path}` | `If-None-Match: *` | `201` + ETag; `412` if the path exists (including a concurrent create) |
| Update | `PUT …/files/{id}` | `If-Match` | `200` + new ETag; `412` with the current content on mismatch; `410` on a tombstone. **Never creates.** |
| Delete | `DELETE …/files/{id}` | `If-Match` | `204`; leaves a tombstone |
| Rename/move file | `POST …/files/{id}/move` `{to}` | `If-Match` on the source; the destination **must not exist** | **No-clobber**: `renameat2(RENAME_NOREPLACE)`, or `link()`+`unlink()` where that's unavailable; `409` if the destination exists |
| Folder rename/move | `POST …/folders/move` | A subtree lock; every open file under it must be clean | No-clobber; children keep their IDs and get a generation bump |
| Folder delete | `DELETE …/folders` | A subtree lock; every open file under it must be clean | An atomic move into the vault trash, then a resumable purge (§2); every recorded child ID is tombstoned |

The API addresses files **by ID** after creation. Paths are resolved to IDs on open.

### 4. Locking (server)
- One **vault-level mutex** serializes structural changes (create, move, folder ops) and git
  operations that touch the working tree (ADR-0016).
- **Per-file locks** for content updates. Multi-path operations take locks in a **fixed
  order**: vault lock first, then file locks sorted by path.
- Saves wait (bounded, 5 s) behind a running git operation, then fail with
  `503 Retry-After`. The client retries; nothing is lost because the draft persists.

### 5. Write path
1. Take the locks and re-read the current file. Verify ID, generation and hash.
2. Write a temp file in the same directory, fsync, re-`stat` the target (§1), `rename()`,
   fsync the directory.
3. Run the **synchronous secret scan** on the new content (bounded by the 5 MB note size
   limit; target < 20 ms p95). Findings are returned **in the save response** (ADR-0010).
4. In one SQLite transaction: bump the generation and record the new hash, the findings and
   an **outbox** row (`file.updated`, generation N).
5. Publish the event to the bus **explicitly from the outbox**. The watcher's own echo of
   this write is suppressed by `(path, hash)`; suppression never suppresses the application
   event.
6. Release the locks. Indexing and the git auto-commit are queued with `(fileID, generation)`.

### 6. Asynchronous jobs
- Index, link-extraction and commit jobs carry `(fileID, generation)`. A job **drops its
  result** if the file's current generation is newer (stale-result rejection).
- Work is durable (outbox) and bounded **per kind**, and each kind overflows differently:

  | Kind | Bound (per vault) | On overflow |
  |---|---|---|
  | Index obligations | 10k | Collapse to "reindex vault" (fully replaces them) |
  | Commit obligations | Not dropped | Collapse to "commit-scan vault": the working-tree diff against HEAD is the source of truth, so nothing is lost |
  | Client notifications | 10k events in the ring buffer | Drop the detailed events and publish an explicit `resync` |
  | Git and recovery intents | Never coalesced | Kept until completed or repaired (ADR-0016) |

  On startup, unprocessed outbox rows are replayed.
- Disk full / `EROFS` / `ENOSPC`: the save fails with `507 Insufficient Storage`, the vault is
  marked read-only, `/readyz` stays ready (reads still work), an alert metric is raised, and
  the client keeps the draft.

### 7. Client contract (`web/src/core/saveQueue`, `drafts`)
- Each document has a monotonically increasing **edit generation** (local, per keystroke
  batch). A save submits an immutable **snapshot** `{content, editGen, baseETag}`.
- A save response clears dirty state **only if** `editGen` equals the current edit generation.
  Otherwise the document stays dirty and the next save uses the new base ETag. **An older
  save's response can never clear a newer draft.**
- Debounce 1–2 s after the last keystroke, plus on blur or navigation. One request in flight
  per document; retries use backoff.
- **Remote events** (`file.updated` for an open document):
  - if the buffer is clean, apply them;
  - if it is dirty, **never overwrite it**: show "changed on server" and resolve with a 3-way
    merge (base = the snapshot's base content).
- `412` opens the conflict view: mine / theirs / merge.
- **Drafts are per editing session.** Each tab gets a persistent **editing-session ID**
  (`esid`, a ULID kept in `sessionStorage`, so it survives a tab reload).
  - **Cloned identities:** duplicating a tab, or opening a window that has an opener, can
    copy `sessionStorage`, and with it the `esid`. On load, the tab must **claim** its `esid`
    with the Web Locks API (`navigator.locks.request('esid:'+esid, {ifAvailable: true})`,
    held for the tab's lifetime) before writing any draft. If the lock is unavailable, another
    live tab owns that `esid`, so this tab mints a new one.
  - **Without Web Locks** (rare: every current major browser supports them), a timeout can't
    prove exclusive ownership. So the tab **never reuses** a stored `esid`: it mints a fresh
    one on every page load before any draft write. Drafts from earlier `esid`s show up in the
    recovery dialog on open, so a reload costs one extra recovery prompt and never causes a
    shared identity.
  - Drafts are keyed `instanceID/userID/vaultID/fileID/esid` and hold the base ETag and edit
    generation.
  - A save acknowledgement clears **only the draft with the same `esid` and edit generation**.
    It never touches another tab's draft.
  - On open, if drafts from other editing sessions (closed tabs, crashed browsers) exist for the
    file, the recovery dialog lists all of them.
  - Tabs coordinate over a `BroadcastChannel`: only one tab at a time edits a file, and the
    others follow read-only with a "take over editing" button. Offline tabs can't coordinate,
    so their drafts stay separate and surface in the recovery dialog.
  - Logout behaviour is defined in ADR-0018.

### 8. Limits
- Max note size: 5 MB (`413` above that).
- Max request body: 6 MB for notes. Uploads are covered by ADR-0009.
- Save timeout: 10 s server-side.

## Required tests
- Concurrent tab edits.
- Delete during a pending autosave.
- Delete and recreate with identical content (ABA).
- A crash at each phase of a folder delete (`moving`, `trashed`, a half-done `purging`),
  including an external file added mid-delete.
- Two offline tabs editing the same file: both drafts survive and are both offered for
  recovery.
- External rename of one of several identical files: new IDs, with no transfer of identity.
- Rename during an edit.
- Rename onto an existing destination (no-clobber).
- Folder move with an open dirty file.
- An external write during the save window: the re-`stat` aborts when it can. The test also
  documents the case that can't be detected.
- A stale save response after a newer edit.
- `ENOSPC` simulation.
- Crash between `rename()` and the DB commit (startup repair).
- A contract test that the client reads and uses the returned ETag.
- Fuzz tests on the operation sequence.

## Consequences
- Positive: no lost updates between PostelMD clients; honest, detectable behaviour for
  external writers; deterministic and testable.
- Negative: more state (IDs, generations, tombstones, outbox). Uncoordinated external writers
  can lose data in a narrow window that may go undetected; that is documented, and maintenance
  mode is the safe path.
- Metrics:
  - `postelmd_save_total{result}`;
  - `postelmd_save_conflicts_total`;
  - `postelmd_external_overwrite_suspected_total`;
  - `postelmd_outbox_pending`.

## Alternatives considered
| Option | Why not |
|---|---|
| Last write wins | Silent data loss |
| Content hash as the only revision | ABA: a delete+recreate with identical bytes passes the check |
| Advisory `flock` against external tools | They don't honour it; it gives false confidence |
| CRDT/OT real-time co-editing | Large complexity; conflicts with plain files and external writers; a Non-Goal |
| mtime-based revision | Unreliable across filesystems and sync tools |
