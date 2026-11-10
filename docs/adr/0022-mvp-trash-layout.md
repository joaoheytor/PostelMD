# ADR-0022: The MVP trash keeps each item's name in a folder of its own

- **Status:** Accepted
- **Date:** 2026-11-10

## Context
Supersedes the **trash** paragraph of [ADR-0020](0020-mvp-scope.md) ("Deletes move the file
or folder into the vault's `.postelmd-trash/<timestamp>-<name>` with one
`RENAME_NOREPLACE`"), and with it, for deletes only, that paragraph's "single-rename
operations that need no intents". The rest of ADR-0020 stands.

A file or folder name can be up to 255 bytes (Linux `NAME_MAX`). The timestamp prefix pushed
a name near that limit over it, so deleting it failed with `ENAMETOOLONG` and a `500`.
Shortening the name to make room would lose it, and in the MVP the trash entry's name is the
only record of what was deleted: there is no database (ADR-0020) and no trash record until
v0.2 (ADR-0003 §2). The trash restore (next in v0.1) has to show and restore that name.

## Decision
- A delete moves the item to **`.postelmd-trash/<stamp>/<name>`**: `<stamp>` is the UTC time
  of the delete (`20060102T150405.000000000Z`), followed by `-1`, `-2`… when that folder
  exists; `<name>` is the item's own, unchanged name. This is ADR-0003 §2's layout
  (`<op-id>/<basename>`), with the stamp as the op ID. Any name that fits on disk fits in the
  trash.
- The delete has two steps, under the vault mutex, after the trash folder itself is durable:
  1. **`mkdir`** of `.postelmd-trash/<stamp>`, then `fsync` of the trash. The `mkdir` fails
     if the folder exists, and the next suffix is tried: an existing folder, whether from
     another delete in the same instant or left by a crash, is **never reused**.
  2. **One `RENAME_NOREPLACE`** of the item into it, then `fsync` of the stamp folder before
     the item's old folder (destination first, as for moves).
  If the rename fails, the delete removes the folder it made with a remove that only takes
  an empty directory, and returns the rename's error.
- The first side effect holds no data, so no intent is needed (CLAUDE.md rule 14): the empty
  stamp folder is all a crash can leave besides the two states the rename already had.

  | Crash point | On disk | Recovery |
  |---|---|---|
  | Before or during the `mkdir` | Item in place; no stamp folder, or an empty one | None. The delete didn't happen; the user deletes again |
  | After the `mkdir`, before or during the rename | Item in place; an empty stamp folder | None. The folder holds no data; the trash is reserved, so no listing, index or commit shows it; the next delete in the same instant takes the next suffix. It can be removed by hand |
  | After the rename, before the syncs | Kill: item in the stamp folder. Power loss: in the stamp folder; in place, with an empty stamp folder (the rename didn't reach the disk); or in both places (as for a move, never in neither) | None for a kill: the delete is done. After a power loss: an item back in place means the delete didn't happen, and the user deletes again; a duplicate is cleaned up by the user |
  | After the syncs | Item in the stamp folder | Done |

  The crash tests run every step under the process-kill and power-loss models.
- A **new name** (create, new folder, or a move or rename destination) with a part over 255
  bytes is refused with `400` and a plain message, not an internal error. A read, save or
  delete of such a path answers `404`: nothing can exist there.
- Entries made in the old layout (`.postelmd-trash/<stamp>-<name>`) are left as they are and
  stay restorable: the restore lists both forms.

## Consequences
- Positive:
  - Deleting works for every name the filesystem allows, and the trash keeps the full name.
  - The layout is the one v0.2's trash record (ADR-0003 §2) expects, so the record only adds
    to it.
- Negative / costs:
  - A delete is two steps instead of one, and a crash can leave an empty folder in the trash.
  - Two layouts exist in a vault deleted from before and after this change.
  - The trash still doesn't record the folder an item was in: the restore asks for it
    (default: the vault root).
- Follow-ups: recording the original path, and purging, arrive with the trash record in v0.2
  (`roadmap.md`).

## Alternatives considered
| Option | Why not |
|---|---|
| Truncate the name so `<stamp>-<name>` fits | Loses the end of the name, and the name is the only record of what was deleted. |
| Keep `<stamp>-<name>` and write a sidecar file with the full name | A second side effect holding data, so a real intent and recovery table for a file the v0.2 record replaces anyway. |
| Nest the original folders (`<stamp>/<old/folder>/<name>`) | Records the folder too, but adds a `mkdir` per level for v0.1; deferred to v0.2 with the purge and the trash record. |
