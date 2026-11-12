"""Which `git push` and `gh pr merge` calls need the maintainer (postelmd_guard.py, git mode).

Routine branch work runs without a prompt: a push of a branch other than main/master to
origin (plain, -u, --force-with-lease), and a squash merge pinned to the reviewed head
(`gh pr merge N --squash --match-head-commit <40-hex sha>`). Everything else these commands can
do asks: a push whose destination (the refspec's, or the current branch's and its upstream's
when there is no refspec) is main/master, a tag or not a local branch; --force/-f/+refspec
without a lease, --mirror, --all, --tags, --delete/:branch, any other option, a remote other
than origin, git -c/--git-dir or GIT_* variables; a merge without both pins, or with --admin
or --repo.

The command is split into simple commands (bash_writes.tokens: quotes, heredocs). `cd DIR`
and `git -C DIR` move the directory the current branch is read in. When the parser can't
follow (an unknown directory, a push the regex sees but the parser doesn't, xargs), it asks.
A guardrail, not a boundary: GitHub's branch protection is the hard gate.
"""
import os
import re

from bash_writes import PREFIXES, REDIRECTS, is_punct, strip_heredocs, tokens

# A command position: start of the command, or after ; & | ( or a newline, optionally after
# VAR=value assignments or sudo/env/command/xargs. Anchoring here keeps text inside
# heredocs, strings and file contents ("git commit -s" in docs) from matching.
CMD_START = r"(?:^|[;&|(]|\n)\s*(?:\w+=\S*\s+)*(?:(?:sudo|env|command|xargs)\s+)*"
# `git` followed by global options (-C dir, -c k=v, --git-dir=..., --no-pager, ...)
GIT = CMD_START + r"git(?:\s+(?:-[Cc]\s+\S+|--[\w-]+(?:=\S+)?|-[pP]))*\s+"
GH = CMD_START + r"gh\s+"
PROTECTED = {"main", "master"}
SHA40 = re.compile(r"[0-9a-f]{40}")
PUSH_FLAGS = {"-u", "--set-upstream", "--force-if-includes", "-q", "--quiet", "-v",
              "--verbose", "--progress", "--no-progress", "-n", "--dry-run", "--porcelain"}
# Earlier git commands in the same call that can change the branch or remote a push uses.
RETARGET = {"switch", "checkout", "branch", "config", "remote", "symbolic-ref", "update-ref",
            "worktree", "init", "clone"}
UNKNOWN = object()  # a directory the parser can't follow (cd -, cd $X, a subshell)


def segments(cmd):
    """(words, cwd-affecting?) for each simple command; redirects and their targets dropped.
    The flag is False for a command in a pipeline, where `cd` runs in a subshell."""
    out, words, toks, i, piped = [], [], tokens(cmd), 0, False
    while i < len(toks):
        tok = toks[i]
        nxt = toks[i + 1] if i + 1 < len(toks) else ""
        if tok in REDIRECTS or tok in (">&", "<", "<<<"):
            i += 2
            continue
        if is_punct(tok):
            out.append((words, not (piped or tok == "|")))
            words, piped = [], tok == "|"
            if ")" in tok:
                out.append((["cd", "$SUBSHELL"], True))  # the subshell's cd ends: unknown
        elif not (re.fullmatch(r"\d+", tok) and nxt in REDIRECTS | {">&", "<"}):
            words.append(tok)
        i += 1
    out.append((words, not piped))
    return [(w, f) for w, f in out if w]


def strip_prefixes(words):
    """(words without VAR=... and sudo/env/... prefixes, reason to ask or None)."""
    why = None
    while words and (words[0] in PREFIXES or re.match(r"\w+=", words[0])):
        if words[0] == "xargs":
            why = "xargs appends arguments the guard can't see"
        elif words[0].startswith("GIT_"):
            why = f"{words[0].split('=')[0]} changes what git pushes"
        words = words[1:]
    return words, why


def move(cwd, target):
    if cwd is UNKNOWN or not target or "$" in target or "`" in target or target == "-":
        return UNKNOWN
    return os.path.normpath(os.path.join(cwd, os.path.expanduser(target)))


def git_args(words, cwd):
    """(subcommand args, directory, reason to ask) for `git [global options] sub ...`."""
    i = 1
    while i < len(words) and words[i].startswith("-"):
        w = words[i]
        if w == "-C" and i + 1 < len(words):
            cwd, i = move(cwd, words[i + 1]), i + 2
        elif w in ("--no-pager", "-p", "--paginate", "-P", "--no-optional-locks"):
            i += 1
        else:  # -c k=v, --git-dir, --work-tree, --namespace...: they change what is pushed
            return words[i:], cwd, f"git option {w} before push"
    return words[i:], cwd, None


