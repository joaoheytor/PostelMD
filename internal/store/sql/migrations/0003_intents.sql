-- Intents (ADR-0033). Released migrations are never edited (ADR-0030).
--
-- One row per PostelMD write in flight: committed before the operation's first filesystem
-- step, deleted in the transaction that records its result. Recovery at start judges each
-- open row on the filesystem and either rolls it forward or drops it; a row it can't judge
-- (interference) is flagged and stays, and the sweep and the scan spare what it names.
--
-- The op ID is 32 hex digits (the temp's name), except a delete's, which is its trash stamp
-- (ADR-0022), also the op ID of its trash record. The payload is JSON that the store writes
-- and reads strictly: paths, IDs, generations, hashes and stats, never note content. Times
-- are INTEGER milliseconds since 1970 (UTC).
CREATE TABLE intents (
    op_id    TEXT PRIMARY KEY CHECK (length(op_id) > 0),
    kind     TEXT NOT NULL CHECK (kind IN ('save', 'create', 'move', 'delete', 'restore')),
    state    TEXT NOT NULL CHECK (state IN ('open', 'flagged')),
    payload  TEXT NOT NULL CHECK (length(payload) > 0),
    at_ms    INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

-- A row an operation vacated (ADR-0033, "Giving a path a row"): PostelMD put a file on a path
-- its no-clobber rename proved absent, while the database still had a row there (its file
-- went outside since the last scan). The row stays, suspect, so the scan's rename rules and
-- two-pass absence still decide its ID (ADR-0031); its path becomes X'00' || id, a
-- placeholder no file can have (a NUL byte), so files.path stays unique, and the path it had
-- is kept here, for its tombstone. NULL for every other row, and set exactly while the path
-- is a placeholder.
ALTER TABLE files ADD COLUMN displaced_from BLOB
    CHECK ((displaced_from IS NULL) = (substr(path, 1, 1) IS NOT X'00')
        AND (displaced_from IS NULL OR (length(displaced_from) > 0 AND state = 'suspect')));
