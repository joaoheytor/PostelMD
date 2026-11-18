"""Repros from the third security review of #195, each a test: without the opt-in the guard
asks, and with every kind trusted it still asks. Fixture: trust_test.TrustFixture ({r}/L is
a symlink out of the project).
Run: make test-scripts
"""
from trust_test import ALL, TrustFixture


class ThirdReview(TrustFixture):
    def assert_asks(self, cases):
        for kinds in (None, ALL):
            if kinds is None:
                self.untrust()
            else:
                self.trust(kinds)
            with self.subTest(trusted=bool(kinds)):
                self.assert_cases(cases, "ask")

    def test_1_sed_backup_suffixes(self):
        self.assert_asks([
            "sed --sandbox -i'.git/*' s/a/a/ config",
            "sed --sandbox --in-place='.githooks/*' s/a/a/ pre-commit",
            "sed --sandbox -i'*.bak' s/a/a/ README.md",
            "sed --sandbox -i'x/y' s/a/a/ README.md",
            "sed --sandbox -Ei'.git/*' s/a/a/ config",
            "sed --sandbox -i'{r}/.git/hooks/*' s/a/a/ {f}/clean.md",  # only the backup is ours
        ])

    def test_2_zsh_clobber_redirects(self):
        self.assert_asks([
            "echo '[core]' >! .git/config",
            "echo '[core]' >>! .git/config",
            "echo '[core]' &>! .git/config",
            "echo '[core]' &>>! .git/config",
            "echo '[core]' >| .git/config",
            "echo x >! notes.md",
        ])

    def test_3_zsh_modifiers(self):
        self.assert_asks([
            "for x in a; do git status; cp README.md sub/$x:s/a/./$x:s/a/./; done",
            "for x in a; do git log -- sub/$x:a; done",
            "for x in a; do git log -- sub/$x:h; done",
            "for x in a; do git log -- sub/$x[1]; done",
            "for x in a; do git log -- sub/${{x%a}}; done",
            "for x in a; do git log -- sub/${{x:-..}}; done",
        ])

    def test_4_reading_outside_the_project(self):
        self.assert_asks([
            "cat ~/.ssh/id_ed25519 > notes.md",
            "cat /etc/hostname > notes.md",
            "cat ../x > notes.md",
            "grep -R . L > notes.md",
            "grep -r x L > notes.md",
            "grep -R x sub > notes.md",
            "grep -f /etc/hostname README.md > notes.md",
            "tee -a notes.md < /etc/hostname",
            "cat L/x > notes.md",
            "cat sub/* > notes.md",
            "head -n 3 {f}/clean.md > notes.md",
        ])

    def test_5_files0_from_reads_the_files_listed_inside(self):
        self.assert_asks([
            "sort --files0-from=list.txt > notes.md",
            "sort --files0-from list.txt > notes.md",
            "wc --files0-from=list.txt > notes.md",
            "wc --files0-from list.txt > notes.md",
            "wc --f list.txt > notes.md",  # getopt takes any unambiguous prefix
            "sort --fil=list.txt > notes.md",
        ])

    def test_reading_inside_the_project_still_passes(self):
        self.trust()
        self.assert_cases([
            "cat README.md sub/x.go > notes.md",
            "grep -r TODO sub > notes.md",
            "grep -e 'a.*b' README.md > notes.md",
            "tee -a notes.md < README.md",
            "sed --sandbox -i.bak s/a/b/ README.md",
            "sed --sandbox --in-place s/a/b/ README.md",
            "echo /etc/hostname > notes.md",
        ], None)


if __name__ == "__main__":
    import unittest
    unittest.main()
