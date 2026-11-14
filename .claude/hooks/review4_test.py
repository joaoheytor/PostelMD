"""Regression tests for the security re-check of 867c07d: cd resolving `..` textually,
zsh glob qualifiers and option changes, redirects that write (<>, >>|), message files that
block (FIFOs), and zsh precommand words. Each "ask" case passed silently at 867c07d (the
first also on main asked; the rest were open there too). Fixture: relax_test.Fixture.
Run: make test-scripts
"""
import unittest

from relax_test import Fixture


class FourthRecheck(Fixture):
    def test_cd_resolves_dotdot_as_the_shell_does(self):
        self.assert_cases([
            "cd {f}/gd/link/.. && git status --short",  # the shell: {f}/gd, a foreign gitdir
            "cd {f}/gd/link/.. && git fetch origin",
            "cd {r}/L/.. && git status",  # cd -P or chdir: {f}/wt
        ], "ask")
        self.assert_cases([
            "cd -P {f}/gd/link/.. && git status",  # physical: the project
            "git -C {f}/gd/link/.. status",  # chdir resolves the link: the project
            "(cd {r}/sub && git status)",
        ], None)

    def test_zsh_glob_qualifiers_and_options_ask(self):
        self.assert_cases([
            "git log -1 -- README.md(e+'git push origin HEAD:main'+)",
            "git status(e+'git tag v1'+)",
            "ls README.md(e+'git push origin HEAD:main'+)",
            "gh pr view 1 --json title(e+'printenv'+)",
            "go test ./internal/x(e:'touch x':)",
            "git log -1 -- README.md(#qe+'git tag v1'+)",
            "make test README.md(e+'true'+)",
            "setopt bareglobqual; git log -1",
            "unsetopt nomatch; git status",
            "emulate zsh; git status",
            "options[bareglobqual]=on; git status",
            "set -o bareglobqual; git status",
            "set +o noglob; ls",
        ], "ask")
        self.assert_cases([
            "git log -1 && (git status)",
            "if (true); then git status; fi",
        ], None)

    def test_redirects_that_write_ask(self):
        self.assert_cases([
            "git log -1 <> .githooks/pre-commit",
            "git log -1 1<>.githooks/pre-commit",
            "git log -1 >>| .githooks/pre-commit",
            "git log -1 >& .githooks/pre-commit",
            "git log -1 &>> .githooks/pre-commit",
            "git log -1 > /dev/null > .githooks/pre-commit",
        ], "ask")
        self.assert_cases([
            "git log -1 >/dev/null 2>&1",
            "git log -1 2>&1 | tail -3",
        ], None)

    def test_message_files_that_block_ask(self):
        self.assert_cases([
            "git commit -s -F {f}/fifo",
            "gh pr comment 5 --body-file {f}/fifo",
            "gh pr create -t 'fix: x' --body-file {f}/gd",  # a directory
        ], "ask")

    def test_precommand_words_dont_hide_gated_commands(self):
        self.assert_cases([
            "noglob gh pr close 5",
            "nocorrect gh release create v9",
            "- gh pr close 5",
            "builtin exec gh pr close 5",
            "repeat 2 gh pr close 5",
            "noglob git tag v9",
        ], "ask")


if __name__ == "__main__":
    unittest.main()
