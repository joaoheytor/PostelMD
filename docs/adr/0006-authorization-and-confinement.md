# ADR-0006: Vaults, memberships, confinement and reserved paths

- **Status:** Proposed
- **Date:** 2026-11-09 (revised after design review)

## Context
Several users share one process and one volume. A path-traversal or authorization bug would
expose other users' notes. FrankMD needed several later fixes for path containment. Hiding
sensitive paths from listings is not the same as denying access to them.

## Decision

### Model
- `User`, with an `instance_admin` flag.
- `Vault {id, slug, git_remote?}`. Each user gets a personal vault, created on first login.
- `Membership {user, vault, role: owner | editor | viewer}`. A shared vault is a vault with
  several memberships.
- Every vault has **at least one owner** at all times (ADR-0018).

### Authorization
- Enforced in the `service` layer: every use case calls `authz.Require(user, vault, perm)`
  before touching the store. Handlers never decide access.
- Permissions are re-checked on every request and on every SSE event delivery (ADR-0018).

### Confinement
- The store opens each vault as an `*os.Root` (**Go 1.25+**, which adds `Rename`, `Link`,
  `MkdirAll` and `RemoveAll`). Every file operation goes through it, which blocks `..`,
  absolute paths and symlink escapes at the OS level.
- `os.Root` has no no-replace rename and **follows symlinks that stay inside the root**. So
  `internal/store/fs` is the one place allowed to call `x/sys/unix` directory-fd syscalls on
  the root's directory: `renameat2(RENAME_NOREPLACE)` (ADR-0003 §2) and
  `openat2(RESOLVE_NO_SYMLINKS | RESOLVE_BENEATH)` for every open. Where `openat2` is
  unavailable (non-Linux, old kernels), the store opens with `O_NOFOLLOW` on each path
  component, walking from the root.
- go-git reaches vault files only through a `billy.Filesystem` adapter over the same store
  API (v0.0 spike).
- Raw `os.*` calls on user-supplied paths are forbidden (lint rule: depguard or forbidigo).
- Symlinks inside vaults are not followed by any operation: they are listed as "unsupported"
  entries and are not readable through the API.

### Reserved paths
One function, `store.IsReserved(relPath)`, is applied by the store to **every** operation:
read, write, create, upload, move (both source and destination), delete, folder ops, search
results, history and diff, reconciliation, indexing and plugin vault access.

| Reserved (case-insensitive, any depth) | Reason |
|---|---|
| `.git` and everything below | Repository internals; git access only through `gitvault` |
| `.postelmd` and everything below | Application state |
| `.postelmd-trash` and everything below (at the vault root) | Vault trash for deletes (ADR-0003). Reachable only through the trash API (list, restore). |
| `.postelmd-secrets-allow` | Readable, but writable only through the findings API (ADR-0010) |
| `.stfolder`, `.stversions`, `.sync-conflict-*` | Syncthing internals. Conflict copies are surfaced in a dedicated UI, not edited in place. |
| Any path component that is empty, `.` or `..`, or that contains NUL or a control character | Defence in depth (the OS-level confinement also blocks these) |

- Attempts on a reserved path return `404` (not `403`), so they reveal nothing, and are logged
  at `warn` with the user ID.
- **Instance state is never inside a vault.** On `/data`, app state lives in `/data/.postelmd/`
  and vaults in `/data/vaults/<slug>/`. When adopting an existing folder, the folder becomes
  `vaults/<slug>` (moved in place or mounted), and setup **refuses** if the folder contains a
  `.postelmd/` directory or `app.db`, `keys/` or `*.db` files at its root, unless the admin
  explicitly confirms after seeing the list.

### Testing
- Table-driven authz tests for every role × operation.
- Fuzz tests on path handling.
- A reserved-path test matrix covering every operation listed above.

## Consequences
- Positive: two independent layers of defence (authz + OS-level confinement), plus explicit
  reserved paths.
- Negative: requires Go ≥ 1.25 and a small Linux-specific syscall layer (with a portable
  fallback); symlinked content isn't supported.

## Alternatives considered
| Option | Why not |
|---|---|
| String prefix checks after `filepath.Clean` | Easy to get wrong (symlinks, races); FrankMD needed several fixes |
| Hiding reserved paths from listings only | Listing ≠ access control |
| One container per user | Operationally heavy for a family-scale app |
