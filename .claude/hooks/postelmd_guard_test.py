"""Tests for postelmd_guard.py and bash_writes.py, on throwaway git repositories.

Run: python3 -m unittest discover -s .claude/hooks -p '*_test.py'  (make test-scripts)
"""
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import bash_writes  # noqa: E402

GUARD = os.path.join(HERE, "postelmd_guard.py")


def run_guard(mode, payload, project):
    env = dict(os.environ, CLAUDE_PROJECT_DIR=project)
    out = subprocess.run([sys.executable, GUARD, mode], input=json.dumps(payload),
                         capture_output=True, text=True, env=env, check=True,
                         timeout=30).stdout  # a hook that blocks (a FIFO) fails, not hangs
    return json.loads(out)["hookSpecificOutput"] if out.strip() else None


def git(repo, *args):
    subprocess.run(["git", "-C", repo, *args], check=True, capture_output=True)


def new_repo():
    repo = tempfile.mkdtemp()
    git(repo, "init", "-q", "-b", "main")
    for k, v in (("user.name", "Test"), ("user.email", "test@example.invalid"),
                 ("commit.gpgsign", "false")):
        git(repo, "config", k, v)
    return repo


class WriteTargets(unittest.TestCase):
    CASES = [
        ("sed -i 's/a/b/' CLAUDE.md", ["CLAUDE.md"]),
        ("sed --in-place -e 's/a/b/' a.go b.go", ["a.go", "b.go"]),
        ("sed -i.bak -E 's/a/b/' x.md", ["x.md"]),
        ("sed -n 1,5p CLAUDE.md", []),
        ("perl -pi -e 's/a/b/' x.go", ["x.go"]),
        ("perl -ne 'print' x.go", []),
        ("echo hi > notes.md", ["notes.md"]),
        ("echo hi >>notes.md", ["notes.md"]),
        ("make lint 2>&1 | tail", []),
        ("git status >/dev/null 2>&1", ["/dev/null"]),
        ("go test ./... &> out.log", ["out.log"]),
        ("ls | tee -a list.txt", ["list.txt"]),
        ("cd web && npm test > ../t.log; echo done", ["../t.log"]),
        ("cat > doc.md <<'EOF'\nnot > a/target\nEOF\necho ok", ["doc.md"]),
        ("cat <<EOF\nsed -i x y.md\nEOF", []),
        ("git commit -s -m \"$(cat <<'EOF'\nchore: a > b\nEOF\n)\"", []),
        ("echo \"a > b\" 'tee c'", []),
        ("diff <(sort a) <(sort b)", []),
        ("FOO=1 sudo tee /etc/x < in", ["/etc/x"]),
        ("echo x >&2", []),
        ("cmd 'unbalanced > x", []),
        ("echo \"<<EOF\" > doc.md\necho y > b.md", ["doc.md", "b.md"]),  # no closing EOF line
        ("echo x#y > c.md", ["c.md"]),  # `#` inside a word isn't a comment in bash
    ]

    def test_targets(self):
        for cmd, want in self.CASES:
            with self.subTest(cmd=cmd):
                self.assertEqual(bash_writes.write_targets(cmd), want)


class RepoWriteTargets(unittest.TestCase):
    """A throwaway repository as the project: inside the project, a temp dir isn't scratch."""

    def setUp(self):
        self.repo = new_repo()
        self.addCleanup(shutil.rmtree, self.repo)
        with open(os.path.join(self.repo, ".gitignore"), "w", encoding="utf-8") as fh:
            fh.write("/bin/\n")
        patcher = mock.patch.dict(os.environ, {"CLAUDE_PROJECT_DIR": self.repo})
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_foreign_fsmonitor_never_runs(self):
        marker = os.path.join(self.repo, "fsmonitor-ran")
        hook = os.path.join(self.repo, "fsmonitor.sh")
        with open(hook, "w", encoding="utf-8") as fh:
            fh.write(f"#!/bin/sh\ntouch '{marker}'\n")
        os.chmod(hook, 0o755)
        git(self.repo, "add", ".gitignore")
        git(self.repo, "config", "core.fsmonitor", hook)
        self.assertEqual(bash_writes.repo_write_targets("echo x > a.md", self.repo), ["a.md"])
        run_guard("git", {"cwd": self.repo, "tool_input": {"command": "echo x > a.md"}}, self.repo)
        path = os.path.join(self.repo, "docs", "adr", "0001-x.md")
        os.makedirs(os.path.dirname(path))
        with open(path, "w", encoding="utf-8") as fh:
            fh.write("x")
        run_guard("adr", {"tool_input": {"file_path": path, "old_string": "x"}}, self.repo)
        self.assertFalse(os.path.exists(marker), "the repository's core.fsmonitor ran")

    def test_judges_each_target(self):
        outside = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, outside)
        cases = [
            ("echo x > README.md", ["README.md"]),
            ("sed -i s/a/b/ docs/new/x.md", ["docs/new/x.md"]),
            ("make build > bin/build.log 2>&1", []),          # git-ignored build output
            ("echo x > /dev/null", []),
            (f"echo x > {outside}/scratch.txt", []),          # outside any repository
            ("echo x > \"$OUT\"", []),                       # unknown until it runs
        ]
        for cmd, want in cases:
            with self.subTest(cmd=cmd):
                self.assertEqual(bash_writes.repo_write_targets(cmd, self.repo), want)


