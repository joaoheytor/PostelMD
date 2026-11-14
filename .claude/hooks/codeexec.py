"""Allowed commands that can be made to run other programs or write files (postelmd_guard.py).

.claude/settings.json allows `go test|build|vet`, `git diff|log|show|grep|fetch|pull --ff-only`,
`git rebase origin/main` and the repository's scripts by name. These options turn them into
something else, so the guard asks: go's -exec/-toolexec/-vettool/-overlay/-modfile, an
-ldflags naming an external linker (and GOFLAGS); make with anything but targets, -j/-k/-s/
-n/-B/-O, -C into the project and ENV_OK=<plain value> (make_check: -f, --eval, SHELL=
replace the recipes); an ENV_OK value with ; $ or spaces; and go's -o, profiles or
-outputdir outside bin/ or a temp dir (they could overwrite a git hook); rebase -x; grep -O;
a long option not on the subcommand's list (gitopts.py: --output, --open-files-in-pager,
--exec and their abbreviations); fetch and pull in any form but
`[-q] [--prune] [--ff-only] [origin [<branch>]]` (--upload-pack runs a command) or
`git fetch [-q] origin [+]pull/<N>/head:<dst>` (pr_refspec: dst under refs/remotes/review/
or refs/scratch/, where + may force, or a local pr<N>... name with no `/`); git -c,
--git-dir, ... before any subcommand, and GIT_* variables (GIT_CONFIG_*, GIT_SSH_COMMAND);
and `..` in a scripts/ path.
"""
import os
import re

from bash_writes import scratch
from gitopts import bad_long
from shparse import bare, git_parts

GO_EXEC = re.compile(r"--?(tool)?exec(=|$)")
# go flags that run or swap in programs and files: -vettool, -overlay (replaces source
# files), -modfile, and -ldflags/-gcflags/... naming an external linker or tool.
GO_RUNS = re.compile(r"--?(vettool|overlay|modfile)(=|$)"
                     r"|--?(ld|gc|asm|gccgo)flags\b.*(ext|tool|plugin)", re.S)
SAFE_VALUE = re.compile(r"[\w.:/@%+,-]*")  # IMAGE=postelmd:x; no ; $ ` spaces or quotes
MAKE_FLAGS = re.compile(r"-j\d*|-[ksnB]+|--(keep-going|silent|no-print-directory|jobs=\d+)"
                        r"|-O\w*|--output-sync(=\w+)?")
MAKE_TARGET = re.compile(r"[A-Za-z0-9_][\w.-]*")
GO_OUT = re.compile(r"--?(?:test\.)?(o|coverprofile|cpuprofile|memprofile|blockprofile"
                    r"|mutexprofile|trace|outputdir)(?:=(.*))?$")
FETCH_FLAGS = {"-q", "--quiet", "--prune", "-p"}
PR_REFSPEC = re.compile(r"(\+?)pull/\d+/head:([A-Za-z0-9][A-Za-z0-9._/-]*)")


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


def pr_refspec(ref):
    """True for pull/<N>/head:<dst> into refs/remotes/review/<x> or refs/scratch/<x> (+ may
    force), or a new local branch named pr<N>... with no `/` (no +). Never origin/main,
    refs/heads/origin/main, a tag-like name or an existing branch such as the agent's own."""
    m = PR_REFSPEC.fullmatch(ref)
    if not m or ".." in ref or ref.endswith((".lock", "/")) or "//" in ref:
        return False
    dst = m.group(2)
    if re.fullmatch(r"refs/(remotes/review|scratch)/[A-Za-z0-9][\w./-]*", dst):
        return True
    return not m.group(1) and bool(re.fullmatch(r"pr\d+[\w-]*", dst))


def fetch_form(sub, args):
    allowed = FETCH_FLAGS | ({"--ff-only"} if sub == "pull" else set())
    pos = [a for a in args if a not in allowed]
    flags = [a for a in pos if a.startswith("-")]
    if flags:
        return f"git {sub} {flags[0]} (--upload-pack and --exec run commands)"
    if sub == "fetch" and len(pos) == 2 and pos[0] == "origin" and pr_refspec(pos[1]):
        return None
    if pos[:1] not in ([], ["origin"]) or len(pos) > 2 or any(":" in p or "+" in p for p in pos):
        return f"git {sub} takes only [origin [<branch>]] or origin pull/<N>/head:<review ref>"
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
        bad += [w.partition("=")[0] for w in before  # IMAGE='x;id' reaches a recipe's shell
                if ASSIGN.match(w) and not SAFE_VALUE.fullmatch(w.partition("=")[2])]
        if bad:
            return f"{bad[0]}= changes what {words[j]} reads or runs (allowed: {', '.join(sorted(ENV_OK))})"
    return None


def make_dir(target, cwd):
    """True when make -C target stays in the project."""
    project = os.path.realpath(os.environ.get("CLAUDE_PROJECT_DIR") or "/nonexistent")
    if not target or "$" in target or not isinstance(cwd, str):
        return False
    return (os.path.realpath(os.path.join(cwd, target)) + "/").startswith(project + "/")


def make_check(args, cwd):
    """A reason to ask for make arguments: only targets, ENV_OK=<plain value>, a few flags
    and -C into the project. -f/--file/--makefile, -E/--eval, -I, SHELL=, .SHELLFLAGS=, GO=
    and other variables replace the recipes the Makefile runs."""
    i = 0
    while i < len(args):
        a = args[i]
        if a in ("-C", "--directory") or a.startswith(("-C", "--directory=")):
            target = a.partition("=")[2] if a.startswith("--") else a[2:]
            if a in ("-C", "--directory"):
                target, i = (args[i + 1] if i + 1 < len(args) else ""), i + 1
            if not make_dir(target, cwd):
                return f"make -C {target} outside the project"
        elif a.startswith("-"):
            if not MAKE_FLAGS.fullmatch(a):
                return f"make {a[:20]} (-f, --eval, -I and the like change what make runs)"
        elif "=" in a:
            name, _, value = a.partition("=")
            if name not in ENV_OK or not SAFE_VALUE.fullmatch(value):
                return (f"make {name[:20]}= replaces what the recipes run (allowed: "
                        f"{', '.join(sorted(ENV_OK))} with plain values)")
        elif not MAKE_TARGET.fullmatch(a):
            return f"make target {a[:20]}"
        i += 1
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
            if any(GO_EXEC.match(a) or GO_RUNS.match(a) for a in words[1:]):
                return ("go -exec, -toolexec, -vettool, -overlay, -modfile or an external "
                        "linker runs or swaps in other programs")
            why = go_output(words[1:], cwd)
        elif name == "make":
            why = make_check(words[1:], cwd)
        else:
            why = git_check(words, cwd) if name == "git" else None
        if why:
            return why
    return None
