---
name: pr-slice
description: Use when implementing one PostelMD change as a pull request — a feature, fix, test or docs slice — from branch to merged, including when several slices run in parallel in worktrees. Also when the user says "open a PR for this", "ship this slice" or "clean up after the merge".
---

# PR slice

One small vertical slice per PR (CLAUDE.md, Workflow): code, tests, docs and, if needed, an
ADR. Read `CLAUDE.md` first; its hard rules and Definition of Done apply to every step.
**Never** push to `main`, create tags or releases, or change repository settings or secrets
without the maintainer's explicit go-ahead in the conversation.

## Steps

1. **Branch from `origin/main`:**
   `git fetch origin && git switch -c <type>/<short-name> origin/main`.
   When slices run in parallel, each runs in its own worktree under `.claude/worktrees/`
   (CLAUDE.md, Worktrees and parallel agents), and exports its own isolation variables
   before any image or e2e target:
   `export IMAGE=postelmd:<slice> PM_E2E_PORT=<port> SMOKE_PORT=<port>`, then
   `make ci IMAGE=postelmd:<slice>`. Run `cd web && npm ci` once if `web/node_modules` is
   missing. Save, sync, git or recovery code (rules 4 and 14), and anything that depends on
   another open slice, stays sequential.
2. **Tests first.** For a bug, start with a test that fails for the reported reason. Follow
   CLAUDE.md's Testing conventions (real temp vaults, role-based locators, fake secrets built
   at run time, servers cleaned up on every exit path).
3. **Implement** the slice. Write repository files with Write/Edit, not shell redirection,
   so the guard hook sees them.
4. **Negative check.** For each new test: break or disable the behaviour it covers, run the
   test and see it fail for the right reason, then restore the code. Note what you broke for
   the PR body.
5. **Docs, ADR, changelog** as needed:
   - design change, new dependency or trust boundary → the `new-adr` skill;
   - user-visible change → the `changelog` skill; otherwise the PR gets the
     `skip-changelog` label;
   - update `docs/` and the threat model where the change touches them.
6. **`make ci` green** (with the isolation variables when in a worktree). Fix failures; never
   skip hooks (`--no-verify`) or quarantine a test.
7. **Commit**, signed off, with a Conventional Commit subject:
   `git commit -s` and end the message with the trailer line
   `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
8. **Push and open the PR** (no prompts for a non-main branch; the guard hook still asks
   for pushes to main, force pushes without a lease and any other remote):
   `git push -u origin <branch>`, then `gh pr create` with
   - a Conventional Commit title (`feat(editor): …`, `fix!: …`);
   - a body with **What** (the change and why), **Tests** (what they cover, and the
     negative check: what you broke and that the test failed), and anything found along the
     way (bugs, gaps, follow-up issues);
   - the body's last line: `🤖 Generated with [Claude Code](https://claude.com/claude-code)`;
   - `--label skip-changelog` when nothing user-visible changed.
9. **Watch CI:** `gh pr checks <n>` (after a push, use the wait loop in the `pr-review`
   skill: `--watch` can exit before the new checks are queued), and list open code-scanning alerts on the PR:
   `gh api "repos/{owner}/{repo}/code-scanning/alerts?ref=refs/pull/<n>/merge&state=open"`.
   Fix what they find in the same PR.
10. **Review:** the `pr-review` skill picks the persona reviewers (`.claude/agents/`), runs
    them alongside CI, and decides "ready to merge".
11. **After the maintainer says it's merged** (squash merge):
    - `git fetch origin`, and find the squash commit:
      `gh pr view <n> --json mergeCommit -q .mergeCommit.oid`.
    - Check the branch matches it: `git diff --quiet <branch> <squash-commit>`. If later PRs
      merged in between and that is inconvenient, compare patches instead:
      `git diff origin/main...<branch> | git patch-id --stable` against
      `git show <squash-commit> | git patch-id --stable`. If they differ, stop and report
      what the branch has that the squash commit doesn't.
    - Then clean up: remove the worktree if one was used
      (`git worktree remove .claude/worktrees/<name>`), delete the local branch
      (`git branch -D <branch>`; the squash merge makes `-d` refuse), and delete the remote
      branch if GitHub didn't (`git ls-remote --heads origin <branch>` non-empty →
      `git push origin --delete <branch>`, a permission prompt).
