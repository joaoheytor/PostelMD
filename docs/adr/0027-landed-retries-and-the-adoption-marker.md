# ADR-0027: Landed retries and the adoption marker

- **Status:** Accepted; its save-mismatch statement, for a file over the read limit, is superseded by [ADR-0029](0029-a-save-over-a-file-too-large-to-read.md); the words "an empty regular file" about the marker, by [ADR-0031](0031-v02-file-identity.md) (the marker carries the vault's identity; ownership still depends only on its existence)
- **Date:** 2026-11-11

## Context
Supersedes three statements of [ADR-0020](0020-mvp-scope.md)'s "Saving" section. The rest
of ADR-0020 stands, including the Installation sentence this ADR implements ("Adoption
refuses a folder that contains `.postelmd-tmp` or `.postelmd-trash` entries it didn't
create"):
1. "A mismatch returns `412` with the current content for the conflict view";
2. "So two saves with the same `If-Match` can't both pass: the second sees the new hash and
   gets `412`";
3. "At startup **every** entry in `.postelmd-tmp/` is removed".

**Landed retries.** When a save's rename lands but its response is lost (a dropped
connection, a timeout, or a directory `fsync` failing after the rename), the client retries
with its old `If-Match`. Under statements 1 and 2 the retry got `412`, and the conflict
view showed two identical texts. [ADR-0003](0003-save-protocol.md) §3 and §5 already
answer `200` when the current content equals the submitted content; the MVP didn't.

**Adoption.** Statement 3 deleted whatever a user's own `.postelmd-tmp` held at the first
start, and nothing implemented the Installation sentence. To refuse entries "it didn't
create", PostelMD needs a record that it created the folders. The vault is a separate mount
that can be swapped under the same `/data/.postelmd`, so the record lives in the vault.

## Decision
**Saves** (replaces statements 1 and 2):
- A save whose bytes equal the note's current bytes succeeds whatever its `If-Match`: `204`
  with the current ETag, and nothing is written. So a retry of a save that landed succeeds,
  and so does a second save with the same `If-Match` and the same bytes.
- Otherwise a mismatch returns `412` with the current content for the conflict view. Two
  saves with the same `If-Match` and **different** bytes can't both pass: the second sees the
  new hash and gets `412`.

**The marker** (replaces statement 3):
- PostelMD owns a vault's reserved folders once `.postelmd-tmp/.created-by-postelmd`, an
  empty regular file, exists. At startup, before anything touches the reserved folders, the
  vault:
  - with the marker: removes **every other** entry in `.postelmd-tmp/` (a save that didn't
    finish never returned `200`, so the client still has the edit as a draft) and keeps the
    marker;
  - without it: requires `.postelmd-tmp` and `.postelmd-trash` to be missing or empty. If
    either holds anything, startup stops and changes nothing. The error names the folder and
    leads with the fix for the usual case, PostelMD's own folders without the marker: run
    `mkdir -p .postelmd-tmp && touch .postelmd-tmp/.created-by-postelmd` in the notes
    folder. For a folder that really is the user's, the fix is to rename it. The error warns
    that renaming PostelMD's trash would make the deleted notes in it visible and
    committable.
  - when both are missing or empty: creates `.postelmd-tmp` (`mkdir`, `fsync` of the vault
    root), creates the marker, and `fsync`s `.postelmd-tmp`, all before the sweep, the rename
    probe and serving. Only the create carries meaning, so there's no intent:

    | Crash after | On disk | Next start |
    |---|---|---|
    | The `mkdir`, before the root's `fsync` | An empty `.postelmd-tmp`, or none | Adopts again |
    | The marker's create, before the folder's `fsync` | The marker, or (power loss) an empty `.postelmd-tmp` | Marked, or adopts again |
    | The folder's `fsync` | The marker | Marked |

    The marker is durable before startup returns, so no later delete can fill the trash of
    an unmarked vault. `internal/vault`'s crash test runs every step of the first start and
    a delete, under the process-kill and power-loss models.

**Limits of the marker.** It's a file in the vault, so other tools can remove it:
- deleting `.postelmd-tmp` by hand, or `git clean -X` on the host (it removes ignored files,
  and `.postelmd-tmp/` is in `.git/info/exclude`), removes it. With deleted notes in the
  trash, the next start is refused, and the error's first fix is the one to apply;
- if appending to `.git/info/exclude` failed (it's best effort), a host `git add -A` can
  commit the marker, and a clone of that repository carries it. A clone also gets any
  committed trash, so the check is skipped for it. Both need the user to commit PostelMD's
  folders on purpose; the server's warning about `info/exclude` is the mitigation.

`docs/operations/running-v0.1.md` covers both.

**Note for v0.2.** With several users, the landed-retry rule makes `204` against `412` a
yes/no answer to "is the note exactly these bytes?". A save must then require read access to
the note as well as write access (#119).

## Consequences
- Positive: a lost save response no longer ends in a conflict view of two identical texts.
  Adopting a folder never deletes the user's files, and the refusal says what to run.
- Negative / costs: vaults used with a build from before the marker that hold deleted notes
  (or a temp leftover) are refused once, and the user runs the printed command. The marker
  is one more file in the temp folder that tests and tools must skip.
- Follow-ups: #119 (look-alike `.git` names, and read access for saves in v0.2).

## Alternatives considered
| Option | Why not |
|---|---|
| Keep `412` for identical bytes | The client can't tell a landed retry from a real conflict; the user resolves a conflict between two equal texts |
| Recognise PostelMD's entries by their names (32-hex temp files, stamp folders in the trash) | A user file can match a pattern; the sweep would then delete it |
| Treat a first start as "no `/data/.postelmd/auth.json` yet" | The vault mount can be swapped under existing state; a crash during the rename probe before setup would refuse PostelMD's own leftover |
| The marker in `/data/.postelmd` | Describes the state directory, not the vault mounted beside it |
