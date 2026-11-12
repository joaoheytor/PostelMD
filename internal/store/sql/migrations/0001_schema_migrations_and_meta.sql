-- Released migrations are never edited (ADR-0030): the runner refuses a database whose
-- recorded checksum differs from the embedded file.

-- One row per applied migration, written in the migration's own transaction.
CREATE TABLE schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    sha256     TEXT NOT NULL,
    applied_at TEXT NOT NULL
) STRICT;

-- Instance-wide values: the vault UUID, the boot UUID of ADR-0014, flags (ADR-0030).
CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT, WITHOUT ROWID;
