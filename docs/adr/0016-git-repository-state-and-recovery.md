# ADR-0016: Git repository state model and recovery

- **Status:** Proposed (in v0.1)
- **Date:** 2026-11-09

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
  stashed by PostelMD into `.postelmd/recovery/` (not `git stash`) and restored after the pull.
  A restore that would overwrite a changed file becomes a conflict.
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
        (eligible changes are auto-committed first; blocked files are moved to recovery and
        recorded in the intent). So the pre-state is exactly the `pre-HEAD` tree plus the
        recorded recovery files.
      - **Finish (default):** for each path in the diff between `pre-HEAD` and the target, if
        the working-tree bytes equal the pre-blob or the target blob, write the target blob.
        Then set the index and HEAD to the target and restore the recovery files.
      - **Abort** (user choice, or if the target commit is missing locally): the same, writing
        pre-blobs and setting HEAD and the index to `pre-HEAD`.
      - Any path whose bytes match **neither** blob (an external write during the crash
        window) is copied to recovery and listed as a conflict. It is never overwritten.
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

### Attribution
- Auto-commits are **per author**: the auto-committer groups changed files by the last
  PostelMD user who saved each file, and creates one commit per author in the batch.
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
