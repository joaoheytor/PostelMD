"""Tests for the six relaxations of the guard (chore/guard-relax), each with the cases that
must still ask or deny next to the ones that now pass. The hook judges, never runs, them in
a throwaway repository on branch `feature`; {w} is a worktree of it in a temp dir (a scratch
review worktree), {f}/wt a directory whose .git file points at a gitdir of its own.
Run: python3 -m unittest discover -s .claude/hooks -p '*_test.py'  (make test-scripts)
"""
import os
import shutil
import tempfile
import unittest

from postelmd_guard_test import git, new_repo, run_guard

SHA = "0123456789abcdef0123456789abcdef01234567"
KEY = "AKIA" + "Z3K7QW2PLM4XRT6B"  # built at run time: not a secret in this file
HOME = "/" + "home" + "/someone/notes.md"
API = "repos/joaoheytor/PostelMD"


class Fixture(unittest.TestCase):
    """The repository and files the relaxation tests judge commands in (no tests here).
    A local branch named `2` makes a dropped fd number look like a real refspec; {r}/L is a
    symlink into the foreign tree {f}/wt, {f}/gd/link one into the project, {f}/fifo a FIFO."""

    @classmethod
    def setUpClass(cls):
        cls.repo = new_repo()
        git(cls.repo, "commit", "-q", "--allow-empty", "-s", "-m", "chore: init")
        git(cls.repo, "remote", "add", "origin", "https://example.com/postelmd.git")
        git(cls.repo, "branch", "2")
        git(cls.repo, "switch", "-q", "-c", "feature")
        git(cls.repo, "update-ref", "refs/remotes/origin/feature", "HEAD")
        os.makedirs(os.path.join(cls.repo, "sub"))
        cls.files = tempfile.mkdtemp()
        cls.wt = os.path.join(cls.files, "review-1")
        git(cls.repo, "worktree", "add", "-q", "--detach", cls.wt)
        git(cls.files, "init", "-q", "--separate-git-dir", os.path.join(cls.files, "gd"),
            os.path.join(cls.files, "wt"))
        os.makedirs(os.path.join(cls.files, "wt", "inner"))
        os.symlink(os.path.join(cls.files, "wt", "inner"), os.path.join(cls.repo, "L"))
        # {f}/gd/link/.. is {f}/gd (a foreign gitdir) for cd, the project for chdir and -C
        os.symlink(os.path.join(cls.repo, "sub"), os.path.join(cls.files, "gd", "link"))
        os.mkfifo(os.path.join(cls.files, "fifo"))  # a message file that blocks a reader
        for name, text in (("clean.md", "## What\nA fix.\n"), ("secret.md", f"chore: x\n\n{KEY}\n")):
            with open(os.path.join(cls.files, name), "w", encoding="utf-8") as fh:
                fh.write(text)

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.repo)
        shutil.rmtree(cls.files)

    def decide(self, command):
        got = run_guard("git", {"cwd": self.repo, "tool_input": {"command": command}}, self.repo)
        return got and got["permissionDecision"]

    def assert_cases(self, cases, want):
        for command in cases:
            command = command.format(r=self.repo, w=self.wt, f=self.files, sha=SHA, key=KEY,
                                     home=HOME, api=API)
            with self.subTest(command=command):
                self.assertEqual(self.decide(command), want)


