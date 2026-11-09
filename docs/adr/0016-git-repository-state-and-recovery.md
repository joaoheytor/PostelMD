# ADR-0016: Git repository state model and recovery

- **Status:** Proposed (in v0.1)
- **Date:** 2026-11-09
- **MVP scope (ADR-0020):** the MVP uses commits from bytes and durable refs and objects, with no remote; push, pull and their intents arrive in v0.2.

## Context
ADR-0010 makes every vault a git repository with auto-commit and push/pull. Repositories can
end up dirty, divergent, mid-operation after a crash, or rejected by the remote. Branches can
diverge without any textual conflict. Several users can contribute to one batched commit. All
of this needs defined states and recovery behaviour.

## Decision

### Serialization
All application-managed repository mutations (stage, commit, pull or merge, checkout,
restore-version, history rewrite of unpushed commits) run **one at a time per vault**. They
hold the vault-level lock (ADR-0003 §4), which also blocks structural file operations. Content
saves queue behind it (bounded wait of 5 s, then `503 Retry-After`; the client draft persists).

### Supported states
| State | Meaning | Allowed actions |
|---|---|---|
| `clean` | Working tree = HEAD; in sync with the remote, or no remote | All |
| `dirty` | Uncommitted changes (normal between auto-commits) | Edit, commit; a pull first commits or keeps eligible changes |
| `ahead` / `behind` / `diverged` | Relative to the remote-tracking branch | Push (if ahead), pull (fast-forward, or a merge per the policy below) |
| `blocked:secrets` | Open findings in the working tree or the outgoing range (ADR-0010) | Edit, resolve or acknowledge; **no push** |
| `conflicted` | A merge produced conflicts | Resolve in the UI (per-file mine/theirs/manual) or **abort the merge**; no auto-commit |
| `maintenance` | An external operation is announced (ADR-0003 §1) | Read only |
| `unsupported` | Detached HEAD, an in-progress rebase or cherry-pick from outside, submodules, LFS pointers, sparse checkout, bare repo | Read only, with a clear message; the admin fixes it externally |

PostelMD works on **one branch per vault** (default `main`, configurable). It never creates
or switches branches by itself.

### Pull and divergence policy
- **v0.1: fast-forward only.** go-git v5's `Merge` supports only fast-forward merges. A
  divergent pull leaves the vault in `diverged` and offers two options:
  - publish the local commits to a new remote branch (for a PR) and then reset to the remote
    (the local commits are kept in that branch and in recovery);
  - resolve with an external git (in maintenance mode).
- **Later (gated on a spike):** three-way merge for divergent branches, **never rebase**, since
  rebasing rewrites other users' work and complicates attribution. The spike (roadmap v0.0)
  evaluates go-git v6, or our own text 3-way merge, which is feasible because vault content is
  mostly text. Until the spike passes, we don't promise divergent merges. When they exist, a
  merge commit is **reported** to the user ("merged N remote commits"), never silent.
- Before any pull, dirty eligible files are auto-committed. Files blocked by findings are
  **copied** (never moved) into `.postelmd/recovery/<intent-id>/` and named in the pull's
  intent; they stay in the working tree the whole time.
- **One conflict rule**, the same for a normal pull and for crash recovery: for each path the
  pull changes, if the working-tree bytes equal the pre-blob, write the target blob; if they
  already equal the target blob, leave them. **Otherwise the file is never overwritten**: it
  keeps its bytes, a copy is in recovery, and the path is listed as a conflict (keep mine,
  take theirs, or merge in the UI). HEAD still moves to the target, so the file simply shows
  as modified against the new HEAD.
- A target tree that contains a **reserved path** (ADR-0006), for example `.postelmd-trash/`
  pushed from a misconfigured clone, is refused before anything is written: the vault stays
  `behind` with a clear error.
- Conflicts are never auto-resolved by overwriting.

### Push
- Never force-push.
- A rejected (non-fast-forward) push changes the state to `behind`/`diverged` and prompts
  for a pull. The app doesn't retry a rejected push automatically.
- Auth and network failures retry with backoff (max 1 h between tries). The error is shown in
  the vault status and exported as `postelmd_git_push_total{result}`.

### Crash safety
- Before mutating, every operation writes an **intent record** in SQLite containing:
  - `op`, `vault` and start time;
  - `pre-HEAD`, a hash of the pre-operation index, and the list of paths it will touch, with
    their pre-operation blob hashes;
  - the **planned target** (the commit or tree it intends to produce).

  The record is cleared only on verified completion.
