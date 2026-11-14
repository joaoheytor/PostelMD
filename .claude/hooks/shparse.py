"""Shell parsing shared by the guard's outward-command rules (outward.py, gh_rules.py, codeexec.py).

Built on bash_writes.tokens (shlex: quotes, heredoc bodies stripped, `#` not a comment).
Fail closed: callers pass an outward command only when strict() returns its words.
"""
import os
import re

from bash_writes import PURE_REDIRECT, is_punct, is_redirect, tokens
from shscan import blanked, scan, stripped

UNKNOWN = object()  # a directory the parser can't follow
GIT_GLOBAL_OK = {"--no-pager"}
KEYWORDS = {"do", "then", "else", "elif", "if", "while", "until", "{", "!"}


def segments(cmd):
    """The words of each simple command in cmd, with redirects and their targets dropped."""
    out, words, toks, i = [], [], tokens(cmd), 0
    while i < len(toks):
        tok = toks[i]
        nxt = toks[i + 1] if i + 1 < len(toks) else ""
        if PURE_REDIRECT.fullmatch(tok) or tok in ("<", "<<<", "<&"):
            i += 2
            continue
        if is_punct(tok):
            out.append(words)
            words = []
        elif not (re.fullmatch(r"\d+", tok) and (is_redirect(nxt) or nxt in ("<", "<&"))):
            words.append(tok)
        i += 1
    out.append(words)
    return [w for w in out if w]


# The only redirects an outward command may carry, exactly as typed (unquoted, a space
# before, nothing attached): ` 2>&1` and ` >/dev/null`. `2>/dev/null` asks: shlex splits it
# into 2 > /dev/null, and dropping the redirect would leave `2` as an argument.
OUTPUT_REDIRECT = re.compile(r"(?<=[ \t])(2>&1|>/dev/null)(?=[ \t|]|$)")
NUM = r"\d+"
FILTER_ARGS = {  # (plain flags, {flag letter: its value, next word or attached})
    "tail": (r"-[qv]|-\d+|--(lines|bytes)=\+?\d+", {"n": r"\+?\d+", "c": r"\+?\d+"}),
    "head": (r"-[qv]|-\d+|--(lines|bytes)=\d+", {"n": NUM, "c": NUM}),
    "wc": (r"-[lcwmL]+", {}),
    "cat": (r"-[nbsAvET]+", {}),
    "sort": (r"-[bdfghinMRrsuVz]+", {"k": r"[\w.,]+", "t": r"[^\w\s]"}),
    "uniq": (r"-[cdDiuz]+", {"f": NUM, "s": NUM, "w": NUM}),
    "grep": (r"-[EFGivnclLqshHwxUz]+|--color(=\w+)?|--line-buffered",
             {"A": NUM, "B": NUM, "C": NUM, "m": NUM, "e": r"[^\n]*"}),
}


def filter_ok(words):
    """True when words are tail/head/grep/wc/cat/sort/uniq with plain flags and option
    values; no positional arguments (a file to read, or for uniq one to write) except grep's
    one pattern when there is no -e. No -O, -f, -o or --long options that run programs."""
    if not words or words[0] not in FILTER_ARGS:
        return False
    flags, valued = FILTER_ARGS[words[0]]
    pos, has_e, i = [], False, 1
    while i < len(words):
        a = words[i]
        if len(a) >= 2 and a[0] == "-" and a[1] in valued:
            val = a[2:] if len(a) > 2 else (words[i + 1] if i + 1 < len(words) else None)
            i += len(a) == 2
            if val is None or not re.fullmatch(valued[a[1]], val):
                return False
            has_e |= a[1] == "e"
        elif not re.fullmatch(flags, a):
            if a.startswith("-"):
                return False
            pos.append(a)
        i += 1
    return not pos or (words[0] == "grep" and len(pos) == 1 and not has_e)


def strict(cmd):
    """The outward command's words when cmd is one simple command, optionally followed by
    ` 2>&1`, ` >/dev/null` and pipes into filter_ok() filters. Otherwise None: no ; && ||
    ( ) heredocs or other redirects, no $ or ` anywhere, nothing shscan flags."""
    s = scan(cmd)
    if s.flags or s.starts:
        return None
    raw, text = stripped(s), blanked(s)
    for m in reversed(list(OUTPUT_REDIRECT.finditer(text))):  # cut from the text as typed
        raw, text = (t[:m.start()] + " " * len(m.group(0)) + t[m.end():] for t in (raw, text))
    if re.search(r"[<>]", text):
        return None
    toks = tokens(raw)
    if not toks or any("$" in t or "`" in t for t in toks):
        return None
    parts, part = [], []
    for t in toks + ["|"]:
        if t != "|":
            part.append(t)
            continue
        if not part or any(is_punct(w) for w in part):
            return None
        parts.append(part)
        part = []
    if not all(filter_ok(p) for p in parts[1:]):
        return None
    return parts[0]


