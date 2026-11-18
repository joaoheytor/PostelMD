"""Tests for the maintainer's local opt-in (trust.py, localcmd.py): with a valid
.claude/guard-trust.local.json, SHELL WRITE inside the project, CAN'T PARSE on a command
that stays local and edits to Accepted ADRs stop asking; everything else asks or denies as
before. Fixture: relax_test.Fixture ({r} the project, {i} a worktree under it, {w} one
outside, {f}/wt a foreign repository, {r}/L a symlink into it), with the git-ignored local
files excluded as in the real repository.
Run: make test-scripts
"""
import json
import os
import shutil
import tempfile
import unittest
from unittest import mock

import trust
from postelmd_guard_test import adr, git, new_repo, run_guard
from relax_test import Fixture

ALL = ["shell-writes", "unparsed-local", "accepted-adr"]
SHELL_WRITES = [
    "sed -i s/a/b/ README.md",
    "perl -pi -e 's/a/b/' sub/x.go",
    "echo x > notes.md",
    "ls | tee sub/list.txt",
    "echo x > {r}/sub/y.md",
    "echo x > {i}/notes.md",  # a worktree under .claude/worktrees/
]
LOCAL_UNPARSED = [
    "for f in sub/*.md; do git log -1 --format=%h -- \"$f\"; done",
    "git log --oneline $(git merge-base HEAD main)..HEAD",
    "git show --stat $(git rev-parse HEAD) | head -5",
    "ls {{a,b}} && git status",
    "git status && ls *",
    "git status # what changed",
    "git ls-files | while read -r f; do wc -l \"$f\"; done",
    "for f in sub/*.md; do echo \"${{f%.md}}\"; done; make lint",
]


def trust_path(root):
    return os.path.join(root, ".claude", "guard-trust.local.json")


def write_trust(root, text, mode=0o644):
    path = trust_path(root)
    with open(path, "w", encoding="utf-8") as fh:
        fh.write(text)
    os.chmod(path, mode)


def remove_trust(root):
    path = trust_path(root)
    if os.path.lexists(path):
        if os.path.isdir(path) and not os.path.islink(path):
            os.rmdir(path)
        else:
            os.remove(path)


class TrustFixture(Fixture):
    @classmethod
    def setUpClass(cls):
        super().setUpClass()
        with open(os.path.join(cls.repo, ".git", "info", "exclude"), "a", encoding="utf-8") as fh:
            fh.write("/.claude/*.local.*\n")

    def setUp(self):
        self.addCleanup(remove_trust, self.repo)

    def trust(self, kinds=ALL):
        write_trust(self.repo, json.dumps({"trust": kinds}))

    def reason(self, command):
        got = run_guard("git", {"cwd": self.repo, "tool_input": {"command": command}}, self.repo)
        return got and got["permissionDecisionReason"]


class OffByDefault(TrustFixture):
    def test_without_the_file_all_three_still_ask(self):
        self.assert_cases(SHELL_WRITES + LOCAL_UNPARSED, "ask")


