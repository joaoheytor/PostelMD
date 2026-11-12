# ADR-0003: Save protocol: identity, conditional writes, atomic rename

- **Status:** Proposed
- **Date:** 2026-11-09 (revised after design review)
- **MVP scope (ADR-0020):** the MVP uses the client contract (§7), the atomic write path (§5) and a content-hash ETag with single-rename moves and trash deletes; file IDs, generations, intents and the outbox arrive in v0.2.

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
30 days. A request for that ID returns `410 Gone`; nothing is ever recreated from it. An ID
never seen, or whose tombstone has expired, returns `404 {"code": "unknown-file"}`
([ADR-0032](0032-v02-etag-and-api.md)).

**ID survival:**
- across restarts and `reindex`: IDs live in authoritative tables, and `reindex` rebuilds only
  the FTS and link tables;
- across **external renames**: the reconciler keeps the ID **only when two signals agree**
  ([ADR-0031](0031-v02-file-identity.md)): the same hash, `(device, inode)` and mtime as the
  missing path (a `mv`, kept even among identical files), or the same hash with exactly one
  disappeared path and exactly one new path with that hash (the new path waits, pending,
  for the second pass). Any other ambiguity (for example several identical template files) gives every new path a new
  ID and tombstones the old IDs. Open tabs on a tombstoned ID see `410` with a "file moved or
  deleted externally" dialog;
- across **brief absences**: editors and sync tools replace files by rename, so one scan can
  miss a file that is about to come back. A path absent in one reconcile pass is only
  **suspected missing**. It is tombstoned, or its ID moved by the rename match above, only if
  a **second pass** at least 10 s later (scheduled for the suspects, not the full interval)
  still finds it absent and no intent names it. If it is back, the suspicion is dropped and the
  ID kept (with a generation bump if the content changed). While suspected, a save to it gets
  `412` and the client keeps its draft;
- after a crash during a **content write**: the save intent (§5) decides, never a hash
  comparison alone. The generation bump, new hash and outbox row commit in the transaction
  that clears the intent, *after* the rename is durable;
