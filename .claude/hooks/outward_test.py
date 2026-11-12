"""Tests for the routine-work gating in postelmd_guard.py (git mode) and outward.py.

The hook judges each command end to end in throwaway repositories; nothing is pushed.
Run: python3 -m unittest discover -s .claude/hooks -p '*_test.py'  (make test-scripts)
"""
import os
import shutil
import tempfile
import unittest

from postelmd_guard_test import git, new_repo, run_guard

SHA = "0123456789abcdef0123456789abcdef01234567"


class Gating(unittest.TestCase):
    """feat: a repository on branch `feature` (with a tag v1 and a branch `tracks-main` whose
    upstream is origin/main); onmain: one on `main`. The hook runs in feat unless cd/-C moves."""

    @classmethod
    def setUpClass(cls):
        cls.feat, cls.onmain = new_repo(), new_repo()
        for repo in (cls.feat, cls.onmain):
            git(repo, "commit", "-q", "--allow-empty", "-s", "-m", "chore: init")
            git(repo, "remote", "add", "origin", "https://example.com/postelmd.git")
            git(repo, "update-ref", "refs/remotes/origin/main", "HEAD")
        git(cls.feat, "-c", "tag.gpgsign=false", "tag", "v1")
        git(cls.feat, "branch", "tracks-main", "origin/main")
        git(cls.feat, "switch", "-q", "-c", "feature")
        cls.detached = new_repo()
        git(cls.detached, "commit", "-q", "--allow-empty", "-s", "-m", "chore: init")
        git(cls.detached, "switch", "-q", "--detach", "HEAD")

    @classmethod
    def tearDownClass(cls):
        for repo in (cls.feat, cls.onmain, cls.detached):
            shutil.rmtree(repo)

    def decide(self, command, cwd=None):
        got = run_guard("git", {"cwd": cwd or self.feat, "tool_input": {"command": command}},
                        self.feat)
        return got and got["permissionDecision"]

    def assert_cases(self, cases, want):
        for command in cases:
            with self.subTest(command=command):
                self.assertEqual(self.decide(command.format(f=self.feat, m=self.onmain)), want)

    def test_branch_pushes_pass(self):
        self.assert_cases([
            "git push",
            "git push -u origin feature",
            "git push --set-upstream origin HEAD",
            "git push origin HEAD:feature",
            "git push origin feature:refs/heads/feature-2",
            "git push origin refs/heads/feature",
            "git push --force-with-lease",
            "git push --force-with-lease=feature:abc123 --force-if-includes origin feature",
            "git push -uq origin feature 2>&1 | tail -5",
            "git -C {f} push -u origin feature",
            "cd {f} && git push",
            "make test && git push -u origin feature && gh pr create --fill",
            "git --no-pager push origin feature",
        ], None)
        self.assertIsNone(self.decide("cd {f} && git push".format(f=self.feat), cwd=self.onmain))
        self.assertIsNone(self.decide(f"git -C {self.feat} push", cwd=self.onmain))

    def test_protected_or_risky_pushes_ask(self):
        self.assert_cases([
            "git push origin main",
            "git push origin master",
            "git push origin feature:main",
            "git push origin HEAD:refs/heads/main",
            "git -C {m} push",
            "cd {m} && git push",
            "cd {m}; git push -u origin HEAD",
            "git push -f",
            "git push --force origin feature",
            "git push -uf origin feature",
            "git push origin +feature",
            "git push --mirror",
            "git push --all origin",
            "git push --tags",
            "git push --follow-tags origin feature",
            "git push --delete origin feature",
            "git push -d origin feature",
            "git push origin :feature",
            "git push upstream feature",
            "git push https://example.com/other.git feature",
            "git push origin v1",
            "git push origin refs/tags/v1",
            "git push origin 'feature:refs/tags/x'",
            "git push origin 'refs/heads/*:refs/heads/*'",
            "git push --repo=other origin feature",
            "git push -o ci.skip origin feature",
            "git -c remote.origin.url=x push origin feature",
            "git --git-dir=/elsewhere/.git push origin feature",
            "GIT_DIR=/elsewhere/.git git push origin feature",
            "echo feature | xargs git push origin",
            "env -i git push origin main",
            "sudo -u someone git push origin main",
            "/usr/bin/git push origin main",
            "git switch tracks-main && git push",  # the branch is read before the switch runs
            "cd - && git push",
            "cd $OTHER && git push",
            "(cd {f}) && git push",
            "git status; git push origin main",
            "git push -u origin feature && git push origin main",
            "git -C \"{m}\" push",
            "git push origin feature 'unbalanced",            # the parser gives up: ask
            "echo \"x\ngit push origin main\"",               # seen by the regex, not parsed
        ], "ask")
        for repo in (self.onmain, self.detached):
            with self.subTest(cwd=repo):
                self.assertEqual(self.decide("git push", cwd=repo), "ask")

    def test_branch_tracking_main_asks(self):
        git(self.feat, "switch", "-q", "tracks-main")
        self.addCleanup(git, self.feat, "switch", "-q", "feature")
        self.assertEqual(self.decide("git push"), "ask")
        self.assertEqual(self.decide("git push origin"), "ask")

    def test_pr_and_issue_work_passes(self):
        self.assert_cases([
            "gh pr create --fill --label skip-changelog",
            "gh pr ready 5",
            "gh pr edit 5 --title 'fix: x'",
            "gh pr comment 5 --body 'Fixed in abc.'",
            "gh issue create -t 'x' -b 'y'",
            "gh issue comment 3 -b 'x'",
            "gh issue edit 3 --add-label bug",
            "gh issue close 3 -c 'Fixed by #5'",
            "gh run rerun 123 --failed",
            f"gh pr merge 5 --squash --match-head-commit {SHA}",
            f"gh pr merge 5 -s --delete-branch --match-head-commit={SHA}",
            "gh pr view 5 -R cli/cli",
            "gh pr checks 5 && gh run list --limit 3",
        ], None)

    def test_other_github_actions_ask(self):
        self.assert_cases([
            "gh pr close 5",
            "gh pr reopen 5",
            "gh pr review 5 --approve",
            "gh issue delete 3",
            "gh issue transfer 3 other/repo",
            "gh issue reopen 3",
            "gh pr merge 5",
            "gh pr merge 5 --squash",
            f"gh pr merge 5 --match-head-commit {SHA}",
            "gh pr merge 5 --squash --match-head-commit abc123",
            f"gh pr merge 5 --squash --match-head-commit {SHA.upper()}",
            f"gh pr merge 5 --merge --squash --match-head-commit {SHA}",
            f"gh pr merge 5 --squash --admin --match-head-commit {SHA}",
            f"gh pr merge 5 --squash -R other/repo --match-head-commit {SHA}",
            "gh pr merge 5 --squash --match-head-commit $(git rev-parse HEAD)",
            f"gh pr create --fill && gh pr merge 5 --squash",
            f"gh pr merge 5 --squash --match-head-commit {SHA} && gh pr merge 6 --squash",
            "gh pr comment 5 -R other/repo -b x",
            "gh issue create --repo other/repo -t x",
            "gh run rerun 123 --repo other/repo",
            "gh run cancel 123",
            "gh run delete 123",
            "gh workflow run ci.yml",
            "gh workflow disable ci.yml",
            "git tag v1.0.0",
            "gh release create v1.0.0",
            "gh repo edit --visibility private",
            "gh secret set X",
            "gh api -X POST repos/o/r/issues",
            "gh api repos/o/r/labels -f name=x",
        ], "ask")

    def test_denials_come_before_asks(self):
        self.assert_cases([
            "git commit -m 'chore: x' && git push origin main",  # no sign-off
            "git commit -s -m 'update stuff' && git push",        # not conventional
            "git push --no-verify",
        ], "deny")
        self.assertIsNone(self.decide("git commit -s -m 'chore: x' && git push"))


class NotAGitDirectory(unittest.TestCase):
    def test_push_outside_a_repository_asks(self):
        d = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, d)
        got = run_guard("git", {"cwd": d, "tool_input": {"command": "git push"}}, d)
        self.assertEqual(got["permissionDecision"], "ask")
        self.assertTrue(os.path.isdir(d))


if __name__ == "__main__":
    unittest.main()
