# ADR-0030: v0.2 storage: SQLite in /data, its driver, migrations and data format 2

- **Status:** Proposed (Accepted with the `store/sql` slice, the first code that depends on it)
- **Date:** 2026-11-12

## Context
Supersedes the opening of the "No database" section of [ADR-0020](0020-mvp-scope.md):
"The MVP has **no SQLite**. ADR-0002's database arrives with the features that need it: file
identity, the watcher, search and multiple users." Its bullets (`auth.json`, first run,
signed-cookie sessions, password change, login throttling, argon2id) stand: v0.2 keeps
them, and accounts move into the database with multiple users (v0.3).

v0.2 ([roadmap](../roadmap.md)) needs state that files can't hold safely: file IDs and
generations, tombstones, durable intents (CLAUDE.md rule 14), the outbox, then a search index.
[ADR-0002](0002-storage-files-as-source-of-truth.md) chose SQLite and
[ADR-0003](0003-save-protocol.md) §5 its durability, both Proposed. This ADR fixes what the
first slices build: the driver, where the files live, how they are opened, migrated, checked,
rebuilt and copied, and how `/data`'s format number ([ADR-0025](0025-data-format-version.md))
moves to 2. The rest of v0.2's design: [ADR-0031](0031-v02-file-identity.md) (file identity),
[ADR-0032](0032-v02-etag-and-api.md) (the ETag and API), [ADR-0033](0033-v02-intents.md)
(intents).

## Decision

### Driver
- **`modernc.org/sqlite` v1.61.0** (SQLite 3.54.1), through `database/sql`, only in
  `internal/store/sql` (CLAUDE.md rule 1). Published 2026-10-21, the newest release at least
  two weeks old on 2026-11-12 (ADR-0015). Pure Go, so the build stays `CGO_ENABLED=0`; FTS5
  included. Its modules and pins are in ADR-0015's clarification of 2026-11-12. Queries are
  hand-written and parameterized in v0.2; `sqlc` comes with v0.3's tables.
- The DSN is a compile-time constant and a `file:` URI (with a plain file name the driver
  strips the query, so `mode=ro` would silently open read-write). The process enables the
  driver's `StrictPragmas` (process-wide): a `_pragma` value holding more than one statement
  fails to open.

### Files and how they are opened
- **`/data/.postelmd/app.db`** (identity and in-flight work) and, from the search slice,
  **`/data/.postelmd/index.db`** (FTS and links, derived): damage to the index never forces a
  rebuild of identity. Both live in the reserved state directory, which ADR-0014's filesystem
  table checks (WAL needs shared memory and POSIX locks). Once `app.db` exists,
  `POSTELMD_ALLOW_NETWORK_FS` no longer overrides that check for the state directory; it
  still applies to vaults.