class Relaxations(Fixture):
    def test_1_read_only_gh_api(self):
        self.assert_cases([
            "gh api {api}/pulls/5/comments --paginate --jq '.[].body'",
            "gh api '{api}/actions/runs?per_page=5' -q '.workflow_runs[].status'",
            "gh api -X GET {api}/dependabot/alerts --jq length",
            "gh api --method GET 'repos/{{owner}}/{{repo}}/pulls/5'",  # unquoted { asks
            "gh api -i --cache 1h /{api}/commits/main",
            "gh api \"repos/{{owner}}/{{repo}}/code-scanning/alerts?ref=refs/pull/5/merge&state=open\"",
        ], None)
        self.assert_cases([
            "gh api -X DELETE {api}/git/refs/heads/x",
            "gh api --method PATCH {api}/pulls/5",
            "gh api -X get {api}/pulls",
            "gh api -X GET -f state=closed {api}/issues",
            "gh api {api}/issues -F title=x",
            "gh api {api}/issues --raw-field title=x",
            "gh api graphql -f query='{{ viewer {{ login }} }}'",
            "gh api repos/other/repo/pulls",
            "gh api repos/joaoheytor/PostelMD-fork/pulls",
            "gh api {api}/../../other/repo/pulls",
            "gh api https://api.github.com/{api}/pulls",
            "gh api --hostname evil.example {api}/pulls",
            "gh api -H 'Accept: x' {api}/pulls",
            "gh api --input body.json {api}/issues",
            "gh api {api}/pulls -R other/repo",
            "gh api {api}/pulls {api}/issues",
            "gh api {api}/pulls; rm -rf x",
            "cd {r} && gh api {api}/pulls",
        ], "ask")

    def test_2_literal_shell_variables(self):
        self.assert_cases([
            "S={r}; git -C $S log --oneline -3",
            "S={r} && git -C \"$S\" status --short",
            "B=origin/main; git diff --stat $B...HEAD",
            "S={r}\ncd $S && git status",
            "W={w}; cd ${{W}} && go test ./...",
            "S={f}; git diff origin/main > $S/c.diff",
            "S=feature; git push -u origin $S",
        ], None)
        self.assert_cases([
            "S=$(mktemp -d); git -C $S log",
            "S=`pwd`; git -C $S log",
            "export S={r}; git -C $S status",
            "S={r} git -C {r} status",               # an environment prefix: today's rules
            "HOME={f}; git status",
            "PAGER=x; git log",
            "S={f}/wt; git -C $S status",            # substituted, then judged: foreign
            "S={r}; S={f}/wt; git -C $S status",     # assigned twice
            "S={r}; for S in {f}/wt; do git -C $S status; done",
            "S={r}; git -C ${{S:-/}} status",
            "S={r}; git -C '$S' status",             # single quotes: a literal $S
            "true && S={r}; git -C $S status",       # not at the start: may not run
            "S=main; git push origin $S",            # judged as `git push origin main`
            "S=feature; git push origin $S; echo x",
            "S=.; echo x > $S/README.md",            # a shell write, now visible
        ], "ask")

    def test_3_heredoc_commit_messages(self):
        self.assert_cases([
            "git add -A && git commit -s -q -F - <<'EOF'\nchore: x\n\nA fix.\nEOF",
            "git commit -s -F - <<EOF\nfix(web): y\n\nPlain body.\nEOF",
            "git commit -s --file=- <<-'EOF'\n\tdocs: z\n\tEOF",
        ], None)
        self.assert_cases([
            "git commit -s -F - <<'EOF'\nchore: x\n\n{key}\nEOF",
            "git commit -s -F - <<'EOF'\nupdate stuff\nEOF",
            "git commit -s -F - <<'EOF'\n\nwip\nEOF",
            "git commit -F - <<'EOF'\nchore: x\nEOF",
        ], "deny")
        self.assert_cases([
            "git commit -s -F - <<EOF\nchore: x\n$(cat ~/x)\nEOF",
            "git commit -s -F - <<EOF\nchore: `id`\nEOF",
            "git commit -s -F - <<'EOF'\nchore: see {home}\nEOF",
            "git commit -s -F -",
            "git commit -s -F - < msg.txt",
            "cat <<'A' >/dev/null\nchore: x\nA\ngit commit -s -F - <<'EOF'\nnot conventional\nEOF",
            "git commit -s -F - ; cat <<'EOF'\nchore: x\nEOF",
        ], "ask")

    def test_4_trailing_output_filters(self):
        self.assert_cases([
            "git push -u origin feature 2>&1 | tail -3",
            "git push -u origin feature >/dev/null 2>&1",
            "git push -u origin feature 2>&1 | grep -v 'remote:' | head -n 5",
            "git push -u origin feature | cat",
            "git push -u origin feature 2>&1 | sort | uniq -c | wc -l",
            "gh pr merge 5 --squash --match-head-commit {sha} 2>&1 | tail -2",
            "gh pr create --title 'fix: x' --body-file {f}/clean.md 2>&1 | tail -1",
            "gh api {api}/pulls --jq '.[].number' | head -5",
        ], None)
        self.assert_cases([
            "git push -u origin feature | tee push.log",
            "git push -u origin feature | sh",
            "git push -u origin feature | xargs echo",
            "git push -u origin feature; rm -rf x",
            "git push -u origin feature > push.log",
            "git push -u origin feature 2>push.log",
            "git push -u origin feature 2>&1 | tail -3 && gh pr create --fill",
            "git push -u origin feature || true",
            "git push -u origin feature | sort -o out.txt",
            "git push -u origin feature | uniq - out.txt",
            "git push -u origin feature | sort --compress-program=sh",
            "git push -u origin feature | grep -f patterns.txt",
            "git push -u origin feature | tail -f",
            "git push -u origin feature | grep $(id)",
            "git push -u origin feature |& tail",
            "git push -u origin feature 2 >/dev/null",
            "git push origin main 2>&1 | tail -3",
            "gh api repos/o/r/pulls | head",
            "gh api {api}/pulls | jq .",
        ], "ask")

    def test_5_fetching_pr_refs(self):
        self.assert_cases([
            "git fetch origin pull/116/head:pr116-review",
            "git fetch -q origin pull/122/head:refs/remotes/review/pr122",
            "git fetch -q origin +pull/122/head:refs/remotes/review/pr122",
            "git fetch origin +pull/98/head:refs/scratch/pr98",
            "git fetch origin pull/5/head:pr5",
            "git fetch origin pull/116/head:pr116 2>&1 | tail -2; git log --oneline -3 pr116",
        ], None)
        self.assert_cases([
            "git fetch origin pull/5/head:main",
            "git fetch origin pull/5/head:Master",
            "git fetch origin pull/5/head:refs/heads/main",
            "git fetch origin pull/5/head:refs/tags/x",
            "git fetch origin pull/5/head:tags/x",
            "git fetch origin pull/5/head:HEAD",
            "git fetch origin +pull/5/head:pr5",
            "git fetch origin pull/5/head:refs/remotes/origin/main",
            "git fetch origin pull/5/head:refs/remotes/review/../../heads/main",
            "git fetch other pull/5/head:pr5",
            "git fetch origin pull/5/head:x pull/6/head:y",
            "git fetch --upload-pack=x origin pull/5/head:pr5",
            "git fetch origin pull/5/merge:pr5",
            "git fetch origin refs/heads/main:pr5",
        ], "ask")

    def test_6_cd_before_git(self):
        self.assert_cases([
            "cd {r} && git log -1",
            "cd {r}/sub && git status",
            "cd {w} && git status && git diff --stat origin/main",
            "S={w}; cd $S && git log --oneline -1",
        ], None)
        self.assert_cases([
            "cd $(mktemp -d) && git status",
            "cd {f}/wt && git status",
            "S={f}/wt; cd $S && git status",
            "cd - && git status",
            "pushd {r} && git status",
            "cd $UNKNOWN_DIR && git status",
            "for d in {f}/wt; do git -C $d status; done",   # git after a shell keyword
            "if true; then git -C {f}/wt status; fi",
            "cd {r} && git push -u origin feature",
            "S={r}; cd $S && gh pr merge 5 --squash --match-head-commit {sha}",
        ], "ask")


if __name__ == "__main__":
    unittest.main()
