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
