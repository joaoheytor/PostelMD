# ADR-0032: v0.2 ETag and API: ID + generation + hash, and saves by file ID

- **Status:** Proposed (Accepted with the ETag slice, which also flips ADR-0003)
- **Date:** 2026-11-12

## Context
[ADR-0031](0031-v02-file-identity.md) gives every file an ID and a generation, and
[ADR-0033](0033-v02-intents.md) keeps them right across crashes. This ADR puts them on the
wire, as [ADR-0003](0003-save-protocol.md) §2–§3 designed, and says how clients cross the
change.

Supersedes these parts of Accepted ADRs, and nothing else in them:
- [ADR-0020](0020-mvp-scope.md): "**The ETag is the SHA-256 of the content.**"; the bullet
  "A file deleted and recreated with identical bytes matches an old `If-Match`. That's
  accepted…"; under "API", "Paths, not file IDs (ADR-0003 §3's IDs arrive with SQLite)"; and,
  in the git table, "`rev` is the revision of the file's bytes" (a move now changes `rev`).
- [ADR-0027](0027-landed-retries-and-the-adoption-marker.md): "A save whose bytes equal the
  note's current bytes succeeds whatever its `If-Match`" now also needs a live ID.
- [ADR-0023](0023-a-draft-key-per-note-session.md): the draft key "`<esid>.<n>` + path"
  becomes `<esid>.<n>` + file ID (the path is kept for display).

[ADR-0029](0029-a-save-over-a-file-too-large-to-read.md) is unchanged: a save over a file too large to read answers `412` with **no
ETag** and the code `too-large-to-read`. ADR-0003 is Proposed and is edited to match this ADR.

## Decision

### The ETag
- `"<fileID>.<generation>.<hash>"`, a strong ETag; `<hash>` is the first 32 hex digits
  (128 bits) of the SHA-256 **of the exact bytes in that response**. Clients treat it as
  opaque.
- **Every ETag comes from bytes read now.** A read hashes what it read; if that differs from
  the row's hash, it takes the vault mutex and reads again. Only if the bytes read under the
  mutex still differ from the row (compared again, since another request may have recorded
  the change meanwhile) does it record the outside change (generation + 1, its own
  transaction); it answers with those bytes and the ETag that describes them. No change, no
  bump.
- **A precondition is checked against the file, not the row.** Under the vault mutex, a save
  (and a restore from history, a move or delete with `If-Match`, and `git status`) reads and
  hashes the target's bytes:
  - if the hash differs from the row's, it first records the outside change as above, then
    compares, so an `If-Match` from before the change no longer matches;
  - if the path is missing, the row becomes `suspect` (ADR-0031) and the answer is
    `412 {"code": "file-missing"}` with **no ETag** (ADR-0029's shape); the client keeps its
    draft;
  - `If-Match` matches only when **ID, generation and hash** equal the current ones. A value in
    the MVP's form (64 hex digits, no dots) never matches.
- **Equal bytes** (replaces ADR-0027's bullet): a save of a **live** ID whose body's full
  SHA-256 equals the full SHA-256 of the bytes just read (never the 32-digit prefix) answers
  `204` with the current ETag and writes nothing, whatever its `If-Match`. A tombstoned ID
  answers `410` even with equal bytes: that is the ABA case.
- **`git status`'s `rev`** is the file's current ETag value, unquoted, computed by the rule
  above; the commit compares each `rev` with the current ETag under the vault mutex, as
  before. The web app's `commit-scan.ts` doesn't change. A move between the list and the
  commit gives `changes-differ`, and the dialog lists again.

### Answers
| Code | When |
|---|---|
| `200` + `ETag` | a read |
| `204` + `ETag` | a save that wrote, or equal bytes |
| `201` + `ETag` + `PostelMD-File-ID` | a create (a new ID) |
| `412` + current note + `ETag` | a precondition that doesn't match |
| `412 {"code": "file-missing"}`, no `ETag` | the path is missing or `suspect` |
| `412 {"code": "too-large-to-read"}`, no `ETag` | ADR-0029 |
| `410` | a tombstoned ID |
| `404 {"code": "unknown-file"}` | an ID never seen, or purged |
| `428` | a save without `If-Match` |

`503 Retry-After` for the held mutex, `413` and the session rules are unchanged.

### Routes in v0.2.0
| Request | Result |
|---|---|
| `GET /api/v1/notes/{path}` | open by path: bytes, `ETag`, and the ID in `PostelMD-File-ID` (a pending path is resolved first, ADR-0031) |
| `GET /api/v1/files/{id}` | bytes and `ETag` |
| `PUT /api/v1/files/{id}` with `If-Match` | save. Never creates |
| `PUT /api/v1/notes/{path}` with `If-None-Match: *` | create; `409` if the path exists |
| `PUT /api/v1/notes/{path}` with `If-Match` | **a permanent path alias**: resolves the path's live ID, then as `PUT /files/{id}`; an ETag naming another ID is `412`. As ABA-safe as the ID route, and useful for scripts |
| `DELETE /api/v1/notes/{path}`, `/folders/{path}`, `POST /api/v1/move` | keep their v0.1 routes and bodies, and accept an **optional `If-Match`** on the note (a mismatch is `412`); the web app sends it. ID routes for them come in v0.3 |

Trash, history and restore keep their v0.1 routes; their effect on IDs is in ADR-0033.

### Crossing the change
This is a breaking API change: the release is **v0.2.0** (`feat!`, CHANGELOG "Changed"). The
web app is embedded, so a reload always brings a matching client.
- A tab opened under v0.1 saves through the path alias with a 64-hex ETag: `204` if the bytes
  are already on disk, else `412` and the conflict view with its draft. The ETag in that
  answer is the new form, so the tab goes on saving after the user resolves it.
- v0.1 drafts (key `esid` + path) are listed as orphaned drafts (ADR-0003 §7) and never
  deleted unasked; restoring one sends its 64-hex base ETag, so the conflict view decides.
- There is no period in which the server accepts both forms as a match: the old form has no
  ID and would bring back the ABA case.

### Client (the ETag slice)
- `docStore` keeps the file ID from the open, and saves go to `/files/{id}`, so a note moved
  by another tab or outside keeps saving.
- **Rebase on a bump without a change.** A move, a folder rename or an outside `mv` bumps the
  generation of unchanged files. When a `412`'s body equals the snapshot's base content, the
  client takes the new ETag and resends the same snapshot at once, without the conflict view.
- `410` and `unknown-file` show "this note was moved or deleted; your edit is kept as a
  draft", with "save as a new note". `file-missing` shows "this note is missing on disk" and
  keeps the draft; the next save retries. Neither is retried automatically.
- Drafts are keyed `<esid>.<n>` + file ID, with the path for display (ADR-0023, superseded
  part).

## Consequences
- Positive: ABA closes; an open tab follows its note across moves; every ETag describes bytes
  the server has just read, so an outside edit can't hide behind a stale row.
- Negative / costs:
  - a breaking change for any client but the web app; v0.1 drafts need one conflict view;
  - reads and saves hash the file under some conditions twice (a read, then a re-read under
    the mutex) when it changed outside;
  - losing `app.db` (ADR-0030) ends every ID: tabs see `unknown-file`, drafts survive.
- Follow-up: **the ETag slice** (after ADR-0033's intents; flips this ADR and ADR-0003): the
  ETag, the routes and answers above, `git status` revisions, and the client. It comes after
  the intents slice because once a generation is on the wire, a save has two side effects.

## Alternatives considered
| Option | Why not |
|---|---|
| Keep the content-hash ETag, add tombstones by path | A delete-and-recreate at one path still matches; a moved note can't be followed |
| Accept both ETag forms for one release | The old form has no ID: the ABA case stays open that long |
| Compare `If-Match` with the stored row only | An outside edit since the last scan would be overwritten: a lost update |
| Every route by ID now (ADR-0003 §3) | More churn in one slice; saves are what autosave races |
| Remove the path alias in 0.3.0 | It is as safe as the ID route and scripts use paths |
| Show the conflict view on every bump | Renaming a folder would ask about every open note under it, with identical texts |

## Decisions on the review's open questions (maintainer may revisit)
1. **Move and delete stay on path routes** in v0.2, with an optional `If-Match` that the web
   app sends; ID routes come in v0.3.
2. **`PUT /notes/{path}` with `If-Match` is a permanent path alias**, not deprecated.
3. **A folder rename keeps the generation bump**; the client rebases when the `412`'s bytes
   equal its base, so no conflict view appears.
