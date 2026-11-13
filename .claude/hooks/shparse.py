"""Shell parsing shared by the guard's outward-command rules (outward.py, gh_rules.py, codeexec.py).

Built on bash_writes.tokens (shlex: quotes, heredoc bodies stripped, `#` not a comment).
Fail closed: callers pass an outward command only when strict() returns its words.
"""
import os
import re

from bash_writes import PREFIXES, REDIRECTS, is_punct, strip_heredocs, tokens

UNKNOWN = object()  # a directory the parser can't follow
GIT_GLOBAL_OK = {"--no-pager"}
KEYWORDS = {"do", "then", "else", "elif", "if", "while", "until", "{", "!"}


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


# Trailing redirects an outward command may carry, as tokens(): 2>&1 and >/dev/null.
OUTPUT_REDIRECTS = (["2", ">&", "1"], [">", "/dev/null"])
NUM = r"\d+"
FILTER_ARGS = {  # (flags, flags whose value is the next word or the rest of the flag, values)
    "tail": (r"-[qv]|-\d+|--(lines|bytes)=\+?\d+", "nc", r"\+?\d+"),
    "head": (r"-[qv]|-\d+|--(lines|bytes)=\d+", "nc", NUM),
    "wc": (r"-[lcwmL]+", "", ""),
    "cat": (r"-[nbsAvET]+", "", ""),
    "sort": (r"-[bdfghinMRrsuVz]+", "kt", r"[\w.,:;]+"),
    "uniq": (r"-[cdDiuz]+", "fsw", NUM),
    "grep": (r"-[EFGivnclLqshHwxUz]+|--color(=\w+)?|--line-buffered|--", "ABCme", r"[^\n]*"),
}


def filter_ok(words):
    """True when words are tail/head/grep/wc/cat/sort/uniq with plain flags, numbers or (grep
    only) one pattern: no file names (uniq and sort -o write one), no -O, no --long options
    that run programs (sort --compress-program)."""
    if not words or words[0] not in FILTER_ARGS:
        return False
    flags, valued, value = FILTER_ARGS[words[0]]
    patterns, i = 0, 1
    while i < len(words):
        a = words[i]
        if valued and re.fullmatch(f"-[{valued}]", a):
            if i + 1 == len(words) or not re.fullmatch(value, words[i + 1]):
                return False
            i += 1
        elif valued and re.fullmatch(f"-[{valued}]", a[:2]) and re.fullmatch(value, a[2:]):
            pass
        elif not re.fullmatch(flags, a) and (words[0] != "grep" or a.startswith("-")):
            if not re.fullmatch(NUM, a) or words[0] not in ("tail", "head"):
                return False
        elif words[0] == "grep" and not a.startswith("-"):
            patterns += 1
        i += 1
    return patterns <= 1


def strict(cmd):
    """The outward command's words when cmd is one simple command, optionally followed by
    2>&1, >/dev/null and pipes into filter_ok() filters (item: trailing output filters).
    Otherwise None: no ; && || ( ) other redirects or heredocs, and no $ or ` anywhere."""
    toks = tokens(cmd)
    if not toks or any("$" in t or "`" in t for t in toks) \
            or re.search(r"(^|\s)\d+\s+[<>]", strip_heredocs(cmd)):  # `5 >x`: an argument or an fd?
        return None
    parts, part = [], []
    for t in toks + ["|"]:
        if t != "|":
            part.append(t)
            continue
        cut = True
        while cut:
            cut = next((len(r) for r in OUTPUT_REDIRECTS
                        if len(part) > len(r) and part[-len(r):] == r), 0)
            part = part[:len(part) - cut]
        if not part or any(is_punct(w) for w in part):
            return None
        parts.append(part)
        part = []
    if not all(filter_ok(p) for p in parts[1:]):
        return None
    return parts[0]


def bare(words):
    """words without leading VAR=value assignments, sudo/env/xargs/... prefixes and shell
    keywords (`for d in x; do git -C $d status; done` runs git after `do`)."""
    while words and (words[0] in PREFIXES or words[0] in KEYWORDS or re.match(r"\w+=", words[0])):
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
