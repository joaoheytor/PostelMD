<!--
PR title = a Conventional Commit, e.g. `feat(editor): add jump-to-line` (it becomes the squash
commit). Every commit needs a DCO sign-off (`git commit -s`). This repository is public: no
secrets, tokens, private hostnames or IPs anywhere in the diff.
-->

## What and why

<!-- What changes, and the problem it solves. Link the issue: "Closes #123". -->

## How it was tested

<!-- Commands run and their results; for UI changes, the Playwright smoke tests. -->

## Definition of Done (CLAUDE.md)

- [ ] Tests added and passing: `make ci` (Go, web, failure scenarios), plus Playwright smoke tests if the UI changed
- [ ] New recovery paths have a scenario test for each crash point
- [ ] Lint clean: `golangci-lint`, `eslint`, file-size check
- [ ] ADR added or updated if the design changed; threat model updated if a trust boundary changed
- [ ] Docs updated; `CHANGELOG.md` `[Unreleased]` entry for user-visible changes (otherwise the `skip-changelog` label)
- [ ] No dead code, no TODO without an issue link

<!-- Not applicable? Tick it and add "(n/a)". -->
