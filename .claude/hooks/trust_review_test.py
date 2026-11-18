"""Repros from the second security review of #195, each a test: without the opt-in the guard
asks, and with every kind trusted it still asks. Fixture: trust_test.TrustFixture.
Run: make test-scripts
"""
from trust_test import ALL, TrustFixture


class SecondReview(TrustFixture):
    def assert_asks(self, cases):
        for kinds in (None, ALL):
            if kinds is None:
                self.untrust()
            else:
                self.trust(kinds)
            with self.subTest(trusted=bool(kinds)):
                self.assert_cases(cases, "ask")

    def test_1_globs_and_braces_in_targets(self):
        self.assert_asks([
            "echo '[core]' >> .gi?/config",
            "echo x > .?ithooks/pre-commit",
            "echo x | tee .{{git,x}}/config",
            "echo x > .c?aude/hooks/postelmd_guard.py",
            "echo x > .c?aude/settings.local.json",
            "echo x > .git[a-z]/x",
            "echo x > .c\\laude/settings.local.json",
            "echo x > *.md",
        ])

    def test_2_writers_that_run_code_or_write_elsewhere(self):
        self.assert_asks([
            "sed -i '1e sh evil.sh' README.md",
            "perl -pi -e 'BEGIN{{system q(sh evil.sh)}}' README.md",
            "sed -i 's/a/a/w .git/config' README.md",
            "sort -o .git/config README.md > notes.txt",
            "sed --sandbox -f x.sed -i README.md",
            "sed --sandbox -s -i s/a/b/ README.md",
            "sed --sandbox --follow-symlinks -i s/a/b/ README.md",
            "sort --output=x.txt README.md > notes.txt",
            "sort --compress-program=sh README.md > notes.txt",
            "uniq README.md out.txt > notes.txt",
        ])

    def test_3_protected_writes_the_guard_used_to_miss(self):
        self.assert_asks([
            "true; d=.git; echo '[core]' >> $d/config",
            "echo x > \"$OUT\"",
            "sed -n 'w .git/config' README.md",
            "sed -n '/x/w .githooks/pre-commit' README.md",
            "sed 's/a/b/w .git/config' README.md",
            "sort -o .git/config README.md",
            "sort -ro.git/config README.md",
            "uniq README.md .githooks/pre-commit",
        ])

    def test_4_printf_v_and_other_assignments(self):
        self.assert_asks([
            "for x in a; do printf -v x '%s%s/%s%s/config' . . . .; git status; "
            "cp README.md sub/$x; done",
            "for x in a; do printf -vx ..; git log -- sub/$x; done",
            "for x in a; do x=..; git log -- sub/$x; done",
            "for x in a; do let x=1; git log -- sub/$x; done",
            "for x in a; do unset x; git log -- sub/$x; done",
            "for x in a; do mapfile x < f; git log -- sub/$x; done",
            "for x in a; do getopts o x; git log -- sub/$x; done",
            "for f in b; do x=a; git log -- sub/$x; done",
            "for x in a; do zparseopts -A x; git log -- sub/$x; done",
            "for f in a; do printf -v IFS a; git log -- sub/$f; done",
        ])

    def test_5_split_names_of_the_opt_in(self):
        self.assert_asks([
            "mkdir -p ~/.con'fig'/postel'md' && printf x > ~/.config/postel'md'/guard-tr'ust'.json",
            "cp X ~/.config/postel\"md\"/gu\"ard-trust\".json",
            "cp X ~/.config/postel\\md/x.json",
            "x=con; cp X ~/.$x'fig'/a",
            "cp X $HOME/.c$x/a",
            "cp X ~/.c*/p*/g*.json",
            "cp X /opt/gua'rd-tr'ust.json",
        ])

    def test_expanded_targets_are_never_trusted(self):
        """protected_target asks first in the hook; the opt-in refuses them on its own too."""
        import os
        from unittest import mock
        import trust
        self.trust()
        with mock.patch.dict(os.environ, {"CLAUDE_PROJECT_DIR": self.repo}):
            trust.kinds.cache_clear()
            self.addCleanup(trust.kinds.cache_clear)
            for target in (".gi?/config", ".{git,x}/config", "*.md", "a[b].md", "a\\b.md"):
                with self.subTest(target=target):
                    self.assertFalse(trust.shell_writes_ok("echo", [target], self.repo))
            self.assertTrue(trust.shell_writes_ok("echo", ["notes.md"], self.repo))

    def test_sandboxed_sed_and_plain_tools_still_pass(self):
        self.trust()
        self.assert_cases([
            "sed --sandbox -i s/a/b/ README.md",
            "sed --sandbox -i -e 's/a/b/' -e 's/c/d/' sub/x.go",
            "sort README.md | uniq > notes.md",
            "grep -v x README.md > notes.md",
        ], None)


if __name__ == "__main__":
    import unittest
    unittest.main()
