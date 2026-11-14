"""Regression tests for the third security re-check of #176 (9201b2b): zsh modifiers after
substituted variables, zsh =cmd, subcommands built at run time, wrappers and keywords
before gated actions, zsh two-argument cd, jq on every gh command, make and go options
that run programs, and eval or a shell's -c. Each "ask" case passed silently at 9201b2b
(some also on main). Fixture: relax_test.Fixture. Run: make test-scripts
"""
import unittest

from relax_test import Fixture


class ThirdRecheck(Fixture):
    def test_zsh_modifiers_after_variables_ask(self):
        self.assert_cases([
            "y=feat; git log -1 --format=%B $y:s,feat,--output=.githooks/pre-commit,",
            "y=feat; git diff $y:s,feat,--output=.git/hooks/pre-commit,",
            "y=feat; gh pr comment 5 --body-file $y:s,feat,/etc/hostname,",
            "y=feat; git push origin $y:s,feat,main,",
            "x={api}/issues; gh api $x:s/joaoheytor/cli/:s/PostelMD/cli/",
            "x=AKIAQ7Q7; gh pr comment 5 --body $x:s/Q7/Q7Q7/",
            "x=AKIAQ7Q7; git commit -s -m \"fix: leak $x:s/Q7/Q7Q7/\"",
            "x={api}/z; gh api $x:h:h",
            "x={api}/z; gh api $x:a",
            "x=feat/main; git push origin $x:t",
            "x=br; git push --force-with-lease=$x:h origin br",
            "x=main; git push origin ${{~x}}",
            "x=a; git log $x[1]",
            "x=a; git log ${{x:-y}}",
            "x=a; git log $x(:)",
        ], "ask")
        self.assert_cases([
            "S={r}; git -C $S/sub status",
            "S={r}; git -C \"$S\" log -1",
            "B=origin/main; git diff --stat $B...HEAD",
            "x=feat; git log ${{x}}:s/feat/--output=y/",  # after the brace: literal in zsh too
        ], None)

    def test_zsh_equals_and_comments_ask(self):
        self.assert_cases([
            "=gh pr close 5",
            "=gh api {api}/git/refs -X POST",
            "=git push origin main",
            "ls # a comment, no tool",
            "echo =x",
        ], "ask")

    def test_run_time_subcommands_ask(self):
        self.assert_cases([
            "git $(echo push) origin main",
            "git `echo push` origin main",
            "for x in push; do git $x origin main; done",
            "set -- push origin main; git $1 $2 $3",
            "read x <<< push; git $x origin main",
            "f() ( git \"$@\" ); f push origin main",
            "git${{IFS}}push origin main",
            "git log -1 $HOME",
            "git log -1 \"$PWD\"",
            "f() (git log -1); f",
        ], "ask")
        self.assert_cases([
            "git commit -s -m \"$(cat <<'EOF'\nchore: x\n\nA body.\nEOF\n)\"",
            "make lint; echo exit=$?",
            "for w in a b; do git -C {r} log -1; done",
        ], None)

    def test_wrappers_and_keywords_before_gated_actions_ask(self):
        self.assert_cases([
            "nohup gh pr close 5",
            "time gh pr review 5 --approve",
            "exec gh release create v9",
            "nice git tag v9",
            "coproc gh pr close 5",
            "if true; then gh pr close 5; fi",
            "while false; do gh release create v9; done",
            "git log -1 >(nohup gh release create v9)",
            "git log -1 <(git status)",
            "command gh repo delete o/r",
            "timeout 5 gh workflow run ci.yml",
        ], "ask")

    def test_cd_forms_the_guard_cant_follow_ask(self):
        self.assert_cases([
            "cd sub evil && git status --short",
            "cd {r} {f} && git status",
            "cd +1 && git status",
            "cd -1 && git status",
            "CDPATH={f}; cd wt && git status",
        ], "ask")
        self.assert_cases([
            "cd -P {r} && git status",
            "cd {r}/sub && git status",
        ], None)

    def test_jq_on_every_gh_command_asks(self):
        self.assert_cases([
            "gh pr view 176 --json number --jq '[env|length]'",
            "gh pr list --jq '$ENV'",
            "gh pr view 5 -q env",
            "gh run view 5 --json jobs --jq input",
            "gh pr view 5 --template '{{{{env}}}}'",
            "gh pr checks 5 --json name -t '{{{{.name}}}}{{{{env}}}}'",
        ], "ask")
        self.assert_cases([
            "gh pr view 5 --json state --jq .state",
            "gh pr create -t 'fix: env handling' --body-file {f}/clean.md",
        ], None)

    def test_make_and_go_options_that_run_programs_ask(self):
        self.assert_cases([
            "make test --eval='x:=$(shell git push origin main)'",
            "make -E 'x:=1' test",
            "make -f evil.mk test",
            "make --file=evil.mk test",
            "make --makefile evil.mk test",
            "make test SHELL=./evil",
            "make test .SHELLFLAGS=-c",
            "make test GO=./evil",
            "make -C /tmp test",
            "make image IMAGE='x;id'",
            "IMAGE='x;id' make image",
            "go vet -vettool=/tmp/x.sh ./...",
            "go build -overlay=o.json ./...",
            "go build -ldflags=-extld=./evil ./...",
            "go test -modfile=evil.mod ./...",
        ], "ask")
        self.assert_cases([
            "make ci IMAGE=postelmd:x PM_E2E_PORT=4100 SMOKE_PORT=4200",
            "make -j4 test",
            "make -C sub test",
            "go test -race -run TestSave ./internal/...",
            "go build -o bin/postelmd ./cmd/postelmd",
        ], None)

    def test_eval_and_shell_c_ask(self):
        self.assert_cases([
            "eval git push origin main",
            "eval \"git push origin main\"",
            "bash -c 'git push origin main'",
            "sh -c 'gh pr close 5'",
            "zsh -c 'git log --output=x'",
            "bash -lc 'git status'",
            "alias g=git\ng push origin main",
            "source ./x.sh",
            ". ./x.sh",
        ], "ask")


if __name__ == "__main__":
    unittest.main()
