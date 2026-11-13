# ADR-0034: app.db in serve before data format 2

- **Status:** Accepted
- **Date:** 2026-11-13

## Context
Supersedes two parts of [ADR-0030](0030-v02-storage-sqlite.md):

- in "Checks at start, and losing the database", the fix a failing check names: "`app.db`
  missing at format 2, or `quick_check` failing: **refuse to start**, change nothing, name the
  fix `postelmd db rebuild`", and "Only a damaged file (…) or one that isn't a database (…)
  names `db rebuild`";
- in Consequences, follow-up 2, the order of its work and when it is done: "**wiring and
  format 2** with ADR-0031's IDs slice: `serve` opens the database (…) Done only with the
  runbook in `docs/operations/` (…); and an image-level upgrade smoke test (…). The maintainer
  takes a `.postelmd` copy before that slice reaches `:main`".

Follow-up 2 is too large for one reviewable PR. Its first part, #175, wires `app.db` into
`serve` at data format 1, before `db rebuild` exists. A refusal that names a command the
binary doesn't have leaves the operator with no fix.

## Decision
- **Follow-up 2 ships in parts.** The first part (#175) stays at data format 1: `serve` opens
  `app.db` under the instance lock after the format check, records the boot UUID (ADR-0014),
  listens before opening anything (`/healthz` `200`, every other route `503` with
  `Retry-After` until ready), refuses a state directory that only `POSTELMD_ALLOW_NETWORK_FS`
  accepts, and `config` makes `DataDir` absolute. A database created this way has no
  "assigned" flag: ADR-0030's format table already covers it (first upgrade, from step 2).
  The later parts bring format 2 with ADR-0031's IDs, `db rebuild` and `vault claim`.
- **The done conditions gate the part that writes format 2:** the runbook in
  `docs/operations/` with everything ADR-0030's follow-up 2 lists, the image-level upgrade
  smoke test (0.1.0 → new → 0.1.0 refuses → rollback → 0.1.0 starts), and the maintainer's
  `.postelmd` copy before that part reaches `:main`. Parts at format 1 need none of them:
  rolling back to v0.1 is still the digest swap, since v0.1 ignores `app.db`.
- **Until `db rebuild` ships, at format 1,** a damaged `app.db` (`quick_check` not `ok`,
  `SQLITE_CORRUPT`, `SQLITE_NOTADB`) is refused, changing nothing, and the refusal names the
  manual fix: with the server stopped, move `app.db`, `app.db-wal` and `app.db-shm` together
  into a new folder such as `.postelmd/damaged-<date>/`, never deleting them, then start
  again. At format 1 they hold only the boot UUID, so nothing is lost; the next start creates
  a new database. From format 2, ADR-0030's text applies again: the refusal names
  `postelmd db rebuild`. The other cases (a canceled start, a busy lock, an I/O or permission
  error) stay plain errors.

## Consequences
- Positive: each part is reviewable on its own; every refusal names a fix this build has.
- Negative / costs: until format 2, a damaged database is a manual step; the operator moves
  three files by hand (the runbook's upgrade section says how).
- Follow-ups: the format-2 part switches the refusal back to `db rebuild` and meets the done
  conditions above.

## Alternatives considered
| Option | Why not |
|---|---|
| Ship follow-up 2 as one PR | Too large to review: wiring, format 2, IDs, rebuild, claim, runbook and the image upgrade test together |
| Keep naming `db rebuild` before it exists | The operator gets an "unknown command" for the fix |
| Recreate a damaged database by itself at format 1 | It holds only the boot UUID, but an automatic rebuild hides a wrong mount; ADR-0030 refuses it for the same reason |
