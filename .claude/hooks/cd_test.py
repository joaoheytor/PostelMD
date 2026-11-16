"""Tests for `cd` inside the project (chore/guard-cd). settings.json allows `Bash(cd:*)`, so
the guard asks for any cd, in any command, whose target may leave the project
(CLAUDE_PROJECT_DIR): checked at the logical target (L/.. as text) and the physical one,
from the hook's cwd. Worktrees under .claude/worktrees/ are inside. Fixture:
relax_test.Fixture ({r} the project, {i} a worktree under it, {r}/L a symlink out of it).
Run: make test-scripts
"""
import unittest

from relax_test import Fixture


class CdInsideTheProject(Fixture):
    def test_cd_inside_the_project_passes(self):
        self.assert_cases([
            "cd sub && ls",
            "cd {r}/sub && npm test",
            "cd {r} && ls",
            "cd sub && cd .. && ls",
            "cd -P sub && ls -la",
            "cd -- sub && ls",
            "(cd sub && ls) && ls",
            "S=sub; cd $S && ls",
            "cd {i} && ls",
            "cd .claude/worktrees/agent-1 && git status --short",
            "echo 'run `cd /tmp` once'",  # single quotes: text, never run
            "git commit -s -F - <<'EOF'\nchore: x\n\nRun `cd web && npm ci` once.\nEOF",
        ], None)

    def test_cd_that_may_leave_the_project_asks(self):
        self.assert_cases([
            "cd .. && ls",  # from the project root
            "cd sub; cd ../.. && ls",
            "cd sub; cd .. && ls",  # if cd sub fails, cd .. leaves the root
            "cd sub && cd .. && cd .. && ls",
            "cd sub || cd .. && ls",
            "cd /tmp && ls",
            "cd {f} && ls",
            "cd {w} && ls",  # a worktree, but outside the project
            "cd && ls",
            "cd ~ && ls",
            "cd - && ls",
            "cd $HOME && ls",
            "cd $(mktemp -d) && ls",
            "cd L && ls",  # a symlink out of the project
            "cd L/.. && ls",  # logical: the project; physical: outside
            "cd sub evil && ls",
            "cd +1 && ls",
            "CDPATH={f}; cd wt && ls",
            "pushd sub && ls",
            "popd && ls",
            "chdir sub && ls",
            "ls && cd /etc",
            "echo x; (cd /tmp; ls)",
            "ls `cd /tmp; pwd`",
            "echo \"`cd /tmp; pwd`\"",  # double quotes: the backticks run
        ], "ask")

    def test_security_review_cases_ask(self):
        self.assert_cases([
            # 1. a quoted or escaped command name is still cd
            "'c'd /tmp && npm test",
            "\"c\"d /tmp && go test ./...",
            "c\"\"d /tmp && make test",
            "c\\d /tmp && ls",
            "'c'd sub && ls",
            # 2. ! makes a failing cd succeed: not a plain && chain
            "! cd nosuch && cd .. && pwd",
            # 3. globs in a cd argument (L is a symlink out of the project)
            "cd [L] && pwd -P",
            "cd ? && pwd -P",
            "cd L* && pwd -P",
            "cd {{L,x}} && pwd -P",
            # 4. zsh's lowercase cdpath, however it is set
            "cdpath=/tmp; cd claude-1000 && pwd",
            "cdpath=/x cd y && ls",
            "cdpath+=(/tmp); cd sub",
            "typeset -T CDPATH cdpath; cd sub",
            "export cdpath=/tmp; cd sub",
            # 5. cd in a command substitution the shell runs
            "echo \"$(cd L && cat f)\"",
            "cat <<EOF\n$(cd L && cat f)\nEOF",
            "x=\"`cd L && pwd`\"",
            # 6. tilde forms: the directory stack, named directories
            "cd ~- && ls",
            "cd ~+ && ls",
            "cd ~1 && ls",
            "cd ~-1 && ls",
            "cd ~root && ls",
        ], "ask")
        self.assert_cases([
            "cat <<'EOF'\n$(cd L && cat f)\nEOF",  # a quoted heredoc: text
            "cd sub && ls *.md",  # the glob is ls's, not cd's
            "! cd sub && ls",  # either way inside
        ], None)


if __name__ == "__main__":
    unittest.main()
