# ADR-0042: The format table at start, and offline commands run as the state's owner

- **Status:** Accepted
- **Date:** 2026-11-17

## Context
[ADR-0030](0030-v02-storage-sqlite.md) decides the format table that a build writing data
format 2 runs at start, and [ADR-0041](0041-the-first-upgrades-intent.md) the first upgrade's
intent, with its follow-up: run those steps at start, behind the table, treating a missing
`format` file as format 1 so that the format check doesn't write `2` before the upgrade.
[ADR-0040](0040-db-rebuild-and-vault-claim.md) states ADR-0036's decisions 6 and 7 for
format 2. [ADR-0034](0034-app-db-before-format-2.md) names `postelmd db rebuild` for a damaged
database from format 2. This ADR records how the code does all of it, and one decision none
of them made: who may run the offline commands.

This build still writes data format 1. Every rule below applies from **build format 2**, the
format the binary writes (`layout.FormatVersion`), and changes nothing at 1. The tests run
them by setting the build format to 2, not by skipping until it is.

## Decision
- **The format check (step 2).** At build format 1 a missing `format` file is written as
  before (ADR-0025). From build format 2 a missing file is read as format 1 and nothing is
  written: v0.1.0 data has none, and a new data directory is v0.1-shaped data with nothing
  in it. The first upgrade's step 5 writes `2`. A format newer than the build's is refused,
  as before.
- **Which path.** `upgrade.intent` in the state directory means an upgrade to resume,
  whatever `format` says (a kill after step 5 leaves `2` and the intent): the first upgrade
  runs. Without it, data at format 1 runs the first upgrade, whose refusals are the format
  table's format-1 rows (ADR-0041); a marker naming a vault before any upgrade is refused
  as a vault whose state directory isn't here, naming the right mount and `db rebuild`.
  Data at format 2 runs the table below.
- **At format 2, `app.db` must be there.** It is opened without being created; a missing
  one is refused, naming `db rebuild` (ADR-0030's "2, absent" row). With `upgrade.intent`
  there it is created like any start's, even at format 2 (a kill after step 5, then the
  database lost): the resumed upgrade assigns again under the intent's vault UUID. No ID has
  left the server before the upgrade ends (ADR-0041), so nothing a client holds is lost.
- **Every row is checked before the vault opens** (after the database and its migrations,
  ADR-0030's step 5). Opening adopts the folder (ADR-0027), sweeps its temp folder and runs
  the rename probe, so a start refused after it would have changed the vault it refuses,
  deleting a save's temp; and it writes an empty marker where there was none, which the
  table must tell from an empty one. So the first upgrade's refusals (the intent, the flag
  against the intent's vault UUID, the marker) run first and change nothing; the upgrade
  itself runs once the vault is open. The auth state opens before these checks (after the
  database, as in v0.1), so a refused first start can still write `auth.json` and
  `secret.key` into the state directory: that is the instance's own state, and the vault is
  untouched. The format-2 rows, from ADR-0030:
  the marker naming the stored vault UUID goes on; another UUID is refused (mount the right
  vault, or `db rebuild`); a marker that doesn't parse or isn't a regular file is refused,
  naming `vault claim`; a missing marker beside any row, or an empty one the identity check
  doesn't pass, is refused, naming the mount and `vault claim`; a missing marker beside no
  row at all, or an empty one the identity check passes, is rewritten with the stored UUID
  and the format, with a warning. ADR-0030 says "no live rows" for the missing marker; a
  suspect row (ADR-0031) is still an ID, so it counts too, and only a database with no ID
  at all, which nothing can misattribute, gets the rewrite. A rewrite is one rename (ADR-0031), so it needs no intent
  (CLAUDE.md rule 14) and adds no kill point.
- **A database at format 2 without the "assigned" flag is refused**, naming `db rebuild`:
  the first upgrade sets the flag before it writes `2`, and `db rebuild` before it does, so
  that database isn't this state directory's. ADR-0030's table has no row for it.
- **"assigned"** is set by the first assignment's transaction (ADR-0031), in the first
  upgrade at start or in `db rebuild`; nothing else sets it.
- **ADR-0036's decisions 6 and 7 (ADR-0040).** From build format 2 the scan never runs on a
  marker that doesn't name the database's vault: the table has refused that start, and if the
  scan's own check still finds a mismatch it is an error, not a warning and a skipped scan. A
  failed first pass is still logged and serving goes on.
- **A damaged database** (`SQLITE_CORRUPT`, `SQLITE_NOTADB`, `quick_check` not `ok`) is refused
  from build format 2 naming `postelmd db rebuild`, on data at format 1 or 2, with SQLite's
  error; the manual move of ADR-0034 is format 1's text only.
- **Offline commands run as the state's owner.** From build format 2, `db rebuild`,
  `vault claim`, `db rollback` and `reset-password` refuse, before the layout check creates
  any folder and before the instance lock, when their effective UID isn't the owner of
  `/data/.postelmd` (`lstat`: a symlink or a non-folder there is refused too), or of `/data`
  while `.postelmd` doesn't exist yet (`stat`: a data directory given as a symlink to a folder
  is judged by its target). What they create (a new `app.db`,
  `rebuild.intent`, `damaged-<time>/`, `auth.json`) would belong to that UID, and the server,
  running as the owner, couldn't open it: a `docker run --rm … postelmd db rebuild` without
  `--user` runs as the image's user, and a `sudo` on the host as root. Root is no exception.
  The refusal names the owner's UID and `--user <uid>:<gid>`.

## Consequences
- Positive: the table, the first upgrade and the commands are tested at format 2 before the
  release that writes it; a v0.1.0 data directory is upgraded, never stamped `2` first; an
  offline command can't leave the server a file it can't open.
- Negative / costs: a v0.1.0 directory keeps no `format` file until the upgrade's last step;
  an operator who ran a command as root before must run it again as the owner; the build
  format is a package variable in `cmd/postelmd` (`buildFormat`) that only tests change.
- Follow-ups: the IDs slice's last part raises `FormatVersion` to 2, which turns all of this
  on; the runbook (part C) says how to run the offline commands as the owner.

## Alternatives considered
| Option | Why not |
|---|---|
| Keep writing the format for a missing file, then upgrade | `2` on disk before the upgrade: a crash before step 1 leaves format-2 data with no IDs, which the table refuses |
| Run the table after the vault opens | Adoption writes an empty marker, so "missing" and "empty" can't be told apart |
| Let root run the offline commands | The files it creates belong to root, and the server, as UID 65532 or `--user`, can't open them |
| `chown` what the command created to the state's owner | Only root can, and only for the files it knows it created (SQLite creates its own); refusing first changes nothing |
| Skip the format-2 tests until FormatVersion is 2 | A test that always skips is a quarantine; the build format is injected instead |
