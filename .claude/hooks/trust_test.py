"""Tests for the maintainer's opt-in (trust.py): with a valid guard-trust.json in
${XDG_CONFIG_HOME:-~/.config}/postelmd/ naming this project, SHELL WRITE inside the project
and edits to Accepted ADRs stop asking; everything else asks or denies as before. CAN'T
PARSE: localcmd_test.py. Fixture: relax_test.Fixture ({r} the project, {i} a worktree under
it, {w} one outside, {f}/wt a foreign repository, {r}/L a symlink into it), with the
git-ignored local files excluded as in the real repository.
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
from relax_test import HOME, Fixture

ALL = ["shell-writes", "unparsed-local", "accepted-adr"]
SHELL_WRITES = [
    "sed -i s/a/b/ README.md",
    "perl -pi -e 's/a/b/' sub/x.go",
    "echo x > notes.md",
    "ls | tee sub/list.txt",
    "echo x > {r}/sub/y.md",
    "echo x > {i}/notes.md",  # a worktree under .claude/worktrees/
]


class TrustFixture(Fixture):
    """Each test gets its own XDG_CONFIG_HOME; trust() writes the opt-in there."""

    @classmethod
    def setUpClass(cls):
        super().setUpClass()
        with open(os.path.join(cls.repo, ".git", "info", "exclude"), "a", encoding="utf-8") as fh:
            fh.write("/.claude/*.local.*\n")

    def setUp(self):
        self.xdg = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.xdg)
        patcher = mock.patch.dict(os.environ, {"XDG_CONFIG_HOME": self.xdg})
        patcher.start()
        self.addCleanup(patcher.stop)
        self.opt_in = os.path.join(self.xdg, "postelmd", "guard-trust.json")

    def write_opt_in(self, text, mode=0o644):
        os.makedirs(os.path.dirname(self.opt_in), exist_ok=True)
        if os.path.lexists(self.opt_in):
            os.remove(self.opt_in)
        with open(self.opt_in, "w", encoding="utf-8") as fh:
            fh.write(text)
        os.chmod(self.opt_in, mode)

    def trust(self, kinds=ALL, project=None):
        self.write_opt_in(json.dumps({"project": project or os.path.realpath(self.repo),
                                      "trust": kinds}))

    def untrust(self):
        if os.path.lexists(self.opt_in):
            os.remove(self.opt_in)

    def reason(self, command):
        got = run_guard("git", {"cwd": self.repo, "tool_input": {"command": command}}, self.repo)
        return got and got["permissionDecisionReason"]

    def assert_unchanged(self, cases, kinds=ALL):
        """Each case asks or denies without the opt-in, and decides the same with it."""
        for command in cases:
            command = command.format(r=self.repo, w=self.wt, f=self.files, i=self.inner,
                                     home=HOME)
            with self.subTest(command=command):
                self.untrust()
                off = self.decide(command)
                self.assertIn(off, ("ask", "deny"))
                self.trust(kinds)
                self.assertEqual(self.decide(command), off)

    def write_edit(self, path, text="x"):
        got = run_guard("public", {"tool_input": {"file_path": path, "content": text}}, self.repo)
        return got and got["permissionDecision"]


class ShellWrites(TrustFixture):
    def test_off_by_default(self):
        self.assert_cases(SHELL_WRITES, "ask")

    def test_writes_inside_the_project_pass(self):
        self.trust()
        self.assert_cases(SHELL_WRITES, None)

    def test_only_the_trusted_kind_passes(self):
        self.trust(["accepted-adr"])
        self.assert_cases(SHELL_WRITES, "ask")

    def test_protected_targets_still_ask(self):
        self.assert_unchanged([
            "echo x > .claude/hooks/x.py", "echo x > {i}/.claude/hooks/x.py",
            "sed -i s/a/b/ .claude/settings.json", "echo x > .githooks/pre-commit",
            "echo x > .github/workflows/ci.yaml", "echo x > .github/allowed_signers",
            "echo x > .git/config", "echo x > LICENSE", "echo x > .Claude/Hooks/x.py",
            "echo x > .GIT/config", "echo x > docs/CODEOWNERS",
        ])

    def test_moves_links_and_cd_still_ask(self):
        """Security review of #195, finding 1: targets judged from the wrong directory or
        through links made in the same command."""
        self.assert_unchanged([
            "cd .claude/hooks && echo x > postelmd_guard.py",
            "cd .git && echo '[core]' >> config",
            "cd .githooks && echo x > pre-commit",
            "mkdir -p .github/workflows && cd .github/workflows && echo x > ci.yaml",
            "cd .claude && echo x > settings.json",
            "cd .claude/worktrees/agent-1/.claude/hooks && echo x > a.py",
            "cd docs/adr && sed -i s/Accepted/Proposed/ 0001-x.md",
            "ln -sf .git/config c.txt && echo '[core]' >> c.txt",
            "ln -f .git/config c.txt && echo '[core]' >> c.txt",
            "(cd .git; echo x > config)",
        ])

    def test_an_earlier_hard_link_still_asks(self):
        link = os.path.join(self.repo, "hl.txt")
        os.link(os.path.join(self.repo, ".git", "config"), link)
        self.addCleanup(os.remove, link)
        self.assert_unchanged(["echo '[core]' >> hl.txt"])

    def test_ignored_protected_targets_always_ask(self):
        """Finding 7: git-ignored files the guard used to skip."""
        for kinds in (None, ALL):
            self.untrust() if kinds is None else self.trust(kinds)
            self.assert_cases(["echo '{{}}' > .claude/settings.local.json",
                               "echo x > .claude/public-guard.local.txt",
                               "cd .claude && echo '{{}}' > settings.local.json"], "ask")

    def test_adr_shell_writes_need_the_adr_kind_too(self):
        self.trust(["shell-writes"])
        self.assert_cases(["sed -i s/a/b/ docs/adr/0001-x.md"], "ask")
        self.trust()
        self.assert_cases(["sed -i s/a/b/ docs/adr/0001-x.md"], None)

    def test_targets_outside_the_project_are_not_trusted(self):
        """In this fixture they live in a temp dir, where the guard sees scratch files and
        doesn't ask at all; judge them where the opt-in decides."""
        self.trust()
        with mock.patch.dict(os.environ, {"CLAUDE_PROJECT_DIR": self.repo}):
            trust.kinds.cache_clear()
            self.addCleanup(trust.kinds.cache_clear)
            ok = trust.shell_writes_ok
            self.assertTrue(ok("echo", ["README.md", f"{self.inner}/a.md"], self.repo))
            for targets in ([f"{self.wt}/notes.md"], [f"{self.files}/wt/notes.md"],
                            ["L/notes.md"], ["README.md", "../x.md"], ["~/x.md"], ["$X/a.md"],
                            [self.repo], []):
                with self.subTest(targets=targets):
                    self.assertFalse(ok("echo", targets, self.repo))