- **Every database file and every copy of one is created `0600`**: `store/sql` creates the
  file empty with `O_CREAT|O_EXCL` before SQLite opens or writes it (`app.db`, `index.db`,
  each `VACUUM INTO` target, a restore's temp), so `-wal` and `-shm` inherit the mode; a
  partial copy is removed on error. A start that finds group or world bits on the state
  directory or a database logs a warning and goes on (Helm's `fsGroup` makes them `0660`; the
  chart notes recommend `fsGroupChangePolicy: OnRootMismatch`).
- Opened only by a command that holds the instance lock and has passed the format check
  (CLAUDE.md rule 12).
- **Connections:**
  - **one writer** (`SetMaxOpenConns(1)`, `_txlock=immediate`): every write transaction is
    `BEGIN IMMEDIATE`, and the instance lock rules out a second process;
  - **a read pool** of 4, `mode=ro` with `query_only=1`: the last committed snapshot, never
    blocking the writer;
  - **a snapshot connection**, `mode=ro` without `query_only` (`VACUUM INTO` fails under
    `query_only`), opened for a snapshot or backup and closed after.
- **Pragmas** on every connection:

  | Pragma | Value | Why |
  |---|---|---|
  | `journal_mode` | `WAL` | Readers don't block the writer; set when the file is created |
  | `synchronous` | `FULL` | A commit is on disk when it returns. With `NORMAL` in WAL mode a power loss can drop the last commits: an intent a rename relies on, or a generation whose ETag a client holds (ADR-0003 §5) |
  | `busy_timeout` | `5000` ms | Only readers, checkpoints and snapshots meet a lock; 5 s is the vault mutex's wait before `503 Retry-After` |
  | `foreign_keys` | `ON` | Intents and outbox rows refer to files |
  | `trusted_schema` | `OFF` | Nothing in the schema runs as a trusted function |
  | `_defensive` (DSN) | `1` | `SQLITE_DBCONFIG_DEFENSIVE`: no `writable_schema` or other ways to corrupt the file from SQL |
  | `cell_size_check` | `ON` | Detects some corruption on read instead of acting on it |
  | `journal_size_limit` | 64 MiB | The WAL shrinks after a burst |

  The WAL is checkpointed automatically and with `wal_checkpoint(TRUNCATE)` at a clean stop.
- **Lock order and transactions:** the vault mutex first, then the write transaction;
  nothing waits for the vault mutex while holding a transaction. No filesystem I/O inside a
  write transaction (a transaction only records what the filesystem steps already did or will
  do). Background writers (outbox, purge, tombstone expiry) never take the vault mutex. Code
  never calls the writer `*sql.DB` while holding its transaction (with one connection, that
  deadlocks). The writer connection is taken with a context deadline (5 s), never unbounded.
- **I/O errors.** `SQLITE_CORRUPT`, `SQLITE_IOERR` or `SQLITE_FULL` on the writer fails the
  operation, puts every vault read-only (ADR-0003 §6), and logs an error with `pathless` (the
  alert metric arrives with v0.3's `/metrics`); `/readyz` stays ready and its body says
  `read-only`: a state word only, never a path or error text (CLAUDE.md rule 15). Nothing
  retries the commit. The next start runs the checks below.

### Migrations
- SQL files embedded in the binary, `internal/store/sql/migrations/NNNN_slug.sql`, numbered
  from 1 without gaps, forward-only. Each runs in **one transaction** that also sets
  `PRAGMA user_version = NNNN` and inserts `(version, name, sha256)` into
  `schema_migrations`. A crash mid-migration leaves the version before it; the next start
  runs it again.
- `user_version` above this build's last migration → refuse ("this database was written by
  a newer PostelMD"), nothing changed (ADR-0015). An applied migration whose embedded checksum
  differs → refuse: released migrations are never edited.
- Migrating an existing database (`user_version` ≥ 1) takes ADR-0015's pre-migration snapshot
  first (`VACUUM INTO` on the snapshot connection, `migrate.intent`, `post_migration_started`).
  v0.2.0 only creates databases (version 0 → N, no snapshot), so **`postelmd db rollback` ships
  with the first release that migrates an existing database**, not in v0.2.0.
- Steps that touch the vault (ID assignment, ADR-0031) are not SQL migrations: they are
  startup steps keyed by the data format.

### What is truth and what can be rebuilt
Files stay the only copy of content (ADR-0002). Credentials stay in `auth.json` and
`secret.key`, so losing a database never locks the owner out.

| File | Holds | If lost |
|---|---|---|
| `app.db` | identity (`files`, `tombstones`, `meta`: vault UUID, boot UUID of ADR-0014, flags), in-flight work (`intents`, trash records, `outbox`) | New IDs from the files (`db rebuild`); every ETag clients hold stops matching; interrupted operations can't be judged, so their temps are kept |
| `index.db` | FTS5 and link tables (the search slice) | Deleted and rebuilt by `postelmd reindex`, or at start when `quick_check` fails on it |

The search slice chooses contentless or external-content FTS5, so note text isn't copied
into `index.db`, and decides on `secure_delete`.

### Startup order (format-2 builds; ADR-0004's list is edited to match)
1. The layout checks (ADR-0025: the state and vault folders, the lock file), then the
   instance lock, then **the HTTP listener**: `/healthz` answers `200`, `/readyz` and every
   other route `503` with `Retry-After` until step 9 (Helm's `startupProbe` must allow for
   the first upgrade on a large vault). The listener starts before the format check and the
   database, and serves nothing else until step 9.
2. The format check (table below), the restore-in-progress refusal.
3. Open `app.db`; `PRAGMA quick_check` (linear in the file's size; the identity database
   holds rows of metadata and stays small, the index lives apart).
4. Migrations (ADR-0015); `post_migration_started` if one ran.
5. The vault check (ADR-0031's marker) and, on format-1 data, the first upgrade below.
6. Intent recovery, then the temp sweep (ADR-0033).
7. The startup scan (ADR-0031), later the watcher and full reconcile (ADR-0004).
8. Outbox replay, background workers, the admin socket.
9. `/readyz` answers `200`.

Long steps log progress (step, n of N) every 5 s. Structured records, never with note
content: migrations (from, to, duration, snapshot path); `quick_check` (result, duration);
the first ID assignment (count, duration); each rebuild step; scan totals (new IDs,
generation bumps, suspects, tombstones).

### Data format 2
- The IDs slice is **format 2**. A v0.1 binary refuses it (ADR-0025): it doesn't know
  intents, and its sweep would delete temps that an open save intent names. This **extends**
  ADR-0025's table to format-2 builds (a dated clarification there links here).
- **First upgrade** (format missing or 1):
  1. copy `format` (if present), `auth.json`, `secret.key` and `config.yaml` (if present) into
     `/data/.postelmd/pre-format-2/`, then write its `complete` file. Each copy is created
     `0600` with `O_CREAT|O_EXCL` (as `auth.json` is), written, `fsync`ed and renamed; then
     the folder is `fsync`ed. A complete copy is kept (a resumed upgrade); an incomplete one
     is first moved aside to `pre-format-2-<time>/`, never written over;
  2. create and migrate `app.db` (a database left by the `store/sql` build, without the flag,
     is used as it is);
  3. one transaction: a new vault UUID, every file row, the trash records (ADR-0031), the
     "assigned" flag;
  4. write the vault marker with the UUID and format 2 (ADR-0031);
  5. write `2` into `format` (ADR-0025's atomic write).

  **The identity check** used below: at least 90% of up to 1,000 live `files` rows are found
  at their path with the same inode and size. The sample is deterministic: rows ordered by a
  hash of their ID seeded with the stored vault UUID, the first 1,000 taken, so a test gets
  the same answer on every run (tests also use vaults where every row, or no row, matches).
  A database with no live rows passes (no ID can be misattributed).

  | `format` | `app.db` | Vault marker | Action |
  |---|---|---|---|
  | Missing or `1` | Absent, or without the flag | Absent or empty (v0.1) | First upgrade, from step 1 if `pre-format-2/` isn't complete, else from step 2 |
  | Missing or `1` | Absent, or without the flag | A UUID | Refuse: this vault belongs to a v0.2 instance whose state isn't here. Fix: mount the right state directory; after a rollback that skipped emptying the marker, `postelmd db rebuild` |
  | Missing or `1` | Flag set | Absent or empty | Crash before step 4: if the identity check passes, steps 4 and 5; else refuse as a swapped mount (below) |
  | Missing or `1` | Flag set | Same UUID | Crash before step 5: step 5 |
  | Any | Flag set, or present at `2` | Another UUID | Refuse: another vault is mounted. Fix in the message: mount the vault this state belongs to, or `db rebuild` to adopt this one with new IDs |
  | `2` | Present | Same UUID | Go on |
  | `2` | Present, with live rows | Absent | Refuse, changing nothing (no tombstone): the message names both fixes, mount the right folder, or, if this is the vault, `postelmd vault claim` |
  | `2` | Present, with no live rows | Absent | Nothing can be misattributed (the identity check passes): rewrite the marker with the stored UUID, log a warning, go on |
  | Any | Any | Present but unparseable (not the three lines of ADR-0031, a symlink, or not a regular file) | Refuse, changing nothing; the message names `postelmd vault claim` |
  | `2` | Present | Empty | ADR-0027's printed fix recreated it, or another v0.1 folder is mounted. If the identity check passes, rewrite it with the stored UUID, log a warning, go on; else refuse as for an absent marker |
  | `2` | Absent | Any | Refuse: database lost; fix `postelmd db rebuild` |
  | Higher, damaged, symlink | | | ADR-0025's rows |

- **`postelmd vault claim`** (offline, under the instance lock): prints the vault's path, its
  file count and how many live rows match by path, inode and size, asks for confirmation
  (`--yes` for scripts), then writes the marker with the stored UUID and format 2. When the
  identity check fails (for example no row matches), `--yes` alone refuses: it takes an
  explicit `--force`, or typing the vault's path at the interactive prompt, since the next
  scan will then tombstone every row that doesn't match.
- **Rollback to v0.1** (runbook, no command), with v0.2 stopped after one clean start and
  stop (so no intent is open):
  1. move `app.db`, `-wal` and `-shm` together into `rolled-back-<time>/`;
  2. move `pre-format-2/` to `pre-format-2-<time>/`;
  3. empty the marker in place, `: > "$vault/.postelmd-tmp/.created-by-postelmd"` (never
     delete it: v0.1 would refuse a vault whose trash isn't empty, ADR-0027);
  4. write `1` into `format`.

  On a named volume (no host shell on the data), steps 1–4 run in a helper container with
  the volumes mounted, e.g. `busybox`, **as the data's owner** (`--user 65532:65532`, the
  image's user, or the operator's ID used with `--user`), and steps 3 and 4 truncate and
  write the existing files rather than replacing them, so their owner and mode don't change.

  v0.1 then sees v0.2's edits as files, and the trash layout is the same (ADR-0022). Lost:
  file identity. The next v0.2 start runs a normal first upgrade, with a fresh
  `pre-format-2/`; `db rebuild` is the fallback when step 3 was skipped.

### Checks at start, and losing the database
- `app.db` missing at format 2, or `quick_check` failing: **refuse to start**, change
  nothing, name the fix `postelmd db rebuild`. Never rebuild by itself: a missing database is
  also what a wrong mount looks like, and the operator must know IDs and intents are lost.
  `index.db` failing its check is deleted and rebuilt: it is derived.
- **`postelmd db rebuild`** (offline, under the instance lock; in a container,
  `docker run --rm … postelmd db rebuild` with the same volumes):
  1. write and `fsync` `rebuild.intent`: a new rebuild UUID, the timestamp, and whether an
     `app.db` existed;
  2. move `app.db`, `-wal` and `-shm` into `damaged-<timestamp>/` (never deleted; a stale WAL
     beside a new file would be replayed into it);
  3. copy each **regular file** in each vault's `.postelmd-tmp/` (except the marker) into
     `/data/.postelmd/recovery/rebuild-<timestamp>/`, then remove it. Each is opened through
     `os.Root` with `O_NOFOLLOW|O_NONBLOCK` and checked with `fstat`; anything else (a
     symlink, a folder, a device) is left in place, never opened, and logged with its name and
     kind as structured attributes, never in the message text (#109);
  4. create and migrate the database; one transaction with the first upgrade's step 3, plus
     the rebuild UUID in `meta`;
  5. steps 4 and 5 of the first upgrade; remove `rebuild.intent`.

  | `rebuild.intent` | `app.db` | Next run (a start refuses and asks for it) |
  |---|---|---|
  | Present | Absent, or without the intent's rebuild UUID | Continue at step 2 (move what is there; a half-built database from an earlier attempt goes to `damaged-<timestamp>/attempt-<n>/`, beside the original), then 3 |
  | Present | Carries the intent's UUID, and `damaged-<timestamp>/` holds the moved files (or the intent says there was none) | Continue at step 5 |
  | Present | Carries the UUID, but the moved files are missing | Refuse: interference; the runbook decides |
  | Absent | | No rebuild in progress |

- Retention: `damaged-*/`, `rolled-back-*/`, `pre-format-2/` and `pre-format-2-*/` are never
  removed automatically; a start logs a warning while one older than 30 days exists, and the
  runbook's cleanup step removes them with the `pre-migrate-*` snapshots.
  `recovery/rebuild-*/` follows `recovery/`'s 7 days (ADR-0003): those temps were never
  acknowledged, so clients still hold the edits as drafts.
- `pre-format-2/` and `pre-format-2-*/` hold an old `secret.key` and password hash: the
  key-compromise and password-reset runbooks say to remove them, so a revoked key or a
  replaced password doesn't survive there.

### Backup and copies (ADR-0017)
- The database copy is **`VACUUM INTO`** a pre-created `0600` file on the snapshot connection,
  inside the backup barrier; we `fsync` it (SQLite doesn't), check it with
  `PRAGMA integrity_check`, and record its `user_version`. The same code takes the
  pre-migration snapshot.
- A volume snapshot holding `app.db`, `-wal` and `-shm` together is crash-consistent. A
  file-by-file copy of a running instance is **not**: until the backup command exists, the
  runbook requires stopping the server, or an atomic snapshot of the volume.
- A restore writes each database by temp, `fsync` and rename, and never leaves a `-wal` from
  elsewhere beside it.

## Consequences
- Positive: one directory, already checked, reserved and backed up; no service to run;
  losing the identity database loses no note and no login; older binaries refuse newer data
  twice (`format`, `user_version`); index damage stays derived.
- Negative / costs:
  - every save costs two durable commits (intent and completion), about two WAL `fsync`s
    per autosave;
  - pure-Go SQLite is slower than C; the search budget measures it;
  - the image grows by about 3.8 MB (about 36 MB of the 40 MB budget, the reviewers'
    estimate; the `store/sql` slice measures it);
  - rolling back to v0.1 loses file identity; going forward again assigns new IDs;
  - a vault whose marker was deleted by hand is refused until `postelmd vault claim` runs.
- Follow-ups, in order (each a PR):
  1. **`store/sql`** (flips this ADR): open, connections, pragmas, migrations with checksums,
     `quick_check`, the boot UUID (ADR-0014), listen-early `/readyz`, `db rebuild`; the
     dependency added to `go.mod`; format stays 1 (v0.1 ignores `app.db`);
  2. **format 2** with ADR-0031's IDs slice, with `postelmd vault claim`. Done only with the
     runbook in `docs/operations/`: the pre-upgrade copy (server stopped), the rollback (all
     four steps), `db rebuild` and `vault claim` in a container, every new refusal message
     and its fix, the trash's new 7-day clock (ADR-0031), the cleanup of `damaged-*`,
     `rolled-back-*`, `pre-format-2*`, `recovery/rebuild-*` and `pre-migrate-*`, and the
     `pre-format-2*` step in the key-compromise and password-reset runbooks; and an
     image-level upgrade smoke test
     (0.1.0 → new → 0.1.0 refuses → rollback → 0.1.0 starts). The maintainer takes a
     `.postelmd` copy before that slice reaches `:main`;
  3. `make perf` measures cold start on format-2 data, and reports the first upgrade's time
     apart from it.

## Alternatives considered
| Option | Why not |
|---|---|
| `ncruces/go-sqlite3` (SQLite in Wasm on `wazero`) | Pure Go too, but pre-1.0 (v0.37), adds a runtime compiler to the server, and its WAL needs its own shared-memory layer |
| `mattn/go-sqlite3` | cgo: breaks `CGO_ENABLED=0` and the distroless image (ADR-0009) |
| `zombiezen.com/go/sqlite`, `crawshaw.io/sqlite` | zombiezen wraps modernc without `database/sql`; crawshaw is cgo and unmaintained |
| No database: sidecar files per note | Two file writes per save with nothing atomic between them; tombstones, intents and the outbox each need a crash-safe format; search still needs an index |
| `bbolt` | Transactional, but no SQL or FTS (a second library with search), and v0.3's tables are relational |
| A JSON index rewritten on change | Rewrites everything per save; no concurrent readers; one corruption loses it all |
| FTS in `app.db` | Index damage would force a rebuild of identity |
| `synchronous=NORMAL` | Loses the last commits on power loss |
| Online backup API instead of `VACUUM INTO` | Driver-specific (`sql.Conn.Raw`), copies free pages; `VACUUM INTO` is plain SQL and already the snapshot |
| Rebuild automatically when `app.db` is missing | Hides a wrong mount and changes every ID clients hold |
| A `postelmd downgrade` command | Its own intent and table for a path used once; four runbook steps do it |
| Rewrite an empty or absent marker at format 2 without a check | An empty folder or another v0.1 vault mounted by mistake would be taken for this vault, and the scan would tombstone every ID |

## Decisions on the review's open questions (maintainer may revisit)
1. Format 2 **extends** ADR-0025; a dated clarification there links here.
2. **No downgrade command**: the rollback is the runbook above, with `pre-format-2/` as a
   safety copy.
3. **Hand-written queries** in v0.2; `sqlc` with v0.3's tables (ADR-0015 clarification).
4. **`quick_check` at every start.**
