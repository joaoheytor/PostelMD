# Contributing to PostelMD

Thanks for your interest! PostelMD is pre-alpha, and the design lives in [`docs/`](docs/).

## Before you start

1. Read [CLAUDE.md](CLAUDE.md): the conventions apply to humans and AI agents alike.
2. Check the [PRD Non-Goals](docs/PRD.md#4-non-goals). Proposals that conflict with them need an
   ADR first.
3. For anything non-trivial, open an issue to discuss it before writing code.

## Developer Certificate of Origin (DCO)

Every commit must be signed off, certifying the [DCO](https://developercertificate.org/):

```sh
git commit -s -m "feat(editor): add jump-to-line"
```

This appends `Signed-off-by: Your Name <you@example.com>`. PRs without sign-off cannot be
merged. There is no CLA.

## Local git hooks (recommended)

The repository ships git hooks that catch problems before CI does. Enable them once per clone:

```sh
git config core.hooksPath .githooks
```

- `commit-msg`: Conventional Commit subject and DCO sign-off.
- `pre-commit`, on the **staged** content:
  - secret scan with [gitleaks](https://github.com/gitleaks/gitleaks), if installed (CI
    always scans);
  - no absolute home-directory paths, and no terms from your own optional
    `.claude/public-guard.local.txt` (git-ignored: list hostnames or names you never want
    published);
  - the 500-line limit for source files.

CI enforces the same rules, so the hooks only make failures faster. Don't bypass them with
`--no-verify`; fix the cause instead.

## Pull requests

- One focused change per PR. Keep diffs small.
- Follow the [Definition of Done](CLAUDE.md#definition-of-done): tests, lint, docs, and an ADR
  if the design changes.
- Bug fixes include a test that fails without the fix.
- PRs are squash-merged, so the **PR title** must be a
  [Conventional Commit](https://www.conventionalcommits.org/) (`feat`, `fix`, `perf`,
  `refactor`, `docs`, `test`, `build`, `ci`, `chore`, `revert`; add `!` for breaking changes),
  for example `feat(editor): add jump-to-line`. CI checks it.
- User-visible changes (`feat`, `fix`, `perf`, security, breaking) add a line under
  `## [Unreleased]` in [CHANGELOG.md](CHANGELOG.md), in the right Keep a Changelog section
  (`Added`, `Changed`, `Deprecated`, `Removed`, `Fixed`, `Security`).
- If you used AI tools, you are still responsible for understanding and testing every line.

## Reporting bugs

Please include:
- what you expected and what happened;
- exact error messages;
- steps to reproduce;
- version and install method;
- relevant logs, with secrets redacted.

"It stopped working" reports cannot be acted on.

## Security issues

Do not open public issues. See [SECURITY.md](SECURITY.md).

## Code of Conduct

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md).
