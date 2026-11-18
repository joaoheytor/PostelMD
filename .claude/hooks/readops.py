"""What the commands the shell-writes opt-in trusts read (trust.py).

A trusted shell write skips the Write/Edit secret and public-repo scans, so it may only copy
from files inside the project: reads_inside() requires every file operand, file-valued
option (grep -f, --file=) and `<` source to be inside the project at its logical and its
physical path, written literally (no glob, brace, ~ to elsewhere, $ or backslash). grep
-R and --dereference-recursive (they follow every symlink) are refused, and grep -r with a
symlinked operand. echo, printf, true and tr read no files. Unknown commands are refused.
"""
import os
import re

from bash_writes import tokens
from shparse import bare, segments

SPECIAL = re.compile(r"[*?\[\]{}~$`\\!]")  # the shell may expand or unquote these
NO_FILES = {"echo", "printf", "true", "tr"}
# Short options that take a value, per command (the value is the next word or the rest of
# the cluster); grep's -f value is a file it reads.
VALUED = {"grep": "efmABCdD", "sed": "el", "sort": "ktST", "uniq": "fsw", "head": "nc",
          "tail": "ncs", "cut": "dfcb", "nl": "bdfhilnpsvw", "paste": "d", "wc": "",
          "cat": "", "tee": ""}
FILE_VALUES = {"grep": "f"}
FILE_LONG = {"file", "exclude-from", "files0-from"}


def operands(words):
    """(the files words read, ok, grep -r?); ok is False when it can't tell or reads in a
    way it refuses."""
    name, args = words[0], words[1:]
    if name in NO_FILES:
        return [], True, False
    if name not in VALUED:
        return [], False, False
    files, pos, pattern_given, recursive, i = [], [], name not in ("grep", "sed"), "", 0
    while i < len(args):
        a = args[i]
        if a == "--":
            pos += args[i + 1:]
            break
        if a.startswith("--"):
            key, _, val = a[2:].partition("=")
            if key in ("dereference-recursive",) or (key == "directories" and val == "recurse"):
                return [], False, False
            recursive = recursive or ("r" if key == "recursive" else "")
            pattern_given |= key in ("regexp", "expression", "file")
            files += [val] if key in FILE_LONG and val else []
        elif a.startswith("-") and a != "-":
            for j, c in enumerate(a[1:], 1):
                if name == "sed" and c == "i":
                    break  # the rest of the cluster is -i's backup suffix
                if name == "grep" and c in "rR":
                    recursive = "R" if c == "R" or recursive == "R" else "r"
                if c in VALUED[name]:
                    val = a[j + 1:] or (args[i + 1] if i + 1 < len(args) else "")
                    i += 0 if a[j + 1:] else 1
                    pattern_given |= c in "ef"
                    files += [val] if c in FILE_VALUES.get(name, "") else []
                    break
        else:
            pos.append(a)
        i += 1
    if recursive == "R":
        return [], False, False
    files += pos if pattern_given else pos[1:]
    return files, True, bool(recursive)


def inside(word, cwd, roots, under):
    if word in ("-", "/dev/null", "/dev/stdin"):
        return True
    if SPECIAL.search(word.removeprefix("~/") if word.startswith("~/") else word):
        return False
    path = os.path.join(cwd, os.path.expanduser(word))
    return under(os.path.normpath(path), roots) and under(os.path.realpath(path), roots)


def reads_inside(cmd, cwd, roots, under):
    """True when every file cmd's commands read is inside the project (module doc)."""
    for words in segments(cmd):
        w = bare(words)
        if not w:
            continue
        files, ok, recursive = operands(w)
        if not ok or not all(inside(f, cwd, roots, under) for f in files):
            return False
        if recursive and any(os.path.islink(os.path.join(cwd, f)) for f in files):
            return False
    toks = tokens(cmd)
    for i, t in enumerate(toks):
        if t in ("<", "<>") and not inside(toks[i + 1] if i + 1 < len(toks) else "~x",
                                           cwd, roots, under):
            return False
    return True
