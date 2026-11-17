# ADR-0036: The startup scan: one transaction per pass, re-checked under the vault mutex

- **Status:** Accepted (with the IDs slice's third part, the first code that depends on it); its sentences on a read's full pass for a due hold and on a read's failed pass are superseded by [ADR-0037](0037-the-read-only-state.md) (no new full pass within a minute of a failed one; a read-only vault isn't logged per read)
- **Date:** 2026-11-16

## Context
[ADR-0031](0031-v02-file-identity.md) decides what the startup scan does: stat every file,
hash what changed, mark missing paths `suspect`, tombstone or move an ID only on a pass at
least 10 s later, match outside renames by rule 1 (`mv`) and rule 2 (copy and delete), hold a
rule-2 path `pending`, and let a read resolve a path the scan hasn't seen. It says the scan
"applies its results under the vault mutex, re-checking each path first", and leaves to the
code how: how many transactions a pass takes, where the 10 s are measured from across a
restart, what a held path that changes means, which tombstone reason an unmatched rename
gets, and what `serve` does before data format 2. CLAUDE.md rule 14 asks for the recovery
table of an operation before its code. This ADR records those choices; it changes nothing in
ADR-0031's Decision.

## Decision

### A pass
- **Plan, then apply.** A pass reads the rows and holds, walks the vault (stat only), and
  hashes what it must, all without the vault mutex. Each decision (a row unchanged or bumped,
  a suspect, a tombstone, a moved ID, a hold, a new ID) names the paths it rests on: present
  with the stat the plan saw, or absent.
- **Under the vault mutex**, the pass stats each of those paths again and drops every
  decision whose paths changed; then it commits the rest in **one write transaction**, each
  decision in its own savepoint guarded by the row's ID, path and generation (a suspect also
  by its state, or a clock in the future; a new ID or a hold by the path having no row and no
  hold, a hold also by its row being suspect and holding nothing yet). A decision whose
  row changed since the plan (a read's pass ran in between) is rolled back alone. The vault
  mutex comes first, then the transaction, and no filesystem I/O happens inside it
  (ADR-0030). A dropped decision is left for the next pass.
- **A walk that fails changes nothing**: an unreadable folder is never taken for missing
  files. A file that can't be hashed (a writer busy for three tries, or a read error, logged
  without its path) is left for the next pass; it is on disk, so it isn't missing.
- **No intent.** A pass writes nothing in the vault and commits once: its one side effect is
  one transaction, so rule 14's intent isn't needed.

### Time
- `suspect_since` is the pass's time, written with the suspect. A later pass resolves the
  suspect when its own time is at least 10 s after that, **whichever pass it is**: the 10 s
  pass after the start, or the first pass after a restart. The 10 s are never counted in
  memory.
- The 10 s are measured on the system clock, so its steps count. A `suspect_since` in the
  future (the clock stepped back) is treated as now: the pass writes now in its place, so no
  wait, and no `Retry-After`, is ever longer than 10 s. A clock that jumps forward shortens
  the 10 s, down to the next pass; a move or tombstone that comes early is the same as one
  after a real 10 s wait.
- The second pass is a full pass, run once 10 s after the first finishes (ADR-0031: "until
  the watcher slice"). A path that first goes missing during the second pass stays suspect
  until the next start, or until a read resolves a match for it.

### The rules, made exact
A pass decides in this order: the rows whose paths are there; rule 1 over every new and held
path; the holds that still stand; rule 2 over the other new paths; then the rows still
missing.
- **Rule 1 comes first, and it includes the rows that hold a path.** When a suspect's own
  inode (same hash, device, inode and mtime) comes back at a new path, the ID moves there, at
  once, inside the 10 s or after them, and the suspect's hold is released: the held path gets
  a new ID. A held path can itself be rule 1's match (the original renamed onto its copy). A
  held path that is being written can't be hashed: its hold waits for the next pass, but
  rule 1 can still move its row.
- **A rule-2 hold is provisional.** It records a match; it doesn't decide it. Rule 1 can take
  its row (above), and before a hold becomes a move the pass checks rule 2's uniqueness
  again: the row must still be the only missing row with the hash and the held path the only
  new or held path with it. If not, the hold is released, every path with the hash gets a new
  ID, and the row is tombstoned as `ambiguous-rename` once it has been missing 10 s.
- Rule 2 counts, for a hash, the new paths and the held paths, and the missing rows (live or
  suspect) whose IDs haven't moved in this pass.
- A held path that went away releases its hold; one whose bytes changed releases it and is
  taken as a new path. When the old path is back, the hold is released and the held path
  gets a new ID in the same pass.
- A tombstone's reason is `ambiguous-rename` when another path with the row's bytes got an ID
  at or after the row's first miss (an ID's ULID time is when PostelMD first saw its path),
  else `missing`.
- "No intent names it" (ADR-0031) has nothing to check until ADR-0033's intents; that slice
  adds the check to the plan.