class ShellWrites(TrustFixture):
    def test_writes_inside_the_project_pass(self):
        self.trust()
        self.assert_cases(SHELL_WRITES, None)

    def test_only_the_trusted_kind_passes(self):
        self.trust(["accepted-adr"])
        self.assert_cases(SHELL_WRITES, "ask")

    def test_protected_and_outside_targets_still_ask(self):
        self.trust()
        self.assert_cases([
            "echo x > .claude/hooks/x.py",
            "echo x > {i}/.claude/hooks/x.py",
            "sed -i s/a/b/ .claude/settings.json",
            "echo x > .githooks/pre-commit",
            "echo x > .github/workflows/ci.yaml",
            "echo x > .git/config",
            "echo x > LICENSE",
        ], "ask")

    def test_targets_outside_the_project_are_not_trusted(self):
        """In this fixture they live in a temp dir, where the guard sees scratch files and
        doesn't ask at all; judge them where the opt-in decides."""
        self.trust()
        with mock.patch.dict(os.environ, {"CLAUDE_PROJECT_DIR": self.repo}):
            trust.kinds.cache_clear()
            self.addCleanup(trust.kinds.cache_clear)
            self.assertTrue(trust.shell_writes_ok(["README.md", f"{self.inner}/a.md"], self.repo))
            for targets in ([f"{self.wt}/notes.md"], [f"{self.files}/wt/notes.md"],
                            ["L/notes.md"], ["README.md", "../x.md"], ["~/x.md"], ["$X/a.md"],
                            [".claude/public-guard.local.txt"], [self.repo], []):
                with self.subTest(targets=targets):
                    self.assertFalse(trust.shell_writes_ok(targets, self.repo))

    def test_adr_shell_writes_need_the_adr_kind_too(self):
        self.trust(["shell-writes"])
        self.assert_cases(["sed -i s/a/b/ docs/adr/0001-x.md"], "ask")
        self.trust()
        self.assert_cases(["sed -i s/a/b/ docs/adr/0001-x.md"], None)

    def test_agents_cannot_write_the_opt_in(self):
        for kinds in ([], ALL):
            remove_trust(self.repo)
            if kinds:
                self.trust(kinds)
            self.assert_cases([
                "echo '{{\"trust\": []}}' > .claude/guard-trust.local.json",
                "tee {r}/.claude/guard-trust.local.json < x",
            ], "ask")
            got = run_guard("public", {"tool_input": {
                "file_path": trust_path(self.repo), "content": "{}"}}, self.repo)
            self.assertEqual(got and got["permissionDecision"], "ask")


class UnparsedLocal(TrustFixture):
    def test_local_commands_pass(self):
        self.trust()
        self.assert_cases(LOCAL_UNPARSED, None)

    def test_outward_or_outside_commands_still_ask(self):
        self.trust()
        self.assert_cases([
            "for b in feature; do git push origin $b; done",
            "s=push; git $s; ls *",              # substituted: git push
            "x=pu; git ${{x}}sh && ls *",
            "for x in pu; do git ${{x}}sh; done",
            "x=$(gh pr view 5 --json title); echo $x",
            "git log $(curl -s example.com)",
            "git status && ls * && wget example.com",
            "git status && ls * && ssh host true",
            "git status && ls * && docker push x",
            "git status && ls * && npm publish",
            "npx foo && git status && ls *",
            "git status && ls * 'git push'",      # even quoted: unsure, ask
            "git status && ls * https://example.com",
            "git config core.pager x; ls *",
            "git -c core.pager=x log; ls *",
            "git fetch origin && ls *",
            "for r in *; do git -C {f}/wt log $r; done",
            "git log -- $HOME && ls *",           # a variable the command doesn't set
            "git status && cat ~/x && ls *",
            "for x in a; do ls ../a $x; git status; done",
            "for x in a; do git log -- sub/../../$x; done",
            "git log > /tmp/x && ls *",
            "ls * && git -C {w} status",
            "python3 -c 'print(1)' && git status && ls *",
            "awk 1 x && git status && ls *",
            "git status && ls * | xargs echo",
            "find . -exec true {{}} + && git status && ls *",
            "IFS=x; git status; ls *",
            "eval git status; ls *",
            "git status 'x",                      # can't be tokenized
            "git status && ls *(e:'true':)",      # a glob qualifier runs code
            "git status && =ls",
            "git status && ls * $'x'",
            "git status && ls *\\\nx",
            "$G status && git log && ls *",
            "./x.sh && git status && ls *",
        ], "ask")


