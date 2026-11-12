"""Scan commit messages before they are written (postelmd_guard.py, git mode; rule 15).

A message is published by the next push, which passes silently, so it gets the Write/Edit
checks: a secret denies, private-looking content asks. The text comes from -m/--message
(also -sm), heredoc bodies in the command (`-m "$(cat <<'EOF' ...)"`), and the file of
-F/--file. It asks when it can't see the text: a file that is `-`, unreadable, or outside the
project and temp dirs (the guard won't read ~/.config for you), or a message with $ or `
other than the quoted-heredoc form. .githooks/commit-msg runs gitleaks on the message too.
"""
import os
import re

from bash_writes import scratch, split_heredocs, under
from content import has_secret, public_reasons
from shparse import UNKNOWN, bare, git_parts, segments

QUOTED_HEREDOC = re.compile(r"\$\(cat <<-?\s*'[\w.-]+'\s*(;\s*)?\)")


def sources(args):
    """([message texts], [message files]) from git commit's arguments."""
    texts, files, i = [], [], 0
    while i < len(args):
        a = args[i]
        if a == "--":
            break
        if a.startswith(("--message", "--file")):
            name, eq, val = a[2:].partition("=")
            if not eq:
                i += 1
                val = args[i] if i < len(args) else ""
            (texts if name == "message" else files).append(val)
        elif re.match(r"-[a-zA-Z]", a):
            for j, ch in enumerate(a[1:], 1):
                if ch in "mFcCt":  # takes a value: the rest of this word, or the next word
                    val = a[j + 1:]
                    if not val:
                        i += 1
                        val = args[i] if i < len(args) else ""
                    if ch in "mF":
                        (texts if ch == "m" else files).append(val)
                    break
        i += 1
    return texts, files


def read_message(path, cwd):
    """(text, None) or (None, reason to ask)."""
    if path == "-" or cwd is UNKNOWN and not os.path.isabs(os.path.expanduser(path)):
        return None, f"the guard can't read the message file {path}"
    full = os.path.normpath(os.path.join(cwd if cwd is not UNKNOWN else "/",
                                         os.path.expanduser(path)))
    project = os.environ.get("CLAUDE_PROJECT_DIR", "")
    if not (under(full, project) or scratch(full, (project,))):
        return None, f"the message file {path} is outside the project and temp dirs"
    try:
        with open(full, encoding="utf-8") as fh:
            return fh.read(1 << 20), None
    except (OSError, UnicodeDecodeError):
        return None, f"the guard can't read the message file {path}"


def check(cmd, cwd):
    """(decision, reason) for a git commit whose message needs the maintainer, or None."""
    found, texts = False, []
    for words in segments(cmd):
        words = bare(words)
        if not words or os.path.basename(words[0]) != "git":
            continue
        _, where, sub, args = git_parts(words, cwd)
        if sub != "commit":
            continue
        found = True
        msgs, files = sources(args)
        for m in msgs:
            if ("$" in m or "`" in m) and not QUOTED_HEREDOC.fullmatch(m.strip()):
                return "ask", "a commit message built with $ or ` that the guard can't see"
            texts.append(m)
        for path in files:
            text, why = read_message(path, where)
            if why:
                return "ask", why
            texts.append(text)
    if not found:
        return None
    for quoted, body in split_heredocs(cmd)[1]:
        if not quoted and ("$" in body or "`" in body):
            return "ask", "a heredoc commit message with expansions (quote the delimiter: 'EOF')"
        texts.append(body)
    text = "\n".join(texts)
    if has_secret(text):
        return "deny", "BLOCKED: secret-shaped string in the commit message (rule 15)."
    reasons = public_reasons(text)
    if reasons:
        return "ask", "the commit message contains " + "; ".join(reasons)
    return None