- **HEAD alone is never evidence.** A merge, for example, can write conflict markers, change
  the index and record merge state before HEAD moves. On startup, for every intent record,
  recovery collects **operation-specific evidence**:
  - HEAD, compared with `pre-HEAD` and the planned target;
  - the index, compared with the recorded pre-index hash (including unmerged entries);
  - the working-tree content of the recorded paths, compared with their pre-operation blobs;
  - merge state (`MERGE_HEAD`, conflict markers in the recorded paths).
- Decision rules:
  - **Nothing differs from the pre-state** → the operation didn't start; clear the intent.
  - **Everything matches the planned target** → the operation completed; clear the intent.
  - **Anything in between** → never discard. Copy every touched path's current content into
    `.postelmd/recovery/<intent-id>/` first. Then:
    - a **fast-forward pull** (the only pull in v0.1) has no merge state. It is recovered
      deterministically:
      - Pre-state is always **reconstructible**: a pull requires the index to equal HEAD
        (eligible changes are auto-committed first; blocked files are copied to recovery and
        recorded in the intent). So the pre-state is exactly the `pre-HEAD` tree plus the
        recorded recovery copies.
      - **Finish (default):** apply the pull's conflict rule above to each path in the diff
        between `pre-HEAD` and the target, then set the index and HEAD to the target.
      - **Abort** (user choice, or if the target commit is missing locally): the same, writing
        pre-blobs and setting HEAD and the index to `pre-HEAD`.
      - Any path whose bytes match **neither** blob (a blocked file, or an external write
        during the crash window) follows the same conflict rule: copied to recovery, listed
        as a conflict, never overwritten.
    - a **merge** (post-spike; see the pull policy) goes to `conflicted`, keeping the merge
      state. Before any merge, the intent records the pre-index **as a tree object** written to
      the object database, so an abort can rebuild it exactly (a hash alone can't). An abort
      restores `pre-HEAD`, the recorded index tree, and the working-tree bytes from the
      recovery copy where they were user content;
    - a commit resets the index to HEAD while keeping the working tree (never
      `reset --hard`), and the vault goes `dirty`.
  - The intent is cleared only after the vault reaches a verified state. Recovery copies are
    kept for 7 days.
- Every crash point of every operation type has a fault-injection test in the failure-scenario
  suite (roadmap v0.0).
- Lock files left behind by an external git (`.git/index.lock`) put the vault in
  `unsupported` until the admin confirms.

### Durability
go-git v5.19.3 never calls `fsync`. Loose objects are written to a temp file and renamed
without a sync, and a ref is **truncated and rewritten in place**, so a power loss can leave
an empty branch ref. PostelMD therefore (ADR-0003 §5 gives the general rules):
- opens `.git/` through a billy wrapper in `internal/store/git` that `fsync`s a file when it is
  closed and its directory after a create or rename, so every object is durable before any
  ref points at it;
- **never writes a branch ref through go-git**. `SetBranch` uses git's own lockfile protocol:
  create `<ref>.lock` with `O_CREAT|O_EXCL` (which also excludes a concurrent external git),
  compare the current value with the expected old one (`ErrRefMoved` otherwise), write the new
  value, `fsync` it, rename it over the ref, and `fsync` the directory. The intent names the
  lock file, so recovery removes a `.lock` left by PostelMD's own crash; any other `.lock`
  keeps the rule above;
- orders every operation: objects → ref → clearing the intent in the database.

### go-git capabilities spike results (2026-11-09, go-git v5.19.3, v6.0.0-beta.1)
Implemented in `internal/store/git` (`Commit`, `SetBranch`, `Relation`, `ObjectsToPush`),
tested against the real git binary (`git fsck --strict`).

| Question | Finding | Consequence |
|---|---|---|
| Version | v5.19.3 is the latest stable. v6 is `v6.0.0-beta.1`. | Pin **v5.19.3**; Renovate tracks it. Revisit v6 at its first stable release. |
| Commit from in-memory bytes | Works with plumbing only (blob → trees → commit). The worktree is never read or written; `git fsck --strict` accepts the result. go-git writes tree entries in the order given, so the adapter sorts them the way git does (a directory sorts as `name/`). | The commit gate (ADR-0010) scans exactly the bytes it commits. Auto-commit never uses go-git's index or `Worktree.Add`. |
| Move a ref safely | `CheckAndSetReference` is compare-and-swap, but it rewrites the ref in place without `fsync` (see Durability). | `SetBranch(old, next)` is the only ref mutation. It keeps the compare-and-swap, so a concurrent external git change fails instead of being overwritten, and writes the ref with git's lockfile protocol. |
| Fast-forward pull | `Fetch` + an ancestry check works. `Merge(FastForwardMerge)` moves the ref **and leaves the worktree untouched** (with no worktree attached). | A pull is: fetch → `Relation` → apply the changed paths through `internal/store/fs` under the crash intent above → `SetBranch`. go-git never checks files out. |
| Diverged push | Rejected, but only as an unexported error string (`non-fast-forward update: …`), not a sentinel. | Never infer divergence from the push error (CLAUDE.md rule 14). Check `Relation` against the freshly fetched remote before pushing; treat any push failure as a generic failure to retry. |
| Objects a push sends | `revlist.Objects(want, have)` lists exactly the new commits, trees and blobs. | The push gate scans that list (ADR-0010). |
| Three-way merge | **v6 beta still rejects every strategy except fast-forward** (it declares ORT options, but `Merge` returns `ErrUnsupportedMergeStrategy`). | v0.1 stays fast-forward only, as decided above. Divergent merges need our own text three-way merge: a separate design issue, not a v0.1 promise. |
| `billy.Filesystem` adapter | go-git needs a filesystem only for `.git/` once it never touches the worktree. go-billy's default `osfs` chroot refuses symlinks leading out of `.git/` (tested), but resolves them in user space, so it isn't race-proof like `internal/store/fs`. | Accepted for `.git/` only: it is a reserved path app users can't write (ADR-0006), so only an external writer with shell access could race it. A billy adapter over `internal/store/fs` stays possible later; no worktree adapter is needed. |
| Transports | The transport for a URL scheme is chosen **process-wide** (`client.InstallProtocol`). Plain local paths and `file://` are valid remotes, served in-process. | At startup, install the egress client (architecture §11) as the `http`/`https` transport and uninstall `file`. Validate every remote URL against a scheme allow-list (`https`, and `ssh` when supported) **before** storing it. Otherwise a vault whose remote is `/data/<other-vault>` could read another vault. |
| Dependency footprint | go-git adds about 20 modules (ssh, OpenPGP, sha1cd, go-diff…). `govulncheck` finds no called vulnerabilities; the module-level `x/crypto/openpgp` advisory isn't reachable from our code. | Accepted under ADR-0001 (native git for the audience's features). `make vuln` gates it on every PR. |