# Commands that run their arguments as a command. bare() drops the ones it can follow
# exactly (no options; timeout with one duration); wrapped() finds the rest.
# zsh precommand modifiers too: noglob, nocorrect, `-` (exec with a dash) and repeat N.
STRIP = {"nice", "nohup", "time", "command", "builtin", "exec", "env", "coproc", "noglob",
         "nocorrect", "-"}
WRAPPERS = STRIP | {"timeout", "sudo", "doas", "xargs", "watch", "stdbuf", "ionice", "chrt",
                    "taskset", "setsid", "flock", "unbuffer", "script", "strace", "ltrace",
                    "nsenter", "chroot", "busybox", "parallel"}
TOOLS = {"git", "go", "gh", "npm", "npx", "make"}


def bare(words):
    """words without leading VAR=value assignments, shell keywords (`for d in x; do git -C
    $d status; done` runs git after `do`) and STRIP wrappers without options."""
    while words:
        nxt = words[1] if len(words) > 1 else ""
        if words[0] in KEYWORDS or re.match(r"\w+=", words[0]):
            words = words[1:]
        elif words[0] in STRIP and nxt and (nxt == "-" or not nxt.startswith("-")):
            words = words[1:]
        elif words[0] == "repeat" and re.fullmatch(r"\d+", nxt) and len(words) > 2:
            words = words[2:]
        elif words[0] == "timeout" and re.fullmatch(r"\d+(\.\d+)?[smhd]?", nxt) \
                and len(words) > 2 and not words[2].startswith("-"):
            words = words[2:]
        else:
            return words
    return words


def wrapped(segs):
    """The wrapper bare() couldn't drop that runs git, go, gh, npm, npx, make or a script."""
    for words in segs:
        words = bare(words)
        if words and os.path.basename(words[0]) in WRAPPERS and any(
                os.path.basename(w) in TOOLS or re.match(r"(\./)?scripts/", w) for w in words[1:]):
            return words[0]
    return None


SHELLS = {"sh", "bash", "zsh", "dash", "ksh", "mksh", "fish", "busybox", "csh", "tcsh"}


SET_OPTIONS = {"pipefail", "errexit", "nounset", "xtrace", "verbose"}


def set_ok(args):
    """True for `set` with only -e/-u/-x/-v and -o of SET_OPTIONS (set -euo pipefail), or
    positional words after --."""
    i = 0
    while i < len(args):
        a = args[i]
        if a == "--":
            return True
        if not re.fullmatch(r"[-+][euxvo]+", a):
            return False
        if "o" in a:
            i += 1
            if i >= len(args) or args[i] not in SET_OPTIONS:
                return False
        i += 1
    return True


def indirect(segs):
    """A command that runs text the guard sees only as a quoted string (eval, sh -c, zsh -c,
    source, .) or that renames commands (alias), or None. Asked in any command."""
    for words in segs:
        words = bare(words)
        name = os.path.basename(words[0]) if words else ""
        if name in ("eval", "alias", "source", ".", "unalias", "hash", "enable", "trap",
                    "setopt", "unsetopt", "emulate", "shopt"):
            return name  # option changes: bare_glob_qual, extended_glob, ... change parsing
        if re.match(r"\w+\[", name):
            return "an array element assignment (zsh options[...]=on)"
        if name == "set" and not set_ok(words[1:]):
            return "set with options other than -e, -u, -x, -v and -o pipefail"
        if name in SHELLS and any(re.fullmatch(r"-\w*c\w*", w) for w in words[1:]):
            return f"{name} -c"
    return None


def move(cwd, target, logical=False):
    """The directory git -C or cd -P target leads to, symlinks resolved (L/.. may leave the
    tree); with logical, the one a plain cd leads to (L/.. resolved as text)."""
    if cwd is UNKNOWN or not target or "$" in target or "`" in target or target == "-":
        return UNKNOWN
    path = os.path.join(cwd, os.path.expanduser(target))
    return os.path.normpath(path) if logical else os.path.realpath(path)


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
