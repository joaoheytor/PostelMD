---
name: changelog
description: Use when a PostelMD change is user-visible (feat, fix, perf, security, breaking, deprecation) and needs a CHANGELOG.md entry, when preparing a release section, or when the user asks to "update the changelog". Not for docs-only, test-only, refactor or CI changes, which use the skip-changelog label instead.
---

# Changelog entry

`CHANGELOG.md` follows [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/) and is
the source of the GitHub Release notes (`docs/ci-cd.md` §5). It is human-curated: commit
messages help, but entries are written for users, not developers.

## Add an entry (every user-visible PR)

1. Decide the section from the change, not the commit type alone:
   | Change | Section |
   |---|---|
   | New feature or capability | `Added` |
   | Behaviour change of something existing | `Changed` |
   | Will be removed later | `Deprecated` |
   | Removed | `Removed` |
   | Bug fix | `Fixed` |
   | Vulnerability fix or security hardening | `Security` |
2. Add one bullet under `## [Unreleased]` → `### <Section>` (create the heading if missing,
   in the order above). Style:
   - what changed **for the user**, in the past tense or as a noun phrase, one line if possible;
   - breaking changes start with `**Breaking:**` and say what users must do;
   - link the issue or PR as `(#123)` when one exists;
   - no internal file paths, no private details; this text is published.
3. If the change isn't user-visible, don't add an entry: say the PR needs the
   `skip-changelog` label.

## Prepare a release section (only when the maintainer asks)

1. Work out the version with SemVer and the pre-1.0 rule in `GOVERNANCE.md` (before 1.0:
   breaking → minor, everything else → patch).
2. Rename `## [Unreleased]` content into `## [X.Y.Z] - YYYY-MM-DD`, leave an empty
   `## [Unreleased]` above it, put `Security` and breaking entries first, and update the
   compare links at the bottom (`[Unreleased]: …/compare/vX.Y.Z...HEAD`).
3. Never create the tag or release: that is the maintainer's step (CLAUDE.md workflow).
