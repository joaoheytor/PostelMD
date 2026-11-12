"""Shell parsing shared by the guard's outward-command rules (outward.py, gh_rules.py, codeexec.py).

Built on bash_writes.tokens (shlex: quotes, heredoc bodies stripped, `#` not a comment).
Fail closed: callers pass an outward command only when single() returns its words.
"""
import os
import re

from bash_writes import PREFIXES, REDIRECTS, is_punct, tokens

UNKNOWN = object()  # a directory the parser can't follow
GIT_GLOBAL_OK = {"--no-pager"}


def segments(cmd):
    """The words of each simple command in cmd, with redirects and their targets dropped."""
    out, words, toks, i = [], [], tokens(cmd), 0
    while i < len(toks):
        tok = toks[i]
        nxt = toks[i + 1] if i + 1 < len(toks) else ""
        if tok in REDIRECTS or tok in (">&", "<", "<<<"):
            i += 2
            continue
        if is_punct(tok):
            out.append(words)
            words = []
        elif not (re.fullmatch(r"\d+", tok) and nxt in REDIRECTS | {">&", "<"}):
            words.append(tok)
        i += 1
    out.append(words)
    return [w for w in out if w]


def single(cmd):
    """cmd's words when it is exactly one simple command: no ; && || | ( ) redirects or
    heredocs, and no $ or ` anywhere (variables, $(...), backticks). Otherwise None."""
    toks = tokens(cmd)
    if not toks or any(is_punct(t) or "$" in t or "`" in t for t in toks):
        return None
    return toks


def bare(words):
    """words without leading VAR=value assignments and sudo/env/xargs/... prefixes."""
    while words and (words[0] in PREFIXES or re.match(r"\w+=", words[0])):
        words = words[1:]
    return words


def move(cwd, target):
    if cwd is UNKNOWN or not target or "$" in target or "`" in target or target == "-":
        return UNKNOWN
    return os.path.normpath(os.path.join(cwd, os.path.expanduser(target)))


def git_parts(words, cwd):
    """(global options all harmless?, directory, subcommand, its args) for git words."""
    i, ok = 1, True
    while i < len(words) and words[i].startswith("-"):
        if words[i] == "-C" and i + 1 < len(words):
            cwd, i = move(cwd, words[i + 1]), i + 2
        elif words[i] in GIT_GLOBAL_OK:
            i += 1
        else:  # -c k=v, --git-dir, --work-tree, --exec-path...: they change what git does
            ok, i = False, i + (2 if words[i] == "-c" else 1)
    return ok, cwd, (words[i] if i < len(words) else ""), words[i + 1:]