class OptIn(TrustFixture):
    def test_agents_cannot_write_or_name_the_opt_in(self):
        """Finding 4: any Bash, Write or Edit that names it asks, whatever the opt-in says."""
        for kinds in (None, ALL):
            self.untrust() if kinds is None else self.trust(kinds)
            self.assert_cases([
                "cp x .claude/guard-trust.local.json",
                "mkdir -p ~/.config/postelmd && cp x ~/.config/postelmd/Guard-Trust.json",
                "ln -s {f}/x notes.json; echo $XDG_CONFIG_HOME",
                "cat ~/.config/postelmd/x",
            ], "ask")
            self.assertEqual(self.write_edit(self.opt_in, "{}"), "ask")
            self.assertEqual(self.write_edit(os.path.join(self.repo, "a.md"),
                                             "see guard-trust.json"), "ask")
            link = os.path.join(self.repo, "notes.json")
            os.symlink(self.opt_in, link)
            try:
                self.assertEqual(self.write_edit(link, "{}"), "ask")
            finally:
                os.remove(link)

    def test_the_old_in_project_file_is_ignored(self):
        old = os.path.join(self.repo, ".claude", "guard-trust.local.json")
        with open(old, "w", encoding="utf-8") as fh:
            json.dump({"trust": ALL}, fh)
        self.addCleanup(os.remove, old)
        self.assert_cases(SHELL_WRITES[:1], "ask")

    def test_bad_opt_ins_are_ignored(self):
        real = os.path.realpath(self.repo)
        for text in ("", "not json", "[]", json.dumps({"trust": ALL}),
                     json.dumps({"project": real, "trust": []}),
                     json.dumps({"project": real, "trust": ["shell-writes", "everything"]}),
                     json.dumps({"project": real, "trust": ALL, "also": 1}),
                     json.dumps({"project": self.files, "trust": ALL}),
                     json.dumps({"project": real, "trust": ALL}) + " " * 5000):
            with self.subTest(text=text[:60]):
                self.write_opt_in(text)
                self.assert_cases(SHELL_WRITES[:1], "ask")
        self.trust()
        os.chmod(self.opt_in, 0o664)
        self.assert_cases(SHELL_WRITES[:1], "ask")
        os.chmod(self.opt_in, 0o644)
        os.link(self.opt_in, self.opt_in + ".hard")
        self.assert_cases(SHELL_WRITES[:1], "ask")
        os.remove(self.opt_in + ".hard")
        os.rename(self.opt_in, self.opt_in + ".real")
        os.symlink(self.opt_in + ".real", self.opt_in)
        self.assert_cases(SHELL_WRITES[:1], "ask")
        os.remove(self.opt_in)
        os.mkfifo(self.opt_in)  # must not block the hook
        self.assert_cases(SHELL_WRITES[:1], "ask")

    def test_other_checks_keep_asking(self):
        self.trust()
        for command, label in (
                ("git commit -s -F - <<'EOF'\nchore: see {home}\nEOF", "COMMIT MESSAGE CHECK"),
                ("git tag v9 HEAD", "OUTWARD-FACING"),
                ("cd /tmp && ls", "OUTSIDE THE PROJECT"),
                ("git -C {f}/wt status", "FOREIGN REPOSITORY"),
                ("git push origin main", "NEEDS THE MAINTAINER"),
                ("ls * && git -C {f}/wt status", "CAN'T PARSE")):
            command = command.format(f=self.files, home=HOME)
            with self.subTest(command=command):
                self.assertIn(label, self.reason(command) or "")
        self.assert_cases(["git commit -m 'chore: x'",
                           "git commit --no-verify -s -m 'chore: x' && ls *"], "deny")
        key = "AKIA" + "Q7" * 8
        self.assertEqual(self.write_edit(os.path.join(self.repo, "n.md"), HOME), "ask")
        for name, want in (("a_test.go", "ask"), ("notes.md", "deny")):
            got = run_guard("secrets", {"tool_input": {"file_path": os.path.join(
                self.repo, name), "content": key}}, self.repo)
            self.assertEqual(got and got["permissionDecision"], want, name)


