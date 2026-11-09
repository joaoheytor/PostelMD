# ADR-0006: Vaults, memberships, confinement and reserved paths

- **Status:** Proposed
- **Date:** 2026-11-09 (revised after design review)
- **MVP scope (ADR-0020):** the MVP keeps confinement and reserved paths; vaults, memberships and the `RENAME_NOREPLACE` fallbacks come later (one user, one vault).

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
- The store opens each vault as an `*os.Root` (needs **Go 1.25+**, which adds `Rename`, `Link`,
  `MkdirAll` and `RemoveAll`; the project builds with the latest supported Go, ADR-0001). Every file operation goes through it, which blocks `..`,
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

### Confinement spike results (2026-11-09, Go 1.27.3, Linux 6.18)
Implemented in `internal/store/fs` (`ReadFile`, `RenameNoReplace`), with tests and a fuzz
target.

| Question | Finding | Consequence |
|---|---|---|
| Does `os.Root` follow symlinks? | Yes, for any symlink that stays inside the root (`link-inside`, `dirlink/note.md` were read). It blocks escapes. | `os.Root` alone can't enforce "symlinks are never followed". The store uses `openat2` instead. |
| Does `Root.Rename` overwrite? | **Yes, silently.** | Never used for moves; ADR-0003 moves use `renameat2(RENAME_NOREPLACE)`. |
| `openat2(RESOLVE_BENEATH \| RESOLVE_NO_SYMLINKS \| RESOLVE_NO_MAGICLINKS)` | Rejects a symlink in **any** component, including the last (`ELOOP`), and every escape (`EXDEV`). Don't add `O_NOFOLLOW`: with `O_PATH` it returns the symlink itself. | The single resolution primitive for every vault path. `OpenRoot` probes it and fails fast on kernels < 5.6. |
| Symlink-swap race (directory atomically exchanged with an escaping symlink in a tight loop) | ~330,000 reads and ~38,000 renames across runs: **0 escapes**. A naive `filepath.Join` + `os.ReadFile` under the same attack leaks within milliseconds (negative control, kept as a test). | The fd-relative design holds; the race tests stay as permanent regression tests. |
| Fuzzing `ReadFile` with arbitrary paths | 828,302 executions in 30 s, no read outside the vault. | Runs on every PR (`make fuzz`, 30 s per target). |
| `renameat2(RENAME_NOREPLACE)` by filesystem | ext4 and tmpfs: supported. **9p** (WSL `/mnt/c`, the Docker Desktop bind-mount family): **`EINVAL` (unsupported)**. On 9p, `link()`, `openat2` and `flock` work. | `RenameNoReplace` returns `ErrNoReplaceUnsupported` and changes nothing. Files then use the ADR-0003 `link`+`unlink` fallback; folder moves need the `rename-checked` mechanism (issue #6). Filesystem detection: issue #24. |
| Non-Linux | Not implemented: `OpenRoot` returns `errors.ErrUnsupported`. The code still compiles for macOS and Windows. | The server runs on Linux (including Docker Desktop's VM). The portable `O_NOFOLLOW` walk arrives with personal mode (issue #34). |
| go-git `billy.Filesystem` adapter | Not evaluated here; it depends on go-git's API. | Moved to the go-git capabilities spike. |

### Reserved paths
One function, `store.IsReserved(relPath)`, is applied by the store to **every** operation:
read, write, create, upload, move (both source and destination), delete, folder ops, search
results, history and diff, reconciliation, the watcher, indexing, the commit-scan and staging
(a reserved path is never put in a commit), pull (a target tree containing one is refused;
ADR-0016) and plugin vault access.

| Reserved (case-insensitive, any depth) | Reason |
|---|---|
| `.git` and everything below | Repository internals; git access only through `gitvault` |
| `.postelmd` and everything below | Application state |
| `.postelmd-trash` and everything below (at the vault root) | Vault trash for deletes (ADR-0003). Reachable only through the trash API (list, restore). |
| `.postelmd-tmp` and everything below (at the vault root) | Temp files of in-flight writes, named by their intents (ADR-0003 §5). Never reachable through the API. |
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

### Owner-only control files
Some files make a git host or git itself **run or fetch** things with the vault's
credentials: a vault editor who changes a CI workflow could get it run with the steward's
token. These paths are readable by every member but **writable only by vault owners**
(create, write, move to or from, delete; `403` with an explanation for editors):

| Path (case-insensitive) | Why |
|---|---|
| `.github/workflows/**`, `.github/actions/**` | GitHub Actions |
| `.gitlab-ci.yml`, `.gitlab/**` | GitLab CI |
| `.forgejo/workflows/**`, `.gitea/workflows/**` | Forgejo and Gitea Actions |
| `.gitmodules` | Submodule URLs that clients fetch |
| `.gitattributes` | Filter, diff and merge drivers |

- Auto-commit stages a change to these paths only if its last saver is an owner. A change
  that the reconciler finds (an external writer) waits as a "pending control-file change"
  until an owner approves it, like an imported allow-list entry (ADR-0010).
- Changes arriving by pull are the remote's history and aren't blocked, but they are audited.
- Steward credentials should not be able to change workflows anyway (ADR-0010).

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
