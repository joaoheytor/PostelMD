"""Allowed commands that can be made to run other programs or write files (postelmd_guard.py).

.claude/settings.json allows `go test|build|vet`, `git diff|log|show|grep|fetch|pull --ff-only`,
`git rebase origin/main` and the repository's scripts by name. These options turn them into
something else, so the guard asks: go's -exec/-toolexec (and GOFLAGS), and -o, profiles or
-outputdir outside bin/ or a temp dir (they could overwrite a git hook); rebase -x; grep -O;
a long option not on the subcommand's list (gitopts.py: --output, --open-files-in-pager,
--exec and their abbreviations); fetch and pull in any form but
`[-q] [--prune] [--ff-only] [origin [<branch>]]` (--upload-pack runs a command); git -c,
--git-dir, ... before any subcommand, and GIT_* variables (GIT_CONFIG_*, GIT_SSH_COMMAND);
and `..` in a scripts/ path.
"""
import os
import re

from bash_writes import scratch
from gitopts import bad_long
from shparse import bare, git_parts

GO_EXEC = re.compile(r"--?(tool)?exec(=|$)")
GO_OUT = re.compile(r"--?(?:test\.)?(o|coverprofile|cpuprofile|memprofile|blockprofile"
                    r"|mutexprofile|trace|outputdir)(?:=(.*))?$")
FETCH_FLAGS = {"-q", "--quiet", "--prune", "-p"}


def go_output(args, cwd):
    """A reason to ask for a file go writes (-o, profiles, -outputdir), or None when it is
    under bin/ or a temp dir."""
    for i, a in enumerate(args):
        m = GO_OUT.match(a)
        if not m:
            continue
        target = m.group(2) if m.group(2) is not None else (args[i + 1] if i + 1 < len(args) else "")
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
    if not ok:
        return f"git options before {sub or 'the subcommand'} (-c, --git-dir, ...) can make git run commands"
    if sub in ("fetch", "pull"):
        return fetch_form(sub, args)
    if sub == "grep" and any(re.match(r"-[a-zA-Z]*O", a) for a in args):
        return "git grep -O runs a command"
    if sub == "rebase" and any(re.match(r"-[a-zA-Z]*x", a) for a in args):
        return "git rebase -x runs commands"
    return bad_long(sub, args)


ENV_OK = {"CGO_ENABLED", "IMAGE", "PM_E2E_PORT", "SMOKE_PORT", "SAMPLES", "CI"}
TOOLS = {"git", "go", "npm", "npx", "make"}
ASSIGN = re.compile(r"(\w+)=")


def env_check(segs):
    """Ask when a variable outside ENV_OK is set for git/go/npm/npx/make or a script: HOME,
    XDG_*, PATH, EDITOR, PAGER, SSH_ASKPASS, ... change which config and programs they run.
    Covers `VAR=x cmd`, `env VAR=x cmd`, and `VAR=x;` / `export VAR=x` before cmd."""
    earlier = []
    for words in segs:
        j, before = 0, []
        while j < len(words) and (ASSIGN.match(words[j]) or words[j] == "env"
                                  or (words[j].startswith("-") and "env" in words[:j])):
            before.append(words[j])
            j += 1
        tool = j < len(words) and (os.path.basename(words[j]) in TOOLS
                                   or re.match(r"(\./)?scripts/", words[j]))
        if not tool:
            if j == len(words) or words[0] in ("export", "declare", "typeset", "readonly"):
                earlier += [m.group(1) for m in map(ASSIGN.match, words) if m]
            continue
        flags = [w for w in before if w.startswith("-")]
        if flags:
            return f"env {flags[0]} before {words[j]}"
        bad = [n for n in earlier + [ASSIGN.match(w).group(1) for w in before if ASSIGN.match(w)]
               if n not in ENV_OK]
        if bad:
            return f"{bad[0]}= changes what {words[j]} reads or runs (allowed: {', '.join(sorted(ENV_OK))})"
    return None


def check(segs, cwd):
    """A reason to ask for any simple command in segs, or None."""
    why = env_check(segs)
    if why:
        return why
    for words in segs:
        if any(w.startswith("GOFLAGS=") for w in words):
            return "GOFLAGS can make go run other programs (-toolexec, -exec)"
        if any(re.match(r"GIT_\w*=", w) for w in words):
            return "GIT_* variables (GIT_CONFIG_*, GIT_SSH_COMMAND, ...) can make git run programs"
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
