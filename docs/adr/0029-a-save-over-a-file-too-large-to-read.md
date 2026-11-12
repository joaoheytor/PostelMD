# ADR-0029: A save over a file too large to read

- **Status:** Accepted
- **Date:** 2026-11-12

## Context
Supersedes, for a file over the store's read limit only, the save-mismatch statement of
[ADR-0027](0027-landed-retries-and-the-adoption-marker.md) ("Otherwise a mismatch returns
`412` with the current content for the conflict view"), and the matching rows of
[ADR-0020](0020-mvp-scope.md)'s API table: "save: `204`, or `412` with the current note and
its `ETag`" and, for restore, "`412` with the current note". The rest of both stands.

A save checks `If-Match` by reading the current file under the vault mutex. The store reads at
most 64 MiB (`internal/store/fs`, #144). When something outside PostelMD grows an open file
past that, the next autosave failed with a `500`, and the client retried it forever
([#157](https://github.com/joaoheytor/PostelMD/issues/157)). Reads of such a file already
answer `413` (#156), but on a save `413` means the request body is over the 5 MiB note
limit: it would tell the user their own note is too large, which it isn't.

The precondition's answer is known without reading the file. A client only gets a revision
by reading a file (at most 64 MiB) or saving one (at most 5 MiB), so no client's `If-Match`
names a file over 64 MiB, and no save body equals it either (ADR-0027's landed-retry rule
can't apply). What can't be sent is the current note and its `ETag`.

## Decision
- A save (`PUT` with `If-Match`) or a restore from history over a file larger than the
  store's read limit returns `412`, with **no `ETag`** and a JSON body
  `{"code": "too-large-to-read", "error": "…"}` instead of the current note. Nothing is
  written; the file stays as it is. It isn't a server error, so it isn't logged as one.
- Every other `412` stays as ADR-0027 says: the current note and its `ETag`.
- The web app treats a `412` without an `ETag` like a conflict whose other version isn't
  text: it isn't retried, the edits stay in the editor and its draft, nothing can be taken or
  compared, and the one way out is "Save my version as a new file".
- Create, move and delete don't read the file, so they answer over such a file as over any
  other (`409` for a create; move and delete succeed).

## Consequences
- Positive: no retried `500` and no misleading "too large to save"; the user keeps their
  text through the same path as a file that turned binary. No new status code, and the client
  change is one condition.
- Negative / costs: an API client that reads every `412` body as the note must check for the
  `ETag` (or the JSON `code`) first. "Keep my version" isn't offered: overwriting needs an
  `ETag`, and hashing a file of any size under the vault mutex is the cost the read limit
  exists to avoid. The user can remove or shrink the file on the host and reopen it.
- Follow-ups: none.

## Alternatives considered
| Option | Why not |
|---|---|
| `413` with a distinct `code` | On a `PUT`, `413` says the request body is too large: clients (ours until now) report "the note is too large to save" |
| `409` with a `code` | `409` on this route means a create found the file there; a save conflict is what `412` already means, and the client would need a new path for it |
| `412` with an empty body and no marker | The web app and API clients would read the empty body as the other version, and "Use the other version" would empty the note |
| Hash the large file to send its `ETag` | Unbounded work under the vault mutex, and the body still can't be sent |
