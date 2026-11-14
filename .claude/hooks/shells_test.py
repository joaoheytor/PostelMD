"""The guard models bash; agents' Bash tool runs zsh. Every command the guard tests allow
(relax_test, review_test, review3_test to review5_test) is run here under bash, zsh -f, zsh
-f with extendedglob, and zsh as Claude Code runs it (its setopt line, then eval), with fake
git, gh, go, make, npm and npx that log their directory and arguments. Each shell must run
the same programs with the same arguments in the same place, or zsh nothing at all (a glob
with no match). A command that zsh runs differently (=cmd, $x:s/a/b/, two-argument cd,
x(e+cmd+) glob qualifiers) must not be allowed. The user's own aliases and options (the
snapshot Claude Code sources) are not modelled (#178).
Skipped where bash or zsh isn't installed, except in CI, where that fails (ci.yaml's
test-go job installs zsh). Run: make test-scripts
"""
import os
import shutil
import subprocess
import tempfile
import unittest
from unittest import mock

import relax_test  # modules, not classes: discovery would run imported test classes twice
import review3_test
import review4_test
import review5_test
import review_test
from relax_test import API, HOME, KEY, SHA, Fixture

FAKE = ("#!/bin/sh\n{ printf '%s in %s' \"$(basename \"$0\")\" \"$PWD\"; for a in \"$@\"; do printf ' [%s]' \"$a\"; "
        "done; echo; } >> \"$FAKE_LOG\"\n")  # a log, not stdout: redirects and pipes eat that
# Claude Code's Bash tool runs `zsh -c 'source <snapshot>; setopt NO_EXTENDED_GLOB
# NO_BARE_GLOB_QUAL; …; eval '<cmd>''`: the last entry, without the user's snapshot.
CLAUDE_CODE = ["zsh", "-f", "-c", "setopt NO_EXTENDED_GLOB NO_BARE_GLOB_QUAL; eval \"$1\"", "zsh"]
SHELLS = (["bash", "-c"], ["zsh", "-f", "-c"], ["zsh", "-f", "-o", "extendedglob", "-c"],
          CLAUDE_CODE)


def require_shells(test):
    """Skip without bash or zsh, except in CI (CI is set), where that fails: the guard's zsh
    cases must not pass by being skipped."""
    missing = [s for s in ("bash", "zsh") if not shutil.which(s)]
    if missing and os.environ.get("CI"):
        test.fail(f"{', '.join(missing)} not installed: CI must install it (ci.yaml test-go)")
    if missing:
        raise unittest.SkipTest(f"needs {', '.join(missing)}")


def allowed_cases():
    """Every command string a guard test expects to pass silently."""
    found = []
    for cls in (relax_test.Relaxations, review_test.OpenedByTheRelaxations, review_test.OpenOnMain,
                review_test.SecondRecheck, review3_test.ThirdRecheck, review4_test.FourthRecheck,
                review5_test.FifthRecheck):
        class Collect(cls):
            def assert_cases(self, cases, want):
                if want is None:
                    found.extend(cases)
        for name in (n for n in dir(Collect) if n.startswith("test")):
            getattr(Collect(name), name)()
    return found


class NeedsTheShellsInCI(unittest.TestCase):
    def test_a_missing_shell_fails_in_ci_and_skips_elsewhere(self):
        with mock.patch.object(shutil, "which", lambda name: None):
            with mock.patch.dict(os.environ, {"CI": "true"}):
                with self.assertRaises(self.failureException):
                    require_shells(self)
            with mock.patch.dict(os.environ, {"CI": ""}):
                with self.assertRaises(unittest.SkipTest):
                    require_shells(self)


class SameUnderZsh(Fixture):
    def setUp(self):
        require_shells(self)

    def run_in(self, shell, command, bin_dir, home):
        log = os.path.join(home, "fake.log")
        env = {"PATH": f"{bin_dir}:/usr/bin:/bin", "HOME": home, "LANG": "C", "FAKE_LOG": log}
        p = subprocess.run(shell + [command], cwd=self.repo, env=env, capture_output=True,
                           text=True, timeout=20, check=False)
        runs = []
        if os.path.exists(log):
            with open(log, encoding="utf-8") as fh:
                runs = fh.read().splitlines()
            os.remove(log)
        return "no matches" if not runs and "no matches found" in p.stderr else runs

    def test_allowed_commands_run_the_same_under_zsh(self):
        bin_dir, home = tempfile.mkdtemp(), tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, bin_dir)
        self.addCleanup(shutil.rmtree, home)
        for name in ("git", "gh", "go", "make", "npm", "npx"):
            path = os.path.join(bin_dir, name)
            with open(path, "w", encoding="utf-8") as fh:
                fh.write(FAKE)
            os.chmod(path, 0o755)
        cases = allowed_cases()
        self.assertGreater(len(cases), 40)
        for command in cases:
            command = command.format(r=self.repo, w=self.wt, f=self.files, sha=SHA, key=KEY,
                                     home=HOME, api=API)
            with self.subTest(command=command):
                runs = [self.run_in(shell, command, bin_dir, home) for shell in SHELLS]
                for got in runs[1:]:  # zsh's "no matches found" runs nothing: also safe
                    self.assertIn(got, (runs[0], "no matches"), "zsh runs other arguments")


if __name__ == "__main__":
    unittest.main()
