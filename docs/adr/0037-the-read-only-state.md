# ADR-0037: The read-only state after a database write fault, and a full pass that keeps failing

- **Status:** Accepted (with the first PR of the IDs slice's fourth part, the first code that depends on it)
- **Date:** 2026-11-17

## Context
[ADR-0030](0030-v02-storage-sqlite.md) ("I/O errors") decides that `SQLITE_CORRUPT`,
`SQLITE_IOERR` or `SQLITE_FULL` on the writer "fails the operation, puts every vault
read-only (ADR-0003 §6), and logs an error with `pathless` (…); `/readyz` stays ready and its
body says `read-only`: a state word only (…). Nothing retries the commit. The next start runs
the checks below." [ADR-0036](0036-the-startup-scan.md) ("serve before data format 2") says
the scan's writer faults follow that rule once part 4 of the IDs slice adds the read-only
state. Neither says how a change is answered while the vault is read-only, whether the
database keeps being written, or what ends the state. The v0.1 MVP has no read-only mode
(threat model D4); this is the first one. Data format 2 turns the startup scan on, which
makes the scan's transaction the first write at run time, so the state comes before it.

Supersedes, in ADR-0036's "A read" section, the sentence: "when the hold is due (or a match
would move at once), the read runs a **full pass** instead, which checks rule 2's uniqueness
against every path, and then reads." While that full pass keeps failing (a folder the walk
can't read), every read of the held path walks the whole vault again
([#184](https://github.com/joaoheytor/PostelMD/issues/184)).

## Decision

### Writer faults
- `store/sql` checks every write transaction's error. `SQLITE_CORRUPT`, `SQLITE_IOERR` or
  `SQLITE_FULL` (any of their extended codes) is a **writer fault**: the operation fails with
  `ErrWriterFault`, and from then on **every write transaction is refused** with
  `ErrWriterFault` without starting one, until the process ends. A damaged or full database
  isn't written again by a background job; the next start's checks (`quick_check`,
  ADR-0030) decide what happens to it. A canceled context, a busy writer or a stale scan unit
  is not a fault.
- The first fault runs a hook, once, that `serve` sets right after opening the vault and
  before the startup scan: it puts the vault **read-only** and logs one error record (event
  `read_only`, no path or error text). The failed operation logs its own error with
  `pathless`, as before.

### The read-only vault
- Every change is refused with `vault.ErrReadOnly`: save (also one with the note's current
  bytes, which writes nothing), create, folder create, move, delete, restore from history
  and from the trash, commit (and the initial import at setup), removing git locks, and the
  startup scan's commit (`Locked`). A change already holding the vault mutex finishes; one
  waiting for it is refused once it gets it. Reads, the change list and history work.
- The HTTP answer is **`503`** with the message "the server is read-only after a storage
  error; …" and **no `Retry-After`**: the state lasts until a restart, so there is no time to
  give. The web app treats `503` as "busy", keeps the draft and retries, which is harmless.
  ADR-0003 §6's `507` stays the answer to a file write that hits `ENOSPC` (not built yet).
- `/readyz` answers `200` with the body `read-only` (`text/plain`), else `200` with an empty
  body: reads still work, so an orchestrator must neither restart the server in a loop nor
  take it out of service.
- Only a restart ends the state. No intent is needed: the state is in memory, and a crash
  is the same as the restart the operator would do.
- A read's identity pass (ADR-0036) that meets the read-only vault isn't logged per read.

### A full pass that keeps failing (#184)
Restating ADR-0036's sentence in full: when the hold is due (or a match would move at once),
the read runs a **full pass** instead, which checks rule 2's uniqueness against every path,
and then reads, **unless a full pass failed less than one minute (`FullPassRetry`) before**,
by the scanner's clock: then the read goes on without one, as it does after a failed pass. A
full pass that succeeds, whichever started it, clears the failure; a failure time in the
future (the clock stepped back) counts as now. The scheduled passes are not held back.

## Consequences
- Positive: a full disk or a damaged `app.db` stops changes before the files and the
  database disagree further, without losing reads; a probe sees it; a vault the walk can't
  read costs one walk a minute, not one per read.
- Negative / costs:
  - a fault on a background write (the expiry, the scan) also stops saves until a restart;
  - the web app says "the server is busy" for a read-only vault (it doesn't read the
    message yet);
  - a held path waits up to a minute longer when the walk failed, then resolves on a read or
    the next start.
- Follow-ups: the alert metric with v0.3's `/metrics` (ADR-0030); `ENOSPC` and `fsync`
  errors on vault files joining the state with ADR-0033's intents (ADR-0003 §5, §6);
  data format 2 (the rest of the IDs slice's part 4) states the scan's failure behaviour at
  format 2.

## Alternatives considered
| Option | Why not |
|---|---|
| `507 Insufficient Storage` for every change | Only true for a full disk; a damaged database isn't a storage quota, and the operator reads the log either way |
| `503` with a `Retry-After` | Nothing changes until a restart; clients would retry on a clock that means nothing |
| Keep writing the database after a fault (only the vault read-only) | A background job would keep writing a file SQLite called corrupt or that has no room |
| Leave the read-only state when a later write succeeds | Nothing retries the commit (ADR-0030); a damaged file can pass one small write and still be damaged |
| `/readyz` `503` while read-only | Orchestrators would restart or drain a server that still serves reads, and a restart may fault again |
| For #184, remember the failure only until the next scheduled pass | After the 10 s pass there is none until the next start (the watcher comes later), so it would never retry |