- after a crash during an **application-managed structural operation** (create, move or rename
  of a file or folder, delete, restore from trash): identity is **never** left to hash matching. Before touching
  the filesystem, the operation writes a durable **structural intent** containing:
  - the op ID and type;
  - source and destination paths;
  - every affected `(fileID, generation, relative path)`;
  - the **source inode and device**, plus the content hash for files;
  - the **mechanism** (`rename-noreplace`, `link-unlink`, `rename-checked`, `create`,
    `delete`, `restore`);
  - the **temp path**, if the operation writes one (§5);
  - the planned post-state.

  On startup, recovery applies the table for **that operation and mechanism** (the order of
  startup is in ADR-0004). Applying the DB side and clearing the intent happen in one
  transaction. "Same inode" always means the same device and inode as recorded.

  **Any state not listed in the table is interference.** Interference (a state the
  operation can't produce by itself) never guesses: both sides are copied into
  `.postelmd/recovery/<op-id>/`, the vault is flagged, the recorded paths stay read-only, and
  the user decides. IDs are reassigned only after that decision. A row that says "appeared
  externally" is not interference: the operation provably didn't touch that path, so the
  reconciler handles it as any external change.

  **Mechanisms.** `renameat2(RENAME_NOREPLACE)` is used wherever it works. Some filesystems
  reject it with `EINVAL` (9p, some FUSE and overlay setups; found by the confinement spike,
  ADR-0006). There, files use `link-unlink` and folders use `rename-checked`. The intent
  records the mechanism actually used, and recovery applies that mechanism's table.

  **Create** (temp file → `renameat2(RENAME_NOREPLACE)`, or `link()` then unlink the temp):
  | Destination | Temp | Action |
  |---|---|---|
  | Absent | Any | Not applied: remove the temp, drop the intent |
  | Present, hash = intended | Absent, or present (`link` done, unlink not) | Applied: remove any temp; roll forward (insert the ID, generation 1) |
  | Present, hash = intended | Present, **same inode** as the destination (a power loss undid the temp folder's side of a `rename-noreplace`) | Applied: unlink the temp name, roll forward ([ADR-0033](0033-v02-intents.md)) |
  | Present, other content | Any | Appeared externally (the no-clobber step failed or never ran): keep a complete temp in recovery, drop the intent; the reconciler gives the file a new ID |

  **Delete: files and folders (subtrees) alike.** Nothing is deleted in place. A delete
  creates a fresh, empty `<vault>/.postelmd-trash/<op-id>/` (`mkdir`, which fails if it
  exists) and renames the file or folder to **`<op-id>/<basename>`**. The trash is on the same
  filesystem, so one rename removes the whole subtree from the vault. Physical removal
  (**purge**) is a separate, resumable step.
  - The destination is inside a directory this operation just created, so it is absent
    without `RENAME_NOREPLACE`. Where `renameat2` is unavailable, a plain `rename()` is
    no-clobber here (the **delete fallback**), for files and folders alike.
  - A **trash record** in the DB holds the op ID, the original path and basename, every
    affected `(fileID, generation, relative path)` in the subtree, who deleted it and when,
    and a state: `moving` → `trashed` → `purging`, or `trashed` → `restoring` (restore,
    below). Each state change is committed before its step.
  - Files that an external tool added under the folder between the intent and the rename have
    no recorded ID. They are trashed with the folder, so they stay recoverable in the trash,
    and the reconciler never resurrects them.

  | State | Source | `<op-id>/` | `<op-id>/<basename>` | Action |
  |---|---|---|---|---|
  | `moving` | Same inode | Absent | Absent | Not applied: drop the intent, IDs unchanged |
  | `moving` | Same inode | Present, empty | Absent | Not applied (crash after `mkdir`): remove `<op-id>/`, drop the intent |
  | `moving` | Absent | Present | Same inode | Applied: tombstone **every recorded ID** in one transaction; state → `trashed` |
  | `moving` | Same inode | Present | Same inode | A power loss kept both entries (ADR-0022's duplicate). A **file**: unlink the source name, then as "Applied". A folder: interference ([ADR-0033](0033-v02-intents.md)) |
  | `moving` | Other inode | Present | Same inode | Applied, then recreated externally: roll forward the tombstones; the new path is reconciled as **new** (new IDs) |
  | `moving` | Absent | Any | Absent | Interference: flag it, keep the IDs untombstoned, and ask the user (nothing to copy) |
  | `trashed` / `purging` | Any | Present | Fully or partly present | Already applied: tombstones exist; **resume the purge** when retention expires (removal is idempotent and works bottom-up) |
  | `purging` | Any | Absent | Absent | Purge completed: drop the trash record |
  | `trashed` | Any | Absent | Absent | Removed outside PostelMD (the trash is reserved, so only a shell user can): log a warning and an audit event, drop the trash record. Nothing is left to protect, and the tombstones stay. |

  - **Retention:** trashed items stay for **7 days**, the same as recovery copies.
  - The purge runs in the background, outside the vault lock, and is throttled for large
    subtrees. It starts only by a **compare-and-set** of the trash record from `trashed` to
    `purging`, so it can never run at the same time as a restore. The trash is reserved
    (ADR-0006): the watcher, index, git and every API except the trash API (list, restore)
    ignore it. It is still visible to the trash API, to backups (ADR-0017) and to anyone with
    shell access to the volume.
  - Git: deleting means the paths are absent from the working tree, so the next auto-commit
    records the deletion. The trash and `.postelmd-tmp/` (§5) are excluded through
    `.git/info/exclude` and are never committed.

  **Restore from trash** is a structural operation of its own. It takes the vault lock, then
  moves the trash record from `trashed` to **`restoring`** by compare-and-set (it fails with
  `409` if the record is already `purging` or `restoring`). Its intent records the trash op
  ID, the entry's inode, the destination (the original path, or one the user chooses) and the
  mechanism. It then moves `<op-id>/<basename>` to the destination without clobbering:
  `rename-noreplace`, or `link-unlink` for a file and `rename-checked` for a folder. A
  restored item always gets **new IDs**: tombstones are never revived, so old autosaves still
  get `410`.
  | Entry `<op-id>/<basename>` | Destination | Action |
  |---|---|---|
  | Same inode | Absent | Not applied: state → `trashed`, drop the intent |
  | Same inode | Present, other inode | Destination appeared externally: state → `trashed`, drop the intent; the user picks another destination |
  | Absent | Same inode | Applied: assign new IDs to the subtree, drop the trash record and `<op-id>/` |
  | Same inode | Same inode (`link` done, unlink not) | Complete the unlink, then as "Applied" |

  **Atomic rename or move** (`renameat2(RENAME_NOREPLACE)`, files and folders):
  | Source | Destination | Action |
  |---|---|---|
  | Same inode | Absent | Not applied: drop the intent |
  | Same inode | Other inode | Not applied; the destination appeared externally (the no-clobber rename failed or never ran): drop the intent |
  | Absent | Same inode | Applied: roll forward with the recorded ID mapping (IDs preserved even for identical files) |
  | Other inode | Same inode | Applied, then the source was recreated externally: roll forward; the new source is reconciled as **new** |

  **Two-step fallback** (`link-unlink`, files only, where `renameat2` isn't available):
  | Source | Destination | Action |
  |---|---|---|
  | Same inode | Absent | Not applied: drop the intent |
  | Same inode | Other inode | Not applied; the destination appeared externally (`link()` fails with `EEXIST`): drop the intent |
  | Same inode | Same inode | Step 1 done: **complete** by unlinking the source, then roll forward |
  | Absent | Same inode | Applied: roll forward |
  | Other inode | Same inode | Applied, then the source was recreated externally: roll forward; the new source is reconciled as **new** |

  **Checked rename** (`rename-checked`, folders only, where `renameat2` isn't available;
  `link()` doesn't work on directories): under the vault lock, `lstat` the destination, then
  `rename()` if it is absent. Plain `rename()` replaces an **empty** destination directory
  and fails with `ENOTEMPTY` or `EEXIST` on anything else, so the only thing an external
  writer can lose in the window between the check and the rename is an empty folder it just
  created (§1).
  | Source | Destination | Action |
  |---|---|---|
  | Same inode | Absent | Not applied: drop the intent |
  | Same inode | Other inode | Not applied; the destination appeared externally: drop the intent |
  | Absent | Same inode | Applied: roll forward with the recorded ID mapping |
  | Other inode | Same inode | Applied, then the source was recreated externally: roll forward; the new source is reconciled as **new** |

### 3. Operations
| Operation | Request | Preconditions | Outcome |
|---|---|---|---|
| Create | `POST …/files` `{path}` | `If-None-Match: *` | `201` + ETag; `412` if the path exists (including a concurrent create) |
| Update | `PUT …/files/{id}` | `If-Match` | `200` + new ETag; `412` with the current content on mismatch, **unless the current content equals the submitted content** (`200`, idempotent; §5); over a file past the store's read limit, `412` with no content and no ETag ([ADR-0029](0029-a-save-over-a-file-too-large-to-read.md)); `410` on a tombstone. **Never creates.** |
| Delete | `DELETE …/files/{id}` | `If-Match` | `204`; leaves a tombstone |
| Rename/move file | `POST …/files/{id}/move` `{to}` | `If-Match` on the source; the destination **must not exist** | **No-clobber**: `renameat2(RENAME_NOREPLACE)`, or `link()`+`unlink()` where that's unavailable; `409` if the destination exists |
| Folder rename/move | `POST …/folders/move` | A subtree lock; every open file under it must be clean | No-clobber: `renameat2(RENAME_NOREPLACE)`, or `rename-checked` where that's unavailable (§2); children keep their IDs and get a generation bump |
| Folder delete | `DELETE …/folders` | A subtree lock; every open file under it must be clean | An atomic move into the vault trash, then a resumable purge (§2); every recorded child ID is tombstoned |
| Restore from trash | `POST …/trash/{op-id}/restore` `{to?}` | The trash record is `trashed`; the destination (default: the original path) **must not exist** | No-clobber (§2); new IDs; `409` if the record is being purged or restored, or the destination exists |

The API addresses files **by ID** after creation. Paths are resolved to IDs on open.

**v0.2** ([ADR-0032](0032-v02-etag-and-api.md)) builds a subset of this table: create is
`PUT …/notes/{path}` with `If-None-Match: *` (`201`, with the ID in `PostelMD-File-ID`);
saves are `PUT …/files/{id}` (or the path alias `PUT …/notes/{path}`) and answer **`204`**
where this table says `200`; delete, folder operations, move and trash restore keep their
v0.1 path routes, with an optional `If-Match`, until v0.3. A missing or suspect path answers
`412 {"code": "file-missing"}` without an ETag.

### 4. Locking (server)
- One **vault-level mutex** serializes structural changes (create, move, folder ops) and git
  operations that touch the working tree (ADR-0016).
- **One vault mutex for content updates too**, as in the MVP (ADR-0020) and v0.2
  ([ADR-0033](0033-v02-intents.md)): no per-file locks. If measurements ever call for them,
  multi-path operations take locks in a **fixed order**: vault lock first, then file locks
  sorted by path.
- Saves wait (bounded, 5 s) behind a running git operation, then fail with
  `503 Retry-After`. The client retries; nothing is lost because the draft persists.

### 5. Write path, temp files and durability

**Temp files** live in **`<vault>/.postelmd-tmp/`**, never next to the target:
- Siblings like `note.md.tmp` are seen by external tools (Syncthing can sync a half-written
  one; `git status`, editors and search show them), and a crash leaves them in user folders.
- The directory is at the vault root, on the same filesystem as the vault, so `renameat` from
  it is atomic. It is **reserved** (ADR-0006), excluded from the watcher, index, git
  (`.git/info/exclude`) and backups (ADR-0017), and created `0700`.
- A temp file is `<op-id>`, created with `O_CREAT|O_EXCL`, mode `0600`. Before the rename it
  takes the target's permission bits (`fchmod`); a create uses the vault default.
- Every temp path is **recorded in the operation's intent** before the file is created.
- `EXDEV` (a sub-folder mounted from another filesystem) fails the operation with `500`, a
  metric and a log line. It **never falls back** to copying, which isn't atomic.
- **Startup sweep:** after intent recovery (which handles every temp an open intent names),
  any entry in `.postelmd-tmp/` that no open intent names is removed.

**Save intent.** A content save has two side effects (the file, then the database), so it
writes a durable intent first (CLAUDE.md rule 14): op ID, file ID, base generation and hash,
the new content's hash, the target path, the target's `stat` at the precondition check, and
the temp path. It holds no content: until the client gets a `200`, its draft is the copy.

1. Take the locks and re-read the current file. Verify ID, generation and hash. If the
   precondition fails, apply the **idempotent rule** below; otherwise return `412`.
2. If the submitted content equals the current content, return `200` with the current ETag;
   nothing is written.
3. Commit the save intent.
4. Write the temp file and `fsync` it. Re-`stat` the target (§1); if it changed, remove the temp,
   drop the intent and return `412`. `renameat()` onto the target, then `fsync` the target's directory and
   `.postelmd-tmp/`.
5. Run the **synchronous secret scan** on the new content (bounded by the 5 MB note size
   limit; target < 20 ms p95). Findings are returned **in the save response** (ADR-0010).
   Deferred in v0.2 until the secrets slice (ADR-0033): until then step 6 records no
   findings.
6. In one SQLite transaction: bump the generation, record the new hash and the findings, add
   an **outbox** row (`file.updated`, generation N), and clear the intent.
7. Publish the event to the bus **explicitly from the outbox**. The watcher's own echo of
   this write is suppressed by `(path, hash)`; suppression never suppresses the application
   event.
8. Release the locks. Indexing and the git auto-commit are queued with `(fileID, generation)`.

**Save recovery** (startup, per open save intent; the temp is judged by its hash, so a
partly written temp never counts as the new content):

| Target | Temp | Action |
|---|---|---|
| Base hash | Any | Not applied: remove the temp, drop the intent. The client's retry succeeds. |
| New hash | Absent | Applied: roll forward (scan, generation, hash, findings, outbox) and clear the intent in one transaction |
| New hash | Present, **same inode** as the target | Applied: a power loss undid the temp folder's side of the rename. Unlink the temp name, then roll forward ([ADR-0033](0033-v02-intents.md)) |
| New hash | Present, new hash, another inode | An external writer installed the same bytes: remove the temp, drop the intent; the reconciler records an external change |
| Other content | Present, new hash | Interference: move the temp to `.postelmd/recovery/<op-id>/`, drop the intent, flag it to the user; the reconciler records the external change |
| Other content | Absent or partial | Interference: remove any temp, drop the intent; the reconciler records the external change. Nothing is lost: the client never got a `200`, so its retry gets `412` and the conflict view shows its draft |
| Absent | Present, new hash | External delete or move: keep the temp in recovery as above; the reconciler handles the absence (ADR-0004) |
| Absent | Absent or partial | External delete or move: remove any temp, drop the intent; the client's retry gets `410` or `412` with its draft intact |

**Idempotent retries.** A precondition mismatch where the file ID is live and the **current
content hash equals the hash of the submitted content** returns `200` with the current ETag
and the current findings, and writes nothing. The client's snapshot is exactly what is on
disk, so clearing its dirty state (for that edit generation, §7) loses nothing. This covers a
retry whose first response was lost after the commit, and two tabs saving identical text.
It is ABA-safe because the ID must match: a deleted and recreated file has a new ID.

**Durability.** The rules, for every mechanism:
- An intent is **committed** before the operation's first filesystem side effect.
- File data is `fsync`ed before it is renamed or linked into place.
- After any directory entry changes (create, rename, link, unlink), that directory is
  `fsync`ed; a rename between directories `fsync`s both.
- The database records an operation as done only after its filesystem steps are durable.
- SQLite runs in WAL mode with **`synchronous=FULL`**. With `NORMAL`, the latest commits can
  be lost on power loss, which would lose intents and generation bumps.

| Mechanism | Order |
|---|---|
| Content save | intent → temp + `fsync` → re-`stat` → `renameat` → `fsync` target dir and `.postelmd-tmp/` → DB |
| Create | intent → temp + `fsync` → `renameat2(RENAME_NOREPLACE)`, or `link` then `unlink` the temp → `fsync` destination dir and `.postelmd-tmp/` → DB |
| Rename or move | intent → `renameat2(RENAME_NOREPLACE)` → `fsync` destination dir, then the source dir if different → DB |
| `link`/`unlink` fallback | intent → `link` → `fsync` destination dir → `unlink` → `fsync` source dir → DB |
| Delete | intent (`moving`) → create `.postelmd-trash/<op-id>/`, `fsync` the trash → rename into it → `fsync` the source dir and `<op-id>/` → DB (tombstones, `trashed`). The purge needs no per-step `fsync`: removal is idempotent and resumes. |
| Checked rename | intent → `lstat` destination → `rename()` → `fsync` destination dir, then the source dir if different → DB |
| Restore from trash | intent (record `restoring`) → move the entry by its mechanism → `fsync` destination dir and `<op-id>/` → DB (new IDs, drop the record) → remove the empty `<op-id>/`, `fsync` the trash |
| Git | Objects, refs and their directories (ADR-0016, "Durability") |

An **`fsync` error is fatal** for the operation. After a failed `fsync`, the kernel may have
dropped the dirty pages and a second `fsync` can report success, so nothing retries it. The
operation fails, the vault goes read-only (as for `ENOSPC`, §6), an alert metric is raised,
and the intent's recovery table runs on the next startup.

**Crash models in the scenario suite.** A process kill keeps everything written, because the
page cache survives. **Power loss** also drops what wasn't synced. In test builds,
`internal/store/fs` and the `.git/` adapter log every mutation and `fsync`. For each crash
point, the suite rebuilds the disk state from synced operations only (unsynced file data
reverts to its last synced content, and unsynced directory entries are undone), runs
recovery, and asserts the table's outcome. A committed SQLite transaction counts as durable.

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
- **Drafts are per note session.** Each tab gets a persistent **editing-session ID**
  (`esid`, a ULID kept in `sessionStorage`, so it survives a tab reload). Edit generations
  restart each time a note is opened, so each opening of a note in the tab is a **note
  session** with its own **draft key** `<esid>.<n>` (`n` counts the tab's note sessions;
  [ADR-0023](0023-a-draft-key-per-note-session.md)). The `esid` stays per tab.
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
  - Drafts are keyed `instanceID/userID/vaultID/fileID/<draft key>` and hold the base ETag
    and edit generation. A reload moves the drafts of every draft key of the tab's `esid` to
    a fresh one before any write.
  - A save acknowledgement clears **only the draft with the same draft key, up to that edit
    generation**. It never touches another tab's or another note session's draft.
  - Restoring a draft saves it with If-Match on the draft's base ETag: if the file changed
    since, the `412` opens the conflict view instead of overwriting the newer version.
  - On open, if drafts from other note sessions (closed tabs, crashed browsers, earlier
    openings in this tab) exist for the file, the recovery dialog lists all of them.
  - **Orphaned drafts.** On vault load and each time a note is opened, the client lists every
    local draft for that vault whose `esid` no live tab holds, or whose note session in this
    tab has closed, and that wasn't opened yet: drafts of closed tabs and notes, of files
    tombstoned or moved since (`410`), and of files the user can no longer open. Each can be
    opened beside the current version, saved as a new file, exported or discarded.
    **PostelMD never deletes a draft without asking**; the only exceptions are in ADR-0018
    (suspension and revocation). The client asks the browser for persistent storage
    (`navigator.storage.persist()`) so eviction under storage pressure is unlikely.
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
- A crash at every step of a save, each reaching its row of the save recovery table (§5),
  under both crash models (process kill and power loss).
- A save whose response was lost, retried with the old `If-Match`: `200`, no new generation.
- A leftover temp file not named by any intent is swept at startup; one named by an open
  intent is not.
- `EXDEV` (a sub-folder on another mount): the save fails and never falls back to a copy.
- An `fsync` error: the operation fails, the vault goes read-only, and nothing retries the
  `fsync`.
- A contract test that the client reads and uses the returned ETag.
- Fuzz tests on the operation sequence.

## Consequences
- Positive: no lost updates between PostelMD clients; honest, detectable behaviour for
  external writers; deterministic and testable.
- Negative: more state (IDs, generations, tombstones, outbox, intents). Every save costs one
  extra durable SQLite commit (the intent); autosave is debounced, so this is about one more
  `fsync` per 1–2 s of typing. Uncoordinated external writers
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
