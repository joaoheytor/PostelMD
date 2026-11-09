# ADR-0002: Files are the source of truth; SQLite for derived data and identity

- **Status:** Proposed
- **Date:** 2026-11-09

## Context
Users must be able to keep using git, vim and Syncthing on their notes, and must be able to walk
away from PostelMD without exporting anything. We also need users, sessions, memberships and
fast search.

## Decision
- Note content lives **only** as files under `/data/vaults/<slug>/`. There is no copy in a database.
- SQLite (`modernc.org/sqlite`, pure Go, WAL mode) at `/data/.postelmd/app.db` stores:
  - **authoritative:** users, credentials, sessions, vaults, memberships, invites, plugin
    approvals, secret findings and acknowledgements, **file IDs, generations and tombstones**
    (ADR-0003), the git intent log (ADR-0016) and the event outbox;
  - **derived (rebuildable):** the FTS5 index, link graph and file metadata cache. `postelmd
    reindex` rebuilds it from the files.
- A single writer process owns `/data`, enforced by the instance lock (ADR-0014). No external
  database in v1.
- `.postelmd/` is excluded from the tree, watcher, index and git.

## Consequences
- Positive: no lock-in; simple backups (files + one SQLite file); `docker run` needs no extra
  service.
- Negative: no horizontal scaling (ADR-0014). SQLite must not live on NFS or SMB: startup
  refuses unless explicitly overridden (ADR-0014).
- Backup and restore consistency is defined in ADR-0017.

## Alternatives considered
| Option | Why not |
|---|---|
| Content in a DB (Outline-style) | Breaks git, vim and Syncthing workflows and creates lock-in |
| Postgres support in v1 | Doubles the test matrix; not needed for the target scale |
| No DB (FrankMD-style) | Needs ad-hoc files for users and settings, plus a linear scan for search |