### A read
`GET /api/v1/notes/{path}` runs a single-path pass before reading, only for a path with no
row: a held path resolves once its suspect has been missing 10 s, else the read answers
`503` with `Retry-After` (whole seconds, rounded up, at most 10); a new path is hashed, and
gets the rules against every row with its hash or its device and inode, **live or suspect**,
plus the rows that hold a path. Each of those rows whose path is gone counts as missing, so a
rename made outside PostelMD while it runs (`mv a.md b.md`, then a read of `b.md`) moves the
ID by rule 1, and rule 2's "only missing row" counts a live row whose file was just removed.
Rule 2 may hold the path, and answers the same `503`. A read sees only its own path and the
stored holds, not a second copy elsewhere on disk, so it never makes a rule-2 move itself:
when the hold is due (or a match would move at once), the read runs a **full pass** instead,
which checks rule 2's uniqueness against every path, and then reads. This happens only after
the 10 s wait, and concurrent reads share one full pass. The existing indexes `files_by_sha256`
and `files_by_inode` serve the lookup, so no migration is needed. A path with a row, a path
that isn't a regular file, and a file over the read size limit (`fs.MaxFileSize`) are left to
the scan. Concurrent reads of one path share one pass. If the pass fails, the failure is
logged without the path and the read goes on: no ID is on the wire yet. The commit dialog's
change list (`git status`) does the same in
[#181](https://github.com/joaoheytor/PostelMD/issues/181).

### serve before data format 2
`serve` runs the scan (ADR-0030's step 7, before `/readyz` is ready) only when the database
has the "assigned" flag **and** the vault marker names the database's vault UUID. Anything
else is left as it is, with a warning when the flag is set: at format 1 nothing assigns IDs,
and the other combinations are ADR-0030's format table, which comes with format 2. A failed
pass is logged and serving goes on. Writer I/O errors from the pass's transaction
(`SQLITE_CORRUPT`, `SQLITE_IOERR`, `SQLITE_FULL`) are not an exception to that: once part 4
adds ADR-0030's read-only state, they follow its read-only rule like every other write.
`stop` cancels the background passes and the expiry, and
waits for them before the database closes.

### Tombstone expiry
At start and every hour, one transaction removes tombstones older than 30 days, without the
vault mutex (ADR-0030's background writers).

### Recovery table
A pass is one transaction (`synchronous=FULL`), so a crash leaves all of it or none:

| Crash at | In `app.db` | Next start |
|---|---|---|
| After the plan, before the vault mutex (`scan-planned`) | Nothing of the pass | The first pass plans again; its suspects' clock starts then |
| Inside the transaction (`scan-before-commit`) | Nothing of the pass | The same |
| After the commit (`scan-after-commit`), or after the first pass, before the second (`scan-first-pass-done`) | The whole pass: suspects with their time, holds, moved and new IDs, bumps, tombstones | Under 10 s since a suspect's first miss: it waits (a read of its held path gets `503`); from 10 s, the first pass resolves it |

`internal/ids`' `TestScanCrashPoints` and `internal/store/sql`'s `TestApplyScanCrashPoints`
cover every row under the process-kill model; the power-loss model is
[#182](https://github.com/joaoheytor/PostelMD/issues/182), with the other `app.db` crash
tests.

## Consequences
- Positive: a pass never acts on a path that changed while it hashed; a restart doesn't
  restart the 10 s; one fsync'd commit per pass, and none for an unchanged vault.
- Negative / costs:
  - the first pass hashes every file the first start after the upgrade (the assignment hashed
    them too); later starts hash only what changed;
  - a decision dropped by the re-check waits for the next start, a read, or the watcher;
  - a path that goes missing during the second pass stays suspect until the next start;
  - the web app shows a held path's `503` as "the server is busy" until it retries.
- Follow-ups: [#181](https://github.com/joaoheytor/PostelMD/issues/181) (the commit dialog's
  change list), [#182](https://github.com/joaoheytor/PostelMD/issues/182) (the power-loss
  model); ADR-0033's slice adds the intents check; part 4 (format 2) runs the format table
  before the scan.

## Alternatives considered
| Option | Why not |
|---|---|
| One transaction per decision | A fsync per changed file; a crash mid-pass leaves half a pass, which still recovers but makes the table longer |
| Hash under the vault mutex | A large file would block every save for as long as it takes to read |
| Count the 10 s from the process's own first pass | A restart within the 10 s would wait another 10 s, and a crash loop would never tombstone |
| A second pass for the suspects and holds only | The same rows and a smaller walk, but two planners to keep right; a full pass is the same code |
| Fail startup when the first pass fails | No ID is on the wire yet; a scan that can't read one folder would stop the server |

## Clarifications
- **2026-11-17, at data format 2.** What "serve before data format 2" becomes at format 2
  (the format table refuses where this ADR skips the scan with a warning; a failed first
  pass still doesn't stop the start) is in [ADR-0040](0040-db-rebuild-and-vault-claim.md).
