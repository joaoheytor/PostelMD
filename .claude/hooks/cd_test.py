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


if __name__ == "__main__":
    unittest.main()
