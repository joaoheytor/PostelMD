# ADR-0026: Nested repositories in the vault are left out of commits

- **Status:** Accepted
- **Date:** 2026-11-10

## Context
Supersedes, for folders that are git repositories of their own, the commit bullet of
[ADR-0020](0020-mvp-scope.md) ("A commit, and the initial import, take what `git add -A`
would"). The rest of that bullet (ignore rules, tracked files, `info/exclude`) stands.

A notes folder can hold cloned repositories and submodules: a folder with a `.git` folder
(a clone) or a `.git` file (a submodule's checkout). `git add -A` records such a folder as
one **gitlink** (mode `160000`, the inner repository's `HEAD` commit), never the files in
it. PostelMD's commit walk didn't know that. It descended into the folder and listed the
inner repository's files as added:
- if the vault's repository already recorded the folder as a gitlink (a submodule, or a
  clone added on the host), every commit failed with `file/directory conflict: clone is a
  file` and a `500`;
- otherwise the initial import committed the inner repository's files as ordinary files,
  a copy of another project's tree in the notes' history.

Recording new gitlinks, or moving one when the inner `HEAD` moves, is a change to another
repository's pointer that the user didn't ask for in the commit dialog, and the dialog has
no way to show it yet.

## Decision
- A commit, and the initial import, take what `git add -A` would, **except for nested
  repositories**: a folder, at any depth below the vault root, that holds an entry named
  `.git` (a folder, a file or anything else, in any letter case) is never descended into.
  - It is not listed as a change. Status and the initial import name it under
    **`repositories`** (next to `skipped`, the files over the size limit), so the user
    can see what was left out; an ignored one isn't named.
  - A gitlink the branch already has is kept exactly as it is: PostelMD never adds,
    moves or removes a gitlink. The user updates it with their own git on the host.
- The API never reaches any `.git` path component, at any depth and in any letter case
  ([ADR-0006](0006-authorization-and-confinement.md), `store.IsReserved`): a nested
  repository's `.git/config` or `.git/hooks/*` can't be listed, read, written, moved or
  deleted through PostelMD, so the web app can't plant a hook that runs when the user next
  runs git in that clone. Its other files are notes like any other.

## Consequences
- Positive: commits next to a submodule or clone work, the notes' history never holds a
  copy of another repository, and the host's `git status` is unchanged by a PostelMD
  commit.
- Negative / costs: a new nested repository isn't recorded in the vault's repository until
  the user adds it on the host. Files the branch already tracks inside a folder that later
  became a repository are listed as deleted (they are still on disk).
- Follow-ups: the commit dialog and the setup notice show `repositories` (web app); recording
  gitlinks may come with v0.2's commit gate.

## Alternatives considered
| Option | Why not |
|---|---|
| Record gitlinks as `git add -A` does | Changes another repository's pointer without the user seeing it in the dialog |
| Commit the inner files | Copies another project into the notes' history; fails outright next to an existing gitlink |
| Refuse to start when the vault holds a repository | The maintainer's notes folder will hold clones by design |
