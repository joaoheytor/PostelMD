"""Regression tests for the guard bypasses found in the security review of #169.

Each case passed the hook silently before its fix. The hook judges, never runs, them in a
throwaway repository on branch `feature` (with branches x and main, and a tag v0.1.0).
Run: python3 -m unittest discover -s .claude/hooks -p '*_test.py'  (make test-scripts)
"""
import os
import shutil
import tempfile
import unittest

from postelmd_guard_test import git, new_repo, run_guard

SHA = "0123456789abcdef0123456789abcdef01234567"
FAKE_KEY = "AKIA" + "Q7" * 8  # built at run time: not a secret in this file
HOME = "/" + "home" + "/someone/notes.md"


class Bypasses(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.repo = new_repo()
        git(cls.repo, "commit", "-q", "--allow-empty", "-s", "-m", "chore: init")
        git(cls.repo, "remote", "add", "origin", "https://example.com/postelmd.git")
        git(cls.repo, "-c", "tag.gpgsign=false", "tag", "v0.1.0")
        git(cls.repo, "branch", "x")
        git(cls.repo, "switch", "-q", "-c", "feature")
        git(cls.repo, "update-ref", "refs/remotes/origin/feature", "HEAD")
        cls.files = tempfile.mkdtemp()
        for name, text in (("secret.md", f"key {FAKE_KEY}\n"), ("ip.md", "host 10.1.2.3\n"),
                           ("clean.md", "## What\nA fix.\n")):
            with open(os.path.join(cls.files, name), "w", encoding="utf-8") as fh:
                fh.write(text)

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.repo)
        shutil.rmtree(cls.files)

    def decide(self, command):
        got = run_guard("git", {"cwd": self.repo, "tool_input": {"command": command}}, self.repo)
        return got and got["permissionDecision"]

    def assert_cases(self, cases, want):
        for command in cases:
            command = command.format(f=self.files, sha=SHA, key=FAKE_KEY, home=HOME)
            with self.subTest(command=command):
                self.assertEqual(self.decide(command), want)

    def test_gh_api_mutations_ask(self):
        self.assert_cases([
            "gh api -X 'DELETE' repos/o/r/git/refs/heads/x",
            "gh api -X\"DELETE\" repos/o/r/git/refs/heads/x",
            "gh api --method \"PUT\" repos/o/r/topics",
            "gh api --method=post repos/o/r/issues",
            "gh api -fstate=approved repos/o/r/pulls/1/reviews",
            "gh api graphql -fquery='mutation {{ x }}'",
            "gh api graphql",
            "gh api --input body.json repos/o/r/issues",
            "gh api -H 'X-HTTP-Method-Override: DELETE' repos/o/r",
        ], "ask")

    def test_comment_char_asks(self):
        self.assert_cases(["git push origin x#y feature:refs/tags/v9.9.9"], "ask")

    def test_refspecs_ask(self):
        self.assert_cases([
            "git push origin feature:heads/main",
            "git push --force-with-lease=refs/tags/v0.1.0:{sha} origin feature:tags/v0.1.0",
            "git push origin feature:HEAD",
            "git push origin feature:remotes/origin/main",
            "git push origin HEAD:v0.1.0",
            "git push origin feature:x",
            "git push origin feature feature",
            "git push --force-with-lease=main origin feature",
            "git push --force-with-lease=x:{sha} origin feature",
            "git push origin Main",
        ], "ask")

    def test_merge_flags_ask(self):
        self.assert_cases([
            "gh pr merge 5 --squash --admin=true --match-head-commit {sha}",
            "gh pr merge 5 --squash --repo=o/r --match-head-commit {sha}",
            "gh pr merge 5 --squash -Ro/r --match-head-commit {sha}",
            "gh pr merge 5 --squash --body x --match-head-commit {sha}",
        ], "ask")

    def test_code_execution_asks(self):
        self.assert_cases([
            "git rebase origin/main -x 'make x'",
            "git rebase --exec=true origin/main",
            "git rebase -ix true origin/main",
            "go test -exec ./evil ./...",
            "go build -toolexec=./evil ./...",
            "go vet --toolexec ./evil ./...",
            "GOFLAGS=-toolexec=./evil go test ./...",
            "scripts/../../tmp/evil.sh",
            "./scripts/../x.sh",
            "git diff --output=CLAUDE.md",
            "git log --output CLAUDE.md",
            "git show --output=x HEAD",
        ], "ask")

    def test_pr_text_is_scanned(self):
        self.assert_cases([
            "gh pr create --title 'fix: x' --body 'key {key}'",
            "gh issue comment 3 --body-file {f}/secret.md",
            "gh pr edit 5 -F {f}/secret.md",
        ], "deny")
        self.assert_cases([
            "gh pr comment 5 -b 'see {home}'",
            "gh issue create -t x -F {f}/ip.md",
            "gh pr edit 5 --body-file -",
            "gh pr create --title x --body-file {f}/missing.md",
            "gh pr create --title x --body \"$(cat {f}/clean.md)\"",
        ], "ask")

    def test_outward_commands_must_stand_alone(self):
        self.assert_cases([
            "git push origin feature; echo ok",
            "true && git push -u origin feature",
            "git push -u origin feature | cat",
            "git push -u origin feature 2>/dev/null",
            "gh pr merge 5 --squash --match-head-commit {sha} && echo merged",
            "gh pr create --fill && echo done",
            "gh api repos/o/r/pulls | jq .",
        ], "ask")

    def test_rereview_bypasses_ask(self):
        """Still silent at 68929b7 (re-review of #170)."""
        self.assert_cases([
            # fetch, pull and grep running commands
            "git fetch --upload-pack='touch x; git-upload-pack' .",
            "git fetch --upload-pack 'touch x' origin",
            "git fetch -u'touch x' origin",
            "git pull --ff-only --upload-pack='sh -c x' .",
            "git pull --ff-only --exec='sh -c x' origin",
            "git fetch --receive-pack=x origin",
            "git fetch other",
            "git fetch origin main:main",
            "git grep -O'sh -c x' pat",
            "git grep -iO pat",
            "git grep --open-files-in-pager=vi pat",
            # a remote-only tag overwritten through HEAD:<name>
            "git push --force-with-lease=v0.2.0:{sha} origin HEAD:v0.2.0",
            "git push origin HEAD:v0.2.0",
            "git push origin HEAD:other-name",
            "git push --force-with-lease=x:{sha} origin x",  # no refs/remotes/origin/x
            "git push origin V1",
            # build output over a git hook
            "go build -o .githooks/pre-commit ./x",
            "go test -c -o .githooks/commit-msg ./x",
            "go build -o=.git/hooks/pre-push ./x",
            "go build -o bin/../.githooks/pre-commit ./x",
        ], "ask")
        self.assert_cases([
            "git fetch",
            "git fetch origin",
            "git fetch -q --prune origin main",
            "git pull --ff-only",
            "git pull --ff-only origin main",
            "git grep -n pat",
            "go build -o bin/postelmd ./cmd/postelmd",
            "go test -c -o /tmp/x.test ./x",
            "git push origin HEAD:feature",
        ], None)

    def test_routine_forms_still_pass(self):
        self.assert_cases([
            "git push -u origin feature",
            "git push origin HEAD",
            "git push origin feature:feature",
            "git push origin refs/heads/feature",
            "git push --force-with-lease origin feature",
            "git push --force-with-lease=feature origin feature",
            "git push --force-with-lease=refs/heads/feature:{sha} origin HEAD:feature",
            "gh pr merge 5 --squash --match-head-commit {sha}",
            "gh pr comment 5 --body 'Fixed in the next commit.'",
            "gh pr create --title 'fix: x' --body-file {f}/clean.md",
            "gh api repos/o/r/pulls --jq '.[].number'",
            "go test -race ./...",
            "git rebase origin/main",
            "git diff --stat origin/main",
            "scripts/check-file-size.sh",
        ], None)


if __name__ == "__main__":
    unittest.main()
