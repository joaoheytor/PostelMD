# ADR-0041: The first upgrade's intent and its mechanics

- **Status:** Accepted. Supersedes three parts of [ADR-0030](0030-v02-storage-sqlite.md): its format table's two rows for a crash during the first upgrade ("Missing or `1`, flag set, marker absent or empty" and "Missing or `1`, flag set, same UUID"), and step 1's "A complete copy is kept" (only this upgrade's own copy is). Also one part of [ADR-0040](0040-db-rebuild-and-vault-claim.md): `db rebuild`'s step 2 file list (it also moves an `upgrade.intent` it takes over)
- **Date:** 2026-11-17

## Context
[ADR-0030](0030-v02-storage-sqlite.md) decides the first upgrade from data format 1 to 2:
copy the v0.1 state into `pre-format-2/`, create and migrate `app.db`, assign every file an
ID under a new vault UUID in one transaction, write the vault marker, write `2` into
`format`. Its format table then judges a crash in the middle from three signals: the
format, the "assigned" flag and the marker, with the sampled identity check for the row
where the flag is set and the marker isn't written. CLAUDE.md rule 14 asks for a durable
intent before the first side effect, an operation-specific recovery table and a crash test
per point, and says never to infer what happened from a single signal. The flag alone can't
tell a crashed upgrade from a database a wrong mount, or a hand, put there.

ADR-0030's step 1 also keeps any complete `pre-format-2/`. A copy left by an earlier upgrade
(an upgrade, a rollback that didn't move the folder aside, a password reset or a `secret.key`
rotation, then a new upgrade) would be kept, and a later rollback would restore credentials
that were revoked, which ADR-0030's own retention rule forbids.

## Decision
- **`upgrade.intent`**, a sidecar in the state directory written as `rebuild.intent` is
  (temp, `fsync`, `RENAME_NOREPLACE`, folder `fsync`): `{"vault_uuid", "time"}`, the vault
  UUID the upgrade assigns, chosen before anything else, and the UTC time to the second. It
  is written **before step 1**, and removed durably after step 5. It is the only signal
  that a first upgrade is under way.
- **Step 2 comes first in practice.** `serve` opens and migrates `app.db` at ADR-0030's
  startup steps 3 and 4 (migrations have their own intent, ADR-0015), before the format
  table can read the flag; the upgrade receives that open database. Creating `app.db` touches
  nothing step 1 copies.
- **Refused before the intent, changing nothing:** a database with the flag set (no
  PostelMD step leaves it without an intent at format 1), a marker naming any vault UUID,
  and a marker that isn't a regular file. **Refused on a resumed upgrade, keeping the
  intent:** a flag under another vault UUID than the intent's, or a marker naming another
  one (interference: `db rebuild` takes it over, below).
- **Step 1, `pre-format-2/`, bound to the intent.** Every source (`format`, `auth.json`,
  `secret.key`, `config.yaml`, those present) is read first, so one that isn't a regular file
  refuses with nothing changed, an earlier copy not moved either. The folder is kept only
  when its `complete` file is a regular file holding this upgrade's tag, `<vault_uuid>
  <time>\n` from the intent (a resumed upgrade). Any other (no `complete`, another tag, an
  empty one, a folder or a symlink at its name) is moved aside, with `RENAME_NOREPLACE`, to
  `pre-format-2-<time>/` (`-<n>` added when that name is taken), never written over or kept,
  and the state directory is synced. Then each file is written to `<name>.tmp` (`0600`,
  `O_CREAT|O_EXCL`, `fsync`) and renamed to `<name>`; the folder is synced; `complete` is
  created with the tag and synced; the folder is synced again. Retention and removal follow
  ADR-0030.
- **Steps 3 to 5** use the intent's vault UUID: the assignment (one transaction,
  ADR-0031), skipped when the flag is set with that UUID; the marker (one rename); the
  format (one rename). Each is safe to run again.
- **A vault swapped during a crashed upgrade costs no ID.** No file ID leaves the server
  before the upgrade ends, so if the vault mounted at the resume isn't the one assigned,
  the startup scan treats the difference as outside edits: rows that match nothing are
  tombstoned, and files get new IDs that no client has seen.
- **`db rebuild` takes over an upgrade that can't finish.** Beside `upgrade.intent`, it
  first decides, changing nothing, whether the next start could finish it. It takes over
  only for a positive finding: an intent PostelMD didn't write (it doesn't parse), an
  `app.db` with file IDs under another vault UUID than the intent's, a damaged `app.db`
  (`SQLITE_CORRUPT`, `SQLITE_NOTADB`), or a vault marker naming another vault or not
  parsing. It then writes `rebuild.intent` with `"upgrade": true` and `upgrade.intent` in its
  `moves`, after the database's files, and moves it into `damaged-<time>/` with them (ADR-0040's
  original move, recorded and resumed as they are); `pre-format-2/` stays where it is. When
  the upgrade can finish, it refuses and says to start the server. Anything it can't read
  (anything but a regular file at the intent's name, a marker that isn't a regular file, an
  I/O or permission error) refuses, never takes over. `vault claim` always refuses beside
  `upgrade.intent`, and the upgrade refuses beside `rebuild.intent`.

| Killed at | Left | Next start (or run) |
|---|---|---|
| inside the intent's write (`upgrade-intent-synced`) | the intent's temp, no intent | removes the temp, starts the upgrade |
| `upgrade-intent-written` | the intent | step 1 on |
| `upgrade-copy-moved-aside` | an earlier `pre-format-2/` moved to `pre-format-2-<time>/` | a new `pre-format-2/`, on |
| `upgrade-copied` (after each file) | `pre-format-2/` without `complete` | moves it aside, copies again, on |
| `upgrade-copy-complete` | `pre-format-2/` complete with the tag | keeps it; step 3 on |
| `upgrade-assigned` | the flag with the intent's UUID | steps 4 and 5 |
| `upgrade-marker-written` | the marker with the intent's UUID | the marker again, step 5 |
| `upgrade-format-written` | `format` written, the intent there | removes the intent |
| inside the intent's removal (`upgrade-done`) | the intent unlinked, maybe not durably | nothing more |
| any, with the flag or the marker under another vault UUID than the intent's | | refuses: interference; `db rebuild` takes it over |
| `db rebuild`'s takeover, at `rebuild-intent-written` or a `rebuild-moved` before `upgrade.intent`'s | `upgrade.intent` in the state directory, `rebuild.intent` listing it | the next `db rebuild` moves it with the rest |
| `db rebuild`'s takeover, after `upgrade.intent`'s move | `upgrade.intent` in `damaged-<time>/` | the next `db rebuild` records the move, on |

The process-kill model is `TestUpgradeIntentCrashPoints` and `TestRebuildTakeoverCrashPoints`
(`internal/store/sql`) and `TestUpgradeCrashPoints` (`internal/ids`). For step 1, faultfs's
power-loss model is `TestCopyPreFormat2SurvivesAPowerLossAtEveryStep`: a surviving
`complete` means every copy survived in full, an earlier copy is never lost, and the copy is
durable once step 1 returns. The model makes a folder's entries durable together, so the
folder's sync before `complete` is asserted on the order of the steps
(`TestCopyPreFormat2SyncsTheCopiesBeforeComplete`). The power-loss model of SQLite's files
is #182.

### What this supersedes
- ADR-0030's rows "Missing or `1`, flag set, marker absent or empty: if the identity check
  passes, steps 4 and 5; else refuse as a swapped mount" and "Missing or `1`, flag set, same
  UUID: step 5" judged a crash from the flag and the marker. With the intent, a crash is
  resumed from the intent; at format 1 without an intent, a set flag is refused as
  interference, and `db rebuild` (once format 2 ships) is the fix. The other rows stay.
- ADR-0030's step 1, "A complete copy is kept (a resumed upgrade)": only a copy whose
  `complete` holds this upgrade's tag is kept; any other complete copy is moved aside.
- ADR-0040's step 2 file list: with a takeover, `upgrade.intent` is moved too.

## Consequences
- Positive: a crashed first upgrade resumes from one record, with a crash test per point;
  the sampled identity check isn't needed to tell a resumed upgrade from a swapped vault; a
  revoked credential never comes back from an earlier upgrade's copy; an upgrade stopped on
  interference has a way out.
- Negative / costs: one more sidecar in the state directory; `db rebuild` reads the database
  and the marker before it decides beside an upgrade's intent.
- Follow-ups: the IDs slice's next part runs these steps at start, behind ADR-0030's format
  table (treating a missing `format` file as format 1, so `EnsureFormat` doesn't write the
  new format before the upgrade runs), and the last one bumps the data format; until then
  nothing calls them outside tests.

## Alternatives considered
| Option | Why not |
|---|---|
| ADR-0030's table alone, without an intent | It infers the crash point from the flag and the marker (rule 14); a flag a wrong mount brings looks like a crashed upgrade |
| Choose the vault UUID at step 3 | A crash after the assignment would leave nothing outside the database to check its UUID against |
| Overwrite an incomplete `pre-format-2/` | It may hold the only good copy of a file a later crash damaged; nothing is deleted |
| Keep any complete `pre-format-2/` (ADR-0030) | An earlier upgrade's copy may hold revoked credentials, which a rollback would restore |
| Let `vault claim` or a start clear a stuck `upgrade.intent` | A start can't tell what to keep, and `claim` doesn't rebuild the IDs; `db rebuild` already moves a database aside and starts over |
| Let `db rebuild` take over any `upgrade.intent` | A resumable upgrade would lose its work, and an I/O error would look like a reason |
