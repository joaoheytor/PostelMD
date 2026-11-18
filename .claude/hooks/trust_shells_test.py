"""The opt-in's trusted cases (the commands the trust tests expect to pass with the opt-in
on) must do the same under bash and zsh, as shells_test.py checks for the guard's other
allowed commands: each runs in its own copy of a small project with fake git, gh, go, make,
npm and npx, and the files left behind, stdout and the fakes' log must match bash's.
Skipped where bash or zsh isn't installed, except in CI. Run: make test-scripts
"""
import os
import shutil
import subprocess
import tempfile
import unittest

import localcmd_test
import trust_review3_test
import trust_review_test
import trust_test  # modules, not classes: discovery would run imported test classes twice
from shells_test import FAKE, SHELLS, require_shells

SOURCES = ((trust_test.ShellWrites, "test_writes_inside_the_project_pass"),
           (localcmd_test.UnparsedLocal, "test_local_commands_pass"),
           (trust_review_test.SecondReview, "test_sandboxed_sed_and_plain_tools_still_pass"),
           (trust_review3_test.ThirdReview, "test_reading_inside_the_project_still_passes"))


def trusted_cases():
    found = []
    for cls, name in SOURCES:
        class Collect(cls):
            def trust(self, *args, **kwargs):
                pass

            def assert_cases(self, cases, want):
                if want is None:
                    found.extend(cases)
        getattr(Collect(name), name)()
    return found


def project(root):
    for rel, text in (("README.md", "a\nTODO b\n"), ("sub/x.go", "package a\n"),
                      ("sub/a.md", "a\n"), (".claude/worktrees/agent-1/keep", "")):
        os.makedirs(os.path.join(root, os.path.dirname(rel)), exist_ok=True)
        with open(os.path.join(root, rel), "w", encoding="utf-8") as fh:
            fh.write(text)


def snapshot(root):
    files = {}
    for d, _, names in os.walk(root):
        for n in names:
            path = os.path.join(d, n)
            with open(path, encoding="utf-8", errors="replace") as fh:
                files[os.path.relpath(path, root)] = fh.read()
    return files


class TrustedSameUnderZsh(unittest.TestCase):
    def setUp(self):
        require_shells(self)
        self.bin = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.bin)
        for name in ("git", "gh", "go", "make", "npm", "npx"):
            path = os.path.join(self.bin, name)
            with open(path, "w", encoding="utf-8") as fh:
                fh.write(FAKE)
            os.chmod(path, 0o755)

    def run_in(self, shell, case):
        root, home = tempfile.mkdtemp(), tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, root)
        self.addCleanup(shutil.rmtree, home)
        project(root)
        command = case.format(r=root, i=os.path.join(root, ".claude", "worktrees", "agent-1"))
        log = os.path.join(home, "fake.log")
        env = {"PATH": f"{self.bin}:/usr/bin:/bin", "HOME": home, "LANG": "C", "FAKE_LOG": log}
        p = subprocess.run(shell + [command], cwd=root, env=env, capture_output=True, text=True,
                           timeout=20, check=False)
        runs = ""
        if os.path.exists(log):
            with open(log, encoding="utf-8") as fh:
                runs = fh.read()
        return [x.replace(root, "<r>") for x in (p.stdout, runs, repr(sorted(snapshot(root).items())))]

    def test_trusted_cases_run_the_same_under_zsh(self):
        cases = trusted_cases()
        self.assertGreater(len(cases), 15)
        for case in cases:
            with self.subTest(command=case):
                got = [self.run_in(shell, case) for shell in SHELLS]
                for other in got[1:]:
                    self.assertEqual(other, got[0], "zsh does something else")


if __name__ == "__main__":
    unittest.main()
