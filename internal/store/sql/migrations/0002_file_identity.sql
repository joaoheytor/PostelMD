-- File identity (ADR-0031). Released migrations are never edited (ADR-0030).
--
-- Paths are BLOBs: the bytes of the name as on disk, which needn't be UTF-8. Times are
-- INTEGER milliseconds since 1970 (UTC), apart from a file's mtime and ctime, kept to the
-- nanosecond because rule 1 compares them exactly. A device or inode number is the
-- unsigned value's bits as a signed 64-bit integer. The vault UUID and the "assigned" flag
-- are rows in meta (vault_uuid, ids_assigned), written in the first assignment's one
-- transaction.

-- The live rows: one per regular file /api/v1/files would list. A file ID is a ULID and is
-- never reused; a deleted ID moves to tombstones.
CREATE TABLE files (
    id                TEXT PRIMARY KEY CHECK (length(id) = 26),
    path              BLOB NOT NULL UNIQUE CHECK (length(path) > 0),
    generation        INTEGER NOT NULL CHECK (generation >= 1),
    -- The full SHA-256, or NULL while a busy writer kept the first assignment from hashing
    -- the file (three tries); the scan fills it.
    sha256            BLOB CHECK (sha256 IS NULL OR length(sha256) = 32),
    size              INTEGER NOT NULL CHECK (size >= 0),
    device            INTEGER NOT NULL,
    inode             INTEGER NOT NULL,
    mtime_ns          INTEGER NOT NULL,
    ctime_ns          INTEGER NOT NULL,
    state             TEXT NOT NULL CHECK (state IN ('live', 'suspect')),
    -- When a scan first missed the path: set exactly while the row is suspect.
    suspect_since_ms  INTEGER,
    CHECK ((state = 'suspect') = (suspect_since_ms IS NOT NULL))
) STRICT, WITHOUT ROWID;

-- The rename rules look rows up by hash (rule 2) and by device and inode (rule 1).
CREATE INDEX files_by_sha256 ON files (sha256);
CREATE INDEX files_by_inode ON files (device, inode);

-- A new path held for a rule-2 rename (ADR-0031): it has no ID until its suspect is
-- resolved, then either takes the suspect's ID or gets a new one. Removing the suspect's
-- row (a tombstone) drops the hold; the next scan sees the path as new.
CREATE TABLE pending (
    path      BLOB PRIMARY KEY CHECK (length(path) > 0),
    file_id   TEXT NOT NULL UNIQUE REFERENCES files (id) ON DELETE CASCADE,
    sha256    BLOB NOT NULL CHECK (length(sha256) = 32),
    size      INTEGER NOT NULL CHECK (size >= 0),
    device    INTEGER NOT NULL,
    inode     INTEGER NOT NULL,
    mtime_ns  INTEGER NOT NULL,
    ctime_ns  INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

-- Ended IDs, kept 30 days (ADR-0031): a tombstoned ID answers 410, one never seen or
-- expired 404 (ADR-0032). A tombstone never comes back.
CREATE TABLE tombstones (
    id               TEXT PRIMARY KEY CHECK (length(id) = 26),
    last_path        BLOB NOT NULL,
    last_generation  INTEGER NOT NULL CHECK (last_generation >= 1),
    at_ms            INTEGER NOT NULL,
    reason           TEXT NOT NULL CHECK (reason IN ('deleted', 'trashed', 'missing', 'ambiguous-rename'))
) STRICT, WITHOUT ROWID;

-- The background expiry removes rows by age.
CREATE INDEX tombstones_by_age ON tombstones (at_ms);

-- One per item in the vault's trash (ADR-0003 §2, ADR-0033). The op ID is the trash entry's
-- name: the stamp folder, or the old layout's <stamp>-<name> entry. original_path is NULL
-- when unknown (an item deleted under v0.1, imported at the upgrade, ADR-0031). The item is
-- purged 7 days after clock_ms: the delete, or the import for an imported item.
CREATE TABLE trash_records (
    op_id          TEXT PRIMARY KEY CHECK (length(op_id) > 0),
    name           BLOB NOT NULL CHECK (length(name) > 0),
    original_path  BLOB,
    kind           TEXT NOT NULL CHECK (kind IN ('file', 'folder')),
    state          TEXT NOT NULL CHECK (state IN ('moving', 'trashed', 'purging', 'restoring')),
    deleted_at_ms  INTEGER NOT NULL,
    clock_ms       INTEGER NOT NULL
) STRICT, WITHOUT ROWID;
