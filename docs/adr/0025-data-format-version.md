# ADR-0025: A data format number in /data

- **Status:** Accepted
- **Date:** 2026-11-10

## Context
[ADR-0015](0015-supply-chain-and-ci.md) ("Upgrades and rollback") requires that an older
binary refuse to start against a newer schema: no implicit downgrade. It states that for the
SQLite schema version, and the MVP has no database ([ADR-0020](0020-mvp-scope.md)): its
state is files in `/data/.postelmd/` and the vault. Nothing in `/data` recorded which
format wrote it, so once a later release changes what it stores, an operator who rolls back
would start the old binary on data it can't read, and the old binary would change it (the
vault's startup sweep alone removes files).

The check has to exist in the first release: v0.1.0 promotes the release candidate's bytes
unchanged, and a binary that doesn't check can't be taught to later.

## Decision
- **`/data/.postelmd/format`** holds the data format number in decimal, followed by a
  newline. This build's format is **1**. A later release raises it only when it changes what
  it stores in a way the earlier one can't read, and then migrates the data forward itself.
- Every command that opens `/data` (`serve`, `reset-password`, and each later offline
  subcommand) checks it **under the instance lock, before anything else reads or changes
  `/data`** (in `serve`: before the auth state and before the vault opens or sweeps):

  | `format` | Action |
  |---|---|
  | Missing | A new data directory, or one from before this ADR: write `1` durably, go on |
  | Equal to this build's | Go on |
  | Higher | Refuse: "this data was written by a newer PostelMD (format N); run that version or newer". Nothing is changed |
  | Not a plain positive decimal | Refuse as damaged, with the fix (restore from a backup; or write `1` if only 0.1 used it). Nothing is changed |
  | A symlink (never followed) or not a regular file | Refuse. Nothing is changed |

- **Writing it** is the atomic write path of ADR-0020's credential: remove a leftover
  `format.new`, create `format.new` (`0600`) and `fsync` it, rename it to `format`, `fsync`
  the state directory, all through `internal/store/fs`. One step carries meaning, the
  rename, so there is no intent (CLAUDE.md rule 14). Recovery per crash point:

  | Crash after | On disk | Next start |
  |---|---|---|
  | The leftover removal, or the temp's create or `fsync` | No `format`, maybe a `format.new` | Missing: removes `format.new`, writes `1` |
  | The rename, before the directory `fsync` | `format` = `1`, or (power loss) none | Equal, or missing: writes `1` |
  | The directory `fsync` | `format` = `1` | Equal |

  A torn `format` can't happen: the file only appears by rename after its data is durable.
  `internal/store/layout`'s crash test runs every crash point under the process-kill and
  power-loss models.

## Consequences
- Positive: a rollback past a format change fails fast with the fix, before any write, and
  the data stays as the newer release left it.
- Negative / costs: one more file to keep in backups (it is in `/data/.postelmd/`, which the
  backup already copies whole). A damaged or deleted `format` on a newer release's data
  can't be told apart from a pre-format directory: deleting it is the operator's override.
- Follow-ups: when SQLite arrives (v0.2), its schema version is checked as ADR-0015 says;
  this file stays the format of everything else in `/data`, and a release that changes it
  documents the migration in its upgrade notes.

## Alternatives considered
| Option | Why not |
|---|---|
| A version field in `auth.json` | Read after other state; missing before setup; ties the check of all of `/data` to the credential's format |
| The release version (`0.1.0`) instead of a format number | Most releases don't change the data; a rollback between two of them must keep working |
| No check until SQLite arrives | 0.1 binaries could never refuse 0.2 data: the RC's bytes become 0.1.0 unchanged |