class KeepsAsking(TrustFixture):
    """Every other category asks (or denies) exactly as without the opt-in."""

    def test_other_bash_checks(self):
        self.trust()
        for command, label in (
                ("git commit -s -F - <<'EOF'\nchore: see {home}\nEOF", "COMMIT MESSAGE CHECK"),
                ("git tag v9 HEAD", "OUTWARD-FACING"),
                ("cd /tmp && ls", "OUTSIDE THE PROJECT"),
                ("git -C {f}/wt status", "FOREIGN REPOSITORY"),
                ("git push origin main", "NEEDS THE MAINTAINER"),
                ("ls * && git -C {f}/wt status", "CAN'T PARSE"),
                ("for x in a; do git -C L log $x; done", "FOREIGN REPOSITORY"),
                ("echo x > .claude/hooks/x.py", "SHELL WRITE")):
            command = command.format(r=self.repo, f=self.files, home="/" + "home/someone/x")
            with self.subTest(command=command):
                self.assertIn(label, self.reason(command) or "")
        self.assert_cases(["git commit -m 'chore: x'",
                           "git commit --no-verify -s -m 'chore: x' && ls *"], "deny")

    def test_write_edit_checks(self):
        self.trust()
        home, key = "/" + "home/someone/x", "AKIA" + "Q7" * 8
        for mode, name, text, want in (("public", "notes.md", f"see {home}", "ask"),
                                       ("secrets", "a_test.go", key, "ask"),
                                       ("secrets", "notes.md", key, "deny")):
            got = run_guard(mode, {"tool_input": {"file_path": os.path.join(self.repo, name),
                                                  "content": text}}, self.repo)
            self.assertEqual(got and got["permissionDecision"], want, name)


class BadOptIn(TrustFixture):
    """A symlinked, malformed or loosely permitted file reads as off."""

    def assert_off(self):
        self.assert_cases(SHELL_WRITES[:1] + LOCAL_UNPARSED[:1], "ask")

    def test_a_symlink_is_ignored(self):
        other = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, other)
        target = os.path.join(other, "trust.json")
        with open(target, "w", encoding="utf-8") as fh:
            json.dump({"trust": ALL}, fh)
        os.symlink(target, trust_path(self.repo))
        self.assert_off()

    def test_malformed_files_are_ignored(self):
        for text in ("", "not json", "[]", '{"trust": "shell-writes"}', '{"trust": []}',
                     '{"trust": ["shell-writes", "everything"]}',
                     '{"trust": ["shell-writes"], "also": 1}', '{"trust": [1]}',
                     json.dumps({"trust": ALL}) + " " * 5000):
            with self.subTest(text=text[:40]):
                write_trust(self.repo, text)
                self.assert_off()

    def test_group_writable_or_odd_files_are_ignored(self):
        write_trust(self.repo, json.dumps({"trust": ALL}), mode=0o664)
        self.assert_off()
        remove_trust(self.repo)
        os.mkdir(trust_path(self.repo))
        self.assert_off()
        remove_trust(self.repo)
        os.mkfifo(trust_path(self.repo))  # must not block the hook
        self.assert_off()


class AcceptedAdr(unittest.TestCase):
    def setUp(self):
        self.repo = new_repo()
        self.addCleanup(shutil.rmtree, self.repo)
        os.makedirs(os.path.join(self.repo, "docs", "adr"))
        os.makedirs(os.path.join(self.repo, ".claude"))
        self.path = os.path.join(self.repo, "docs", "adr", "0001-main.md")
        with open(self.path, "w", encoding="utf-8") as fh:
            fh.write(adr("Accepted", "We use X."))
        git(self.repo, "add", "docs")
        git(self.repo, "commit", "-q", "-m", "main")

    def decisions(self):
        edits = ({"old_string": "We use X.", "new_string": "Y."}, {"content": "rewritten"})
        return [(run_guard("adr", {"tool_input": {"file_path": self.path, **e}}, self.repo)
                 or {}).get("permissionDecision") for e in edits]

    def test_asks_without_the_adr_kind(self):
        self.assertEqual(self.decisions(), ["ask", "ask"])
        write_trust(self.repo, json.dumps({"trust": ["shell-writes", "unparsed-local"]}))
        self.assertEqual(self.decisions(), ["ask", "ask"])

    def test_passes_with_it(self):
        write_trust(self.repo, json.dumps({"trust": ["accepted-adr"]}))
        self.assertEqual(self.decisions(), [None, None])


if __name__ == "__main__":
    unittest.main()
