"""Regression tests for the fourth security pass on #170 (403119e): a .git file pointing at a
crafted gitdir, environment prefixes that change what git reads, and commit messages that
were never scanned. Each "ask"/"deny" case passed the hook silently before its fix.
Run: python3 -m unittest discover -s .claude/hooks -p '*_test.py'  (make test-scripts)
"""
import os
import shutil
import subprocess
import tempfile
import unittest

from postelmd_guard_test import git, new_repo, run_guard

KEY = "AKIA" + "Z3K7QW2PLM4XRT6B"  # built at run time; high enough entropy for gitleaks
HOME = "/" + "home" + "/someone/notes.md"
HERE = os.path.dirname(os.path.abspath(__file__))
COMMIT_MSG = os.path.join(HERE, "..", "..", ".githooks", "commit-msg")


class GitEnv(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.repo = new_repo()
        git(cls.repo, "commit", "-q", "--allow-empty", "-s", "-m", "chore: init")
        cls.files = tempfile.mkdtemp()
        # worktrees whose .git file points at a gitdir of their own: one inside the project
        git(cls.repo, "init", "-q", "--separate-git-dir", os.path.join(cls.repo, "gd"),
            os.path.join(cls.repo, "wt"))
        git(cls.files, "init", "-q", "--separate-git-dir", os.path.join(cls.files, "gd"),
            os.path.join(cls.files, "wt"))
        for name, text in (("secret.md", f"chore: x\n\n{KEY}\n"), ("ip.md", "chore: x\n\n10.1.2.3\n"),
                           ("clean.md", "chore: x\n\nA fix.\n")):
            with open(os.path.join(cls.files, name), "w", encoding="utf-8") as fh:
                fh.write(text)

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.repo)
        shutil.rmtree(cls.files)

    def decide(self, command, cwd=None):
        got = run_guard("git", {"cwd": cwd or self.repo, "tool_input": {"command": command}},
                        self.repo)
        return got and got["permissionDecision"]

    def assert_cases(self, cases, want, cwd=None):
        for command in cases:
            command = command.format(f=self.files, r=self.repo, key=KEY, home=HOME)
            with self.subTest(command=command):
                self.assertEqual(self.decide(command, cwd), want)

    def test_foreign_gitdir_asks(self):
        self.assert_cases([
            "cd wt && git status",
            "cd {r}/wt && git status",
            "git -C wt status",
            "cd {f}/wt && git status",
            "git -C {f}/wt log --oneline",
            "(cd {r}) ; cd wt; git status",
            "cd $X && git status",
            "echo 'gitdir: ../gd' > wt2/.git",
        ], "ask")
        self.assertEqual(self.decide("git status", cwd=os.path.join(self.repo, "wt")), "ask")
        self.assertEqual(self.decide("git status", cwd=self.files), "ask")  # not a repository
        self.assert_cases(["git status", "cd {r} && git status", "git -C {r} log -1"], None)

    def test_environment_prefixes_ask(self):
        self.assert_cases([
            "HOME={f} git status",
            "XDG_CONFIG_HOME={f} git status",
            "PATH={f}:$PATH make test",
            "env EDITOR=x git commit -s -m 'chore: x'",
            "PAGER=x git log",
            "HOME={f}; git status",
            "export HOME={f} && git status",
            "env -i make test",
            "LESSOPEN='|x' go test ./...",
            "SSH_ASKPASS=x npm ci",
            "HOME={f} scripts/check-file-size.sh",
            "VISUAL=x npx vitest",
        ], "ask")
        self.assert_cases([
            "CGO_ENABLED=0 go build -o bin/postelmd ./cmd/postelmd",
            "IMAGE=postelmd:x make ci",
            "PM_E2E_PORT=4100 make e2e",
            "make ci IMAGE=postelmd:x PM_E2E_PORT=4100 SMOKE_PORT=4200",
            "export IMAGE=postelmd:x PM_E2E_PORT=4100 SMOKE_PORT=4200 && make ci IMAGE=postelmd:x",
            "CI=1 SAMPLES=3 make perf",
        ], None)

    def test_commit_messages_are_scanned(self):
        self.assert_cases([
            "git commit -s -m 'chore: x {key}'",
            "git commit -sm 'chore: {key}'",
            "git commit -s --message='chore: x {key}'",
            "git commit -s -F {f}/secret.md",
            "git commit -s -m \"$(cat <<'EOF'\nchore: x\n\n{key}\nEOF\n)\"",
        ], "deny")
        self.assert_cases([
            "git commit -s -F ~/.config/gh/hosts.yml",
            "git commit -s -F -",
            "git commit -s -F {f}/missing.md",
            "git commit -s -F {f}/ip.md",
            "git commit -s -m 'chore: see {home}'",
            "git commit -s -m \"chore: $(cat ~/.config/gh/hosts.yml)\"",
            "git commit -s -m \"$(cat <<EOF\nchore: x\n$(cat ~/x)\nEOF\n)\"",
        ], "ask")
        self.assert_cases([
            "git commit -s -F {f}/clean.md",
            "git commit -s -m \"$(cat <<'EOF'\nchore: x\n\nA fix.\nEOF\n)\"",
        ], None)

    @unittest.skipUnless(shutil.which("gitleaks"), "gitleaks not installed")
    def test_commit_msg_hook_scans_for_secrets(self):
        msg = os.path.join(self.files, "COMMIT_EDITMSG")
        for body, want in ((f"chore: x\n\n{KEY}\n", "secret"), ("chore: x\n\nA fix.\n", "DCO")):
            with open(msg, "w", encoding="utf-8") as fh:
                fh.write(body)
            r = subprocess.run(["sh", COMMIT_MSG, msg], cwd=self.repo, capture_output=True,
                               text=True)
            with self.subTest(want=want):
                self.assertNotEqual(r.returncode, 0)
                self.assertIn(want, r.stderr)
                self.assertNotIn(KEY, r.stderr + r.stdout)


if __name__ == "__main__":
    unittest.main()
