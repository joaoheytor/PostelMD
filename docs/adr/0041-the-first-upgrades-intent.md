# ADR-0041: The first upgrade's intent and its mechanics

- **Status:** Accepted; it supersedes [ADR-0030](0030-v02-storage-sqlite.md)'s two format-table rows for a crash during the first upgrade ("Missing or `1`, flag set, marker absent or empty" and "Missing or `1`, flag set, same UUID")
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
  one (interference; the runbook decides).
- **Step 1, `pre-format-2/`.** Every source (`format`, `auth.json`, `secret.key`,
  `config.yaml`, those present) is read first, so one that isn't a regular file refuses
  with nothing written. A folder with `complete` is kept (a resumed upgrade). One without
  it is moved aside, with `RENAME_NOREPLACE`, to `pre-format-2-<time>/` (`-<n>` added when
  that name is taken), never written over, and the state directory is synced. Then each
  file is written to `<name>.tmp` (`0600`, `O_CREAT|O_EXCL`, `fsync`) and renamed to
  `<name>`, the folder is synced, and its empty `complete` file is created and synced.
  Retention and removal follow ADR-0030.
- **Steps 3 to 5** use the intent's vault UUID: the assignment (one transaction,
  ADR-0031), skipped when the flag is set with that UUID; the marker (one rename); the
  format (one rename). Each is safe to run again.
- **A vault swapped during a crashed upgrade costs no ID.** No file ID leaves the server
  before the upgrade ends, so if the vault mounted at the resume isn't the one assigned,
  the startup scan treats the difference as outside edits: rows that match nothing are
  tombstoned, and files get new IDs that no client has seen.
- While `upgrade.intent` is there, `db rebuild` and `vault claim` refuse, changing nothing,
  and say to start the server so the upgrade finishes; the upgrade refuses beside
  `rebuild.intent`.

| Killed at | Left | Next start |
|---|---|---|
| inside the intent's write (`upgrade-intent-synced`) | the intent's temp, no intent | removes the temp, starts the upgrade |
| `upgrade-intent-written` | the intent | step 1 on |
| `upgrade-copy-moved-aside` | an incomplete `pre-format-2/` moved to `pre-format-2-<time>/` | a new `pre-format-2/`, on |
| `upgrade-copied` (after each file) | `pre-format-2/` without `complete` | moves it aside, copies again, on |
| `upgrade-copy-complete` | `pre-format-2/` complete | keeps it; step 3 on |
| `upgrade-assigned` | the flag with the intent's UUID | steps 4 and 5 |
| `upgrade-marker-written` | the marker with the intent's UUID | the marker again, step 5 |
| `upgrade-format-written` | `format` written, the intent there | removes the intent |
| inside the intent's removal (`upgrade-done`) | the intent unlinked, maybe not durably | nothing more |
| any, with the flag or the marker under another vault UUID than the intent's | | refuses: interference; the runbook decides |

The process-kill model is `TestUpgradeIntentCrashPoints` (`internal/store/sql`) and
`TestUpgradeCrashPoints` (`internal/ids`); the power-loss model is #182.

### What this supersedes in ADR-0030
ADR-0030's rows "Missing or `1`, flag set, marker absent or empty: if the identity check
passes, steps 4 and 5; else refuse as a swapped mount" and "Missing or `1`, flag set, same
UUID: step 5" judged a crash from the flag and the marker. With the intent, a crash is
resumed from the intent; at format 1 without an intent, a set flag is refused as
interference, with the runbook's fix (`db rebuild` once format 2 ships). The other rows stay.

## Consequences
- Positive: a crashed first upgrade resumes from one record, with a crash test per point;
  the sampled identity check isn't needed to tell a resumed upgrade from a swapped vault.
- Negative / costs: one more sidecar in the state directory, and one more refusal for each
  of the two offline commands.
- Follow-ups: the IDs slice's next part runs these steps at start, behind ADR-0030's format
  table, and the last one bumps the data format; until then nothing calls them outside tests.

## Alternatives considered
| Option | Why not |
|---|---|
| ADR-0030's table alone, without an intent | It infers the crash point from the flag and the marker (rule 14); a flag a wrong mount brings looks like a crashed upgrade |
| Choose the vault UUID at step 3 | A crash after the assignment would leave nothing outside the database to check its UUID against |
| Overwrite an incomplete `pre-format-2/` | It may hold the only good copy of a file a later crash damaged; nothing is deleted |
