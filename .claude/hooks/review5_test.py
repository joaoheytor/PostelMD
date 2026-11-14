"""Regression tests for the check of fbb6aa9: a keyword glued to a glob qualifier
(do(e:...:) is a qualifier when a file named do exists), zsh options set through a quoted
or declared argument, and zsh's chdir. Each "ask" case also passed on main.
Fixture: relax_test.Fixture. Run: make test-scripts
"""
import unittest

from relax_test import Fixture


class FifthRecheck(Fixture):
    def test_keywords_glued_to_a_paren_ask(self):
        self.assert_cases([
            "git log do(e:'touch X':)",
            "git log then(x)",
            "ls time(e:'id':)",
            "git log if(x)",
            "git log while(x)",
            "git log until(x)",
            "git log -1 do (x)",  # a keyword, but not in command position
        ], "ask")
        self.assert_cases([
            "while (false); do git status; done",
            "if true; then (git status); fi",
            "for x in a; do (git -C {r} status); done",
        ], None)

    def test_options_through_arguments_ask(self):
        self.assert_cases([
            "typeset 'options[globsubst]=on'; git status",
            "local options[globsubst]=on; git status",
            "declare \"options[x]\"=on; git status",
            "readonly options; git status",
            "export options; git status",
            "print -v 'options[globsubst]' on; git status",
            "echo 'options[x]'; git status",
        ], "ask")

    def test_chdir_asks(self):
        # zsh's chdir is cd, but bash has none: even into the project, the shells would run
        # git in different places (shells_test), so it asks like pushd.
        self.assert_cases([
            "chdir {f}/wt && git status",
            "builtin chdir {f}/wt && git status",
            "chdir {r}/sub && git status",
        ], "ask")


if __name__ == "__main__":
    unittest.main()