def branch_dest(ref, cwd, git_out):
    """The remote branch a refspec writes, or (None, reason)."""
    if ref.startswith("+"):
        return None, f"force refspec {ref} (use --force-with-lease)"
    if "*" in ref:
        return None, f"wildcard refspec {ref}"
    src, _, dst = ref.partition(":")
    if not src:
        return None, f"refspec {ref} deletes a remote branch"
    if src in ("HEAD", "@"):
        src_branch = current_branch(cwd, git_out)
    elif src.startswith("refs/") and not src.startswith("refs/heads/"):
        src_branch = None
    else:
        name = src.removeprefix("refs/heads/")
        ok = git_out(cwd, "show-ref", "--verify", "-q", f"refs/heads/{name}") is not None
        src_branch = name if ok else None
    if not src_branch:
        return None, f"{src} is not a local branch"
    dst = dst or src_branch
    if dst.startswith("refs/") and not dst.startswith("refs/heads/"):
        return None, f"refspec {ref} writes {dst}, not a branch"
    return dst.removeprefix("refs/heads/"), None


def current_branch(cwd, git_out):
    name = (git_out(cwd, "rev-parse", "--abbrev-ref", "HEAD") or "").strip()
    return None if name in ("", "HEAD") else name


def check_push(args, cwd, git_out):
    """A reason to ask for `git push ARGS` run in cwd, or None."""
    if cwd is UNKNOWN:
        return "git push from a directory the guard can't follow"
    rest = []
    for a in args:
        if a.startswith("--force-with-lease") or a in PUSH_FLAGS or re.fullmatch(r"-[uqvn]+", a):
            continue
        if a.startswith("-"):
            return f"git push {a}"
        rest.append(a)
    if rest and rest[0] != "origin":
        return f"git push to {rest[0]}, not origin"
    for ref in rest[1:]:
        dst, why = branch_dest(ref, cwd, git_out)
        if why:
            return f"git push: {why}"
        if dst in PROTECTED:
            return f"git push to {dst}"
    if rest[1:]:
        return None
    branch = current_branch(cwd, git_out)
    if not branch:
        return "git push with no refspec on a detached or unreadable HEAD"
    if branch in PROTECTED:
        return f"git push of the current branch {branch}"
    for rev in ("@{upstream}", "@{push}"):
        up = (git_out(cwd, "rev-parse", "--abbrev-ref", "--symbolic-full-name", rev) or "").strip()
        remote, _, name = up.partition("/")
        if up and (remote != "origin" or name in PROTECTED):
            return f"git push of {branch}, whose {rev} is {up}"
    return None


def check_merge(args):
    """A reason to ask for `gh pr merge ARGS`, or None when it's a pinned squash merge."""
    pinned = any(a.startswith("--match-head-commit=") and SHA40.fullmatch(a.split("=", 1)[1])
                 for a in args)
    pinned |= any(a == "--match-head-commit" and SHA40.fullmatch(b) for a, b in zip(args, args[1:]))
    if not ({"--squash", "-s"} & set(args)) or not pinned:
        return "gh pr merge without both --squash and --match-head-commit <40-hex sha>"
    bad = [a for a in args if a in ("--admin", "-R", "-m", "-r", "--merge", "--rebase")
           or a.startswith("--repo")]
    return f"gh pr merge {bad[0]}" if bad else None


def hidden(words):
    """A git push or gh pr merge the parser can't read: behind sudo -u, env -i, nice, a path."""
    names = [os.path.basename(w) for w in words]
    return ("git" in names and "push" in words) or ("gh" in names and "merge" in words)


def check(cmd, cwd, git_out):
    """The first reason a push or merge in cmd needs the maintainer, or None."""
    if not re.search(r"\bpush\b|\bmerge\b", cmd):
        return None
    pushes = merges = 0
    moved = None
    for words, affects_cwd in segments(cmd):
        words, why = strip_prefixes(words)
        if not words:
            continue
        if words[0] in ("cd", "pushd", "popd"):
            if affects_cwd:
                cwd = move(cwd, words[1] if len(words) > 1 and words[0] == "cd" else "$HOME")
            continue
        if words[0] not in ("git", "gh") and hidden(words):
            return "a git push or gh pr merge behind a command the guard can't read"
        if words[0] == "git":
            args, where, opt_why = git_args(words, cwd)
            if args[:1] == ["push"] or opt_why and "push" in args:
                pushes += 1
                reason = why or opt_why or moved or check_push(args[1:], where, git_out)
                if reason:
                    return reason
            elif args[:1] and args[0] in RETARGET:  # read before it runs: judge the push blind
                moved = f"git push after git {args[0]}, which may change what it pushes"
        elif words[:3] == ["gh", "pr", "merge"]:
            merges += 1
            reason = why or check_merge(words[3:])
            if reason:
                return reason
    text = strip_heredocs(cmd)
    if len(re.findall(GIT + r"push\b", text)) > pushes \
            or len(re.findall(GH + r"pr\s+merge\b", text)) > merges:
        return "a git push or gh pr merge the guard can't parse"
    return None