class GuardBashMode(unittest.TestCase):
    """The hook end to end, judging (never running) commands in a throwaway project."""

    def setUp(self):
        self.repo = new_repo()
        self.addCleanup(shutil.rmtree, self.repo)
        with open(os.path.join(self.repo, ".gitignore"), "w", encoding="utf-8") as fh:
            fh.write("/bin/\n")

    def guard(self, command):
        return run_guard("git", {"cwd": self.repo, "tool_input": {"command": command}}, self.repo)

    def test_asks_before_a_shell_write(self):
        got = self.guard("echo x > CLAUDE.md")
        self.assertEqual(got["permissionDecision"], "ask")
        self.assertIn("Write/Edit", got["permissionDecisionReason"])

    def test_git_rules_come_first(self):
        got = self.guard("git commit --no-verify -s -m 'chore: x' > a.log")
        self.assertEqual(got["permissionDecision"], "deny")

    def test_allows_scratch_and_build_output(self):
        for command in ("git status > /dev/null 2>&1", "make build > bin/build.log",
                        "go test ./... > /tmp/t.log 2>&1", "sed -n 1p CLAUDE.md"):
            with self.subTest(command=command):
                self.assertIsNone(self.guard(command))


def adr(status, decision):
    return (f"# ADR\n\n- **Status:** {status}\n\n## Context\nWhy.\n\n## Decision\n{decision}\n\n"
            "## Consequences\nSome.\n")


class AdrCheck(unittest.TestCase):
    def setUp(self):
        self.repo = new_repo()
        self.addCleanup(shutil.rmtree, self.repo)
        os.makedirs(os.path.join(self.repo, "docs", "adr"))
        self.write("0001-main.md", adr("Accepted", "We use X."))
        git(self.repo, "add", "docs")
        git(self.repo, "commit", "-q", "-m", "main")
        self.main = subprocess.run(["git", "-C", self.repo, "rev-parse", "HEAD"],
                                   capture_output=True, text=True, check=True).stdout.strip()
        git(self.repo, "switch", "-q", "-c", "feature")
        self.write("0002-branch.md", adr("Accepted", "First draft."))
        git(self.repo, "add", "docs")
        git(self.repo, "commit", "-q", "-m", "branch")

    def write(self, name, text):
        with open(os.path.join(self.repo, "docs", "adr", name), "w", encoding="utf-8") as fh:
            fh.write(text)

    def edit(self, name, old):
        path = os.path.join(self.repo, "docs", "adr", name)
        return run_guard("adr", {"tool_input": {"file_path": path, "old_string": old,
                                                "new_string": "Changed."}}, self.repo)

    def test_new_on_branch_is_editable(self):
        git(self.repo, "update-ref", "refs/remotes/origin/main", self.main)
        self.assertIsNone(self.edit("0002-branch.md", "First draft."))

    def test_accepted_on_main_asks(self):
        git(self.repo, "update-ref", "refs/remotes/origin/main", self.main)
        got = self.edit("0001-main.md", "We use X.")
        self.assertEqual(got["permissionDecision"], "ask")
        self.assertIn("ACCEPTED on main", got["permissionDecisionReason"])

    def test_accepted_on_main_after_the_fork_point_asks(self):
        git(self.repo, "switch", "-q", "-c", "later", self.main)
        self.write("0003-later.md", adr("Accepted", "Later."))
        git(self.repo, "add", "docs")
        git(self.repo, "commit", "-q", "-m", "later")
        git(self.repo, "update-ref", "refs/remotes/origin/main", "HEAD")
        git(self.repo, "switch", "-q", "feature")
        self.write("0003-later.md", adr("Proposed", "Later."))  # stale copy on the branch
        got = self.edit("0003-later.md", "Later.")
        self.assertEqual(got["permissionDecision"], "ask")

    def test_without_origin_compares_with_head(self):
        got = self.edit("0002-branch.md", "First draft.")
        self.assertEqual(got["permissionDecision"], "ask")
        self.assertIn("no origin/main", got["permissionDecisionReason"])


if __name__ == "__main__":
    unittest.main()
