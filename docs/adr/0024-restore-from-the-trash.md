# ADR-0024: Restore from the trash

- **Status:** Accepted
- **Date:** 2026-11-10

## Context
A delete moves the item into the vault's trash ([ADR-0022](0022-mvp-trash-layout.md)):
`.postelmd-trash/<stamp>[-n]/<name>`, or `.postelmd-trash/<stamp>-<name>` for deletes made
before that layout. Until now the only way back was a manual `mv -n` on the host
(`docs/operations/running-v0.1.md`). v0.1 lists the trash and restores from it in the web
app. Emptying and purging the trash stay in v0.2, with the trash record of ADR-0003 §2.

The trash is a reserved path (ADR-0006): nothing under it is reachable through the notes API.
Restore is the first operation that reads from it on a client's request, so how the client
names a trashed item is a confinement question. MVP scope: one user, no database
([ADR-0020](0020-mvp-scope.md)), so the trash entry's name is the only record of what was
deleted and when.

## Decision
- **Naming an item.** An item is named by its **trash entry**: the name of a direct child of
  `.postelmd-trash`, never a path. The server compares it with the names of the trash's
  children (never opening it as a path), and accepts only one in one of the two layouts
  (`<stamp>` or `<stamp>-<n>`, a stamp folder; `<stamp>-<name>`, an old entry) that passes
  `IsReserved`, has no `/` and is at most 255 bytes. Anything else is `410 Gone` ("no longer
  in the trash"), so the app tells it from a missing destination folder (`404`) by the status
  alone. A trash folder replaced by a symlink is never followed: it lists nothing and
  restores nothing.
- **Listing** (`GET /api/v1/trash`): the newest items first (stamps are fixed-width UTC, so
  name order is time order), at most 1,000, with `truncated` when there are more. Each item
  has its entry (`id`), its name before the delete, its kind (file or folder), the time of
  the delete (from the stamp) and, for a file, its size. A stamp folder with one entry is that
  entry; an empty one (a crash's leftover, ADR-0022, or this ADR's) is skipped; one with more
  is read as an old entry. Symlinks, special files and entries no delete makes are skipped
  and can't be restored. An old entry's name is everything after `<stamp>-`, so an old
  same-instant entry `<stamp>-1-<name>` shows as `1-<name>`; the user can rename it on
  restore. The listing takes no lock: every change to the trash is a rename or an empty
  folder. It reads the whole trash folder, and skipped entries don't count towards the cap:
  acceptable for one user's trash in the MVP; v0.2's trash record bounds the read.
- **Restore** (`POST /api/v1/trash/{id}/restore`, `{folder, name}`): into `folder` (default:
  the vault root; the trash doesn't record where the item was) under `name` (default: its own).
  The destination gets a move's checks: `IsReserved` (so never into the trash or `.git`;
  `404`), a part over 255 bytes is `400`, a name with a `/` is `400` ("a name can't contain
  /"), a missing folder is `404`. Under the vault mutex:
  1. **One `RENAME_NOREPLACE`** of the item out of the trash to the destination, then `fsync`
     of the destination's folder before the trash folder it left (destination first, as for a
     move). An existing destination is `409` with a plain message, and nothing changes: the
     web app asks for another name. If a sync fails after the rename, the restore answers
     `500` although the item has moved; the web app reads the list again after any failure,
     so the item no longer shows as trashed.
  2. In the new layout, **removal of the emptied stamp folder** with a remove that only takes
     an empty directory, then `fsync` of the trash. A failure here doesn't undo the restore:
     it is logged (the folder name is a stamp, not user content) and the restore answers
     `200`.
- **No intent** (CLAUDE.md rule 14): the rename is the only step that moves data; the second
  removes an empty folder, and the state between them, an empty stamp folder, is one the
  listing already skips. A crash can't leave a state this table doesn't name:

  | Crash point | On disk | Recovery |
  |---|---|---|
  | Before or during the rename | Item in the trash | None. The restore didn't happen; it is still listed, and the user restores again |
  | After the rename, before the syncs | Kill: item at the destination, an empty stamp folder. Power loss: restored; still in the trash (the rename didn't reach the disk); or in both places, never in neither | None for a kill. After a power loss: an item still listed is restored again; a duplicate (restored and still listed) is the user's to delete |
  | An `fsync` error after the rename (no crash) | Item at the destination; an empty stamp folder in the new layout; the answer is `500` | None. The app reloads the list, where the item no longer shows; the tree shows it on the next listing of its folder. A later power loss can still undo the rename, as in the row above |
  | After the syncs, before or during the folder removal | Item at the destination; an empty stamp folder | None. The listing skips it, and nothing reuses it (a delete never reuses an existing stamp folder). It can be removed by hand, and the v0.2 purge removes it |
  | After the removal, before the trash's sync | Kill: done. Power loss: the empty stamp folder may come back | As the row above |
  | After the syncs | Item at the destination | Done |

  `internal/vault/restore_crash_linux_test.go` runs every step under the process-kill and
  power-loss models, in both layouts, then restarts and checks the listing and a retry.
- **Logging and authorization.** The routes are behind the session like the rest of the API
  (ADR-0020). The write log has the route pattern only, never the entry; the restore's
  unexpected errors are logged only as values that can't carry user text (the store
  operation, the error number and a fixed kind), never the paths they quote (CLAUDE.md
  rule 15).
- **Git.** A restore never commits, and git sees the result like any other change to the
  working tree: an item restored to its committed path shows no change; restored elsewhere,
  the commit dialog shows its old path deleted and its new one added; a file that was never
  committed shows as added.

## Consequences
- Positive:
  - Deleted notes and folders come back from the web app, without a shell on the host.
  - The client never sends a path into the trash, so the trash's reservation (ADR-0006)
    holds: only items a delete left there can come out, and only into allowed paths.
  - A restore never overwrites, like a move.
- Negative / costs:
  - The user chooses where an item goes: its old folder isn't recorded until v0.2.
  - A crash, or a failed folder removal, leaves an empty folder in the trash; it holds no
    data and isn't listed, but stays until it is removed by hand or purged.
  - Old-layout same-instant entries show their suffix in the name.
  - An old-layout folder whose name is a number (`<stamp>-7`, from a folder named `7`) reads
    like a stamp folder: holding one entry, it lists and restores only that entry, and the
    folder's own name is lost (its emptied folder is removed); if that entry is a symlink,
    the item is hidden; holding two or more, it restores as a folder named `7`. Only deletes
    made before ADR-0022 can produce it; the files themselves are never lost, and the manual
    steps in the runbook still work.
  - The listing reads the whole trash folder each time (see Listing).
- Follow-ups: emptying and purging the trash, and recording the original path, arrive with
  the trash record in v0.2 (`roadmap.md`).

## Alternatives considered
| Option | Why not |
|---|---|
| The client sends the item's path in the trash (`<stamp>/<name>`) | A path into a reserved directory from the client: every `..`, symlink and nesting case needs its own check. An entry name, looked up among the trash's children, has none of them. |
| Leave the emptied stamp folder in place (one step, no cleanup) | Simpler, but every restore would leave an empty folder for the purge; the listing would skip them anyway, so removing it costs one empty-only remove and saves the clutter. |
| An intent file before the rename | The only data-bearing step is one atomic rename, and the leftover is an empty folder the listing skips: an intent would add a recovery path with nothing to recover. |
| Restore over an existing file after asking | Breaks the rule that moves never overwrite (CLAUDE.md rule 4); the user picks another name instead. |
