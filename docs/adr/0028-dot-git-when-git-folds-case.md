# ADR-0028: A nested repository's .git in any letter case when git folds case

- **Status:** Accepted
- **Date:** 2026-11-12

## Context
Supersedes the nested-repository match of [ADR-0026](0026-nested-repositories-in-the-vault.md):
"a folder … that holds an entry named exactly `.git` (a folder, a file or anything else; the
name is case-sensitive, as git on Linux reads it, so `.GIT` doesn't make a repository)". The
rest of ADR-0026 stands.

Git finds a nested repository by opening `<folder>/.git`. On a case-sensitive filesystem that
is the exact name, but on a case-insensitive one (ext4 with casefold, a case-insensitive
mount, a volume shared from macOS or Windows) the same lookup finds `.GIT` or `.Git`. Git
records which kind of filesystem a repository lives on in `core.ignorecase` when it creates
it. Matching only the exact name there, the commit walk descends into a folder that git
treats as a repository and commits its notes into the vault's history, the copy ADR-0026
exists to prevent. Found in the re-review of #116, recorded on
[#119](https://github.com/joaoheytor/PostelMD/issues/119).

## Decision
- A commit, and the initial import, never descend into a nested repository: a folder, at any
  depth below the vault root, that holds an entry (a folder, a file or anything else) named
  `.git`, compared
  - exactly when the vault's repository has `core.ignorecase` false or unset, as git reads
    it on a case-sensitive filesystem (`.GIT` doesn't make a repository there);
  - in any letter case, with default-ignorable code points dropped, when `core.ignorecase`
    is true: casefold on older Linux kernels ignored those code points, so a lookup of
    `.git` could find `.G<U+200B>it`.
- The other names git treats as `.git` on Windows (`.git ` with trailing spaces or dots,
  `git~1`) never make a nested repository: Linux keeps those characters, so the lookup
  doesn't find them. They, and `.git` with ignorable code points in any case, are reserved
  ([ADR-0006](0006-authorization-and-confinement.md), "Reserved paths"), so they are never
  committed either.
- **`core.ignorecase` in vaults PostelMD creates.** When enabling history creates the
  repository, it probes the filesystem as `git init` does: it creates a file in the new
  `.git` being built (inside the vault's temp folder), looks it up in another letter case,
  and removes it. If the lookup finds it, the config gets `ignorecase = true`. A probe that
  fails fails the init; it is never a guess.
  - Limits: a repository PostelMD created before this decision, or one adopted from the
    host, keeps the `core.ignorecase` it has (git sets it on the host's own `git init`). A
    vault moved to another filesystem keeps the old value, as with git. In both cases the
    user sets the option with their own git; PostelMD never rewrites it.

## Consequences
- Positive: on a case-insensitive filesystem, PostelMD's commit leaves out the same folders
  git would, and the history never holds a copy of another repository there.
- Negative / costs: if a user sets `core.ignorecase` on a case-sensitive filesystem, a folder
  holding `.GIT` is left out of commits and named under `repositories`; the user can see it
  and unset the option. Enabling history writes and removes one empty probe file, inside
  the temp folder the startup sweep already cleans.
- Follow-ups: none.

## Alternatives considered
| Option | Why not |
|---|---|
| Keep the exact match | On a case-insensitive filesystem, commits copy a nested repository's files, as above |
| Match in any letter case always | On a case-sensitive filesystem a `.GIT` folder isn't a repository for git, and its siblings would silently drop out of commits |
| Probe the filesystem for case folding at each commit | One more write in the vault per commit, and git has already recorded the answer in `core.ignorecase` (or PostelMD has, when it created the repository) |
| Rewrite `core.ignorecase` in existing repositories | Changes the user's own git configuration behind their back, and git's ignore matching with it |