class AcceptedAdr(unittest.TestCase):
    def setUp(self):
        self.repo = new_repo()
        self.addCleanup(shutil.rmtree, self.repo)
        self.xdg = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.xdg)
        patcher = mock.patch.dict(os.environ, {"XDG_CONFIG_HOME": self.xdg})
        patcher.start()
        self.addCleanup(patcher.stop)
        os.makedirs(os.path.join(self.repo, "docs", "adr"))
        self.path = os.path.join(self.repo, "docs", "adr", "0001-main.md")
        with open(self.path, "w", encoding="utf-8") as fh:
            fh.write(adr("Accepted", "We use X."))
        git(self.repo, "add", "docs")
        git(self.repo, "commit", "-q", "-m", "main")

    def trust(self, kinds):
        os.makedirs(os.path.join(self.xdg, "postelmd"), exist_ok=True)
        with open(os.path.join(self.xdg, "postelmd", "guard-trust.json"), "w",
                  encoding="utf-8") as fh:
            json.dump({"project": os.path.realpath(self.repo), "trust": kinds}, fh)

    def decisions(self):
        edits = ({"old_string": "We use X.", "new_string": "Y."}, {"content": "rewritten"})
        return [(run_guard("adr", {"tool_input": {"file_path": self.path, **e}}, self.repo)
                 or {}).get("permissionDecision") for e in edits]

    def test_asks_without_the_adr_kind(self):
        self.assertEqual(self.decisions(), ["ask", "ask"])
        self.trust(["shell-writes", "unparsed-local"])
        self.assertEqual(self.decisions(), ["ask", "ask"])

    def test_passes_with_it(self):
        self.trust(["accepted-adr"])
        self.assertEqual(self.decisions(), [None, None])


if __name__ == "__main__":
    unittest.main()
