"""Allowed commands that can be made to run other programs or write files (postelmd_guard.py).

.claude/settings.json allows `go test|build|vet`, `git diff|log|show|grep|fetch|pull --ff-only`,
`git rebase origin/main` and the repository's scripts by name. These options turn them into
something else, so the guard asks: go's -exec/-toolexec (and GOFLAGS) and -o outside bin/ or
a temp dir (it could overwrite a git hook); rebase's -x/--exec; diff/log/show --output (a
file the shell-write check can't see); grep -O/--open-files-in-pager; fetch and pull in any
form but `[-q] [--prune] [--ff-only] [origin [<branch>]]` (--upload-pack runs a command);
git -c/--git-dir/... before any of them; and `..` in a scripts/ path.
"""
import os
import re

from bash_writes import scratch
from shparse import bare, git_parts

GO_EXEC = re.compile(r"--?(tool)?exec(=|$)")
FETCH_FLAGS = {"-q", "--quiet", "--prune", "-p"}


def go_output(args, cwd):
    """A reason to ask for go's -o target, or None when it is under bin/ or a temp dir."""
    for i, a in enumerate(args):
        if a in ("-o", "--o"):
            target = args[i + 1] if i + 1 < len(args) else ""
        elif a.startswith(("-o=", "--o=")):
            target = a.partition("=")[2]
        else:
            continue
        path = os.path.normpath(os.path.join(cwd, os.path.expanduser(target)))
        in_bin = not os.path.isabs(target) and target.startswith("bin/") \
            and ".." not in target.split("/")
        if not target or not (in_bin or scratch(path, (os.environ.get("CLAUDE_PROJECT_DIR", ""),))):
            return f"go -o {target or '(none)'} writes outside bin/ or a temp dir"
    return None


def fetch_form(sub, args):
    allowed = FETCH_FLAGS | ({"--ff-only"} if sub == "pull" else set())
    pos = [a for a in args if a not in allowed]
    flags = [a for a in pos if a.startswith("-")]
    if flags:
        return f"git {sub} {flags[0]} (--upload-pack and --exec run commands)"
    if pos[:1] not in ([], ["origin"]) or len(pos) > 2 or any(":" in p or "+" in p for p in pos):
        return f"git {sub} takes only [origin [<branch>]]"
    return None


def git_check(words, cwd):
    ok, _, sub, args = git_parts(words, cwd)
    if not ok and sub in ("fetch", "pull", "grep", "diff", "log", "show", "rebase"):
        return f"git options before {sub} (-c, --git-dir, ...) can make git run commands"
    if sub in ("fetch", "pull"):
        return fetch_form(sub, args)
    if sub == "grep" and any(a.startswith("--open-files-in-pager") or re.match(r"-[a-zA-Z]*O", a)
                             for a in args):
        return "git grep -O/--open-files-in-pager runs a command"
    if sub == "rebase" and any(a.startswith("--exec") or re.match(r"-[a-zA-Z]*x", a)
                               for a in args):
        return "git rebase -x/--exec runs commands"
    if sub in ("diff", "log", "show") and any(a.startswith("--output") for a in args):
        return f"git {sub} --output writes a file"
    return None


def check(segs, cwd):
    """A reason to ask for any simple command in segs, or None."""
    for words in segs:
        if any(w.startswith("GOFLAGS=") for w in words):
            return "GOFLAGS can make go run other programs (-toolexec, -exec)"
        if any(re.match(r"(\./)?scripts/", w) and ".." in w for w in words):
            return "a scripts/ path with .. leaves the repository's scripts"
        words = bare(words)
        if not words:
            continue
        name = os.path.basename(words[0])
        if name == "go":
            if any(GO_EXEC.match(a) for a in words[1:]):
                return "go -exec/-toolexec runs another program"
            why = go_output(words[1:], cwd)
        else:
            why = git_check(words, cwd) if name == "git" else None
        if why:
            return why
    return None