### Attribution
- Auto-commits are **per author**: the auto-committer groups changed files by the last
  PostelMD user who saved each file, and creates one commit per author in the batch.
  - Each per-author commit is **its own operation with its own intent** (its paths, their
    blob hashes, `pre-HEAD` and the planned commit), cleared when its ref update is durable.
    A crash leaves at most one open intent; recovery handles it as any commit, and the authors
    not yet committed are still dirty, so the next batch picks them up. The last saver of
    each file is recorded in the database with the save, so attribution survives a crash.
  - Author: the user's name and verified email (or `user@<instance>` if none).
  - Committer: `PostelMD <postelmd@<instance>>`.
- A file edited by several users inside one quiet window is committed under its last editor,
  with `Co-authored-by:` trailers for the other editors.
- Changes from **external writers** (detected by the reconciler, no PostelMD user) are
  committed separately as `External change (detected by PostelMD)`, authored as the vault's
  configured "external author" (default: the vault owner), and flagged in history.

## Consequences
- Positive: every state is defined and visible; crashes are recoverable without data loss;
  history attributes changes correctly.
- Negative: some legitimate git setups (submodules, LFS) are read-only in PostelMD. That's
  documented.

## Alternatives considered
| Option | Why not |
|---|---|
| Rebase on pull | Rewrites shared history; worse attribution |
| One commit per save | Noisy history; hurts performance |
| Single committer identity for everyone | Loses who changed what (repudiation) |
| go-git `Worktree` (checkout, `Add`, `Commit`) | It reads and writes the worktree itself, outside `internal/store/fs` confinement and the crash intents, and stages whatever is on disk at that moment rather than the scanned bytes |
| Shelling out to the `git` binary | Not in the distroless image (ADR-0009); ties behaviour to the installed git version |
