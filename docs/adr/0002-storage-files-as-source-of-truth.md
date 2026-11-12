# ADR-0002: Files are the source of truth; SQLite for derived data and identity

- **Status:** Proposed
- **Date:** 2026-11-09
- **MVP scope (ADR-0020):** the MVP has no SQLite; this database arrives in v0.2.

## Context
Users must be able to keep using git, vim and Syncthing on their notes, and must be able to walk
away from PostelMD without exporting anything. We also need users, sessions, memberships and
fast search.

## Decision
- Note content lives **only** as files under `/data/vaults/<slug>/`. There is no copy in a database.
- SQLite (`modernc.org/sqlite`, pure Go, WAL mode, `synchronous=FULL`; ADR-0003 §5) at `/data/.postelmd/app.db` stores:
  - **authoritative:** users, credentials, sessions, vaults, memberships, invites, plugin
    approvals, secret findings and acknowledgements, **file IDs, generations and tombstones**
    (ADR-0003), the git intent log (ADR-0016) and the event outbox;
  - **derived (rebuildable):** the FTS5 index, link graph and file metadata cache, in a
    separate `/data/.postelmd/index.db` so its damage never touches identity
    ([ADR-0030](0030-v02-storage-sqlite.md)). `postelmd reindex` rebuilds it from the files.
- A single writer process owns `/data`, enforced by the instance lock (ADR-0014). No external
  database in v1.
- Reserved paths (`.git/`, `.postelmd*`, Syncthing internals; ADR-0006) are filtered by
  `store.IsReserved` in **every** component that enumerates or writes vault paths: the tree,
  watcher, reconciler, index, commit-scan, staging, pull and backups. `.git/info/exclude`
  also lists them, but only as defence in depth: an external git or a hand-edited exclude
  file can't be trusted, and auto-commit builds trees from bytes without consulting it.

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
