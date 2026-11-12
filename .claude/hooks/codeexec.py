"""Allowed commands that can be made to run other programs or write files (postelmd_guard.py).

.claude/settings.json allows `go test|build|vet`, `git diff|log|show`, `git rebase origin/main`
and the repository's scripts by name. These options turn them into something else, so the
guard asks: go's -exec/-toolexec (and GOFLAGS), rebase's -x/--exec, diff/log/show --output
(writes a file the shell-write check can't see), and `..` in a scripts/ path.
"""
import os
import re

from shparse import bare, git_parts

GO_EXEC = re.compile(r"--?(tool)?exec(=|$)")


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
        if name == "go" and any(GO_EXEC.match(a) for a in words[1:]):
            return "go -exec/-toolexec runs another program"
        if name != "git":
            continue
        _, _, sub, args = git_parts(words, cwd)
        if sub == "rebase" and any(a.startswith("--exec") or re.match(r"-[a-zA-Z]*x", a)
                                   for a in args):
            return "git rebase -x/--exec runs commands"
        if sub in ("diff", "log", "show") and any(a.startswith("--output") for a in args):
            return f"git {sub} --output writes a file"
    return None
