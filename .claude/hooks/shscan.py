"""A quote-aware pass over a Bash command (bash_writes, shparse, commitmsg, shvars).

It tracks single quotes, double quotes, backslash escapes and $( … ) (also inside double
quotes), so it knows which characters are quoted and where real heredocs start: a `<<WORD`
inside quotes is text, not a heredoc (`echo "<<ls"` must not hide the next lines). A
delimiter must be exactly WORD, 'WORD', "WORD" or \\WORD followed by a space, end of line or
an operator; anything else (`<<'E'OF`, `<<E"OF"`) is flagged. Flags, which make the guard ask:
  ansi            `$'…'` outside quotes (ANSI-C quoting: shlex reads it differently)
  bad-heredoc     a `<<` whose delimiter isn't one of the forms above
  continued       a line with a heredoc ends in a backslash (the body starts later)
  unbalanced      a quote is still open at the end
Comments are not special (bash_writes.tokens doesn't treat `#` as one either), so a quote in
a comment leaves the command unbalanced: fail closed.
"""
import re
from typing import NamedTuple

HEREDOC = re.compile(r"<<(-?)[ \t]*(?:'([\w.-]+)'|\"([\w.-]+)\"|\\([\w.-]+)|([\w.-]+))"
                     r"(?=[\s;&|<>()]|$)")
QUOTED = {"'", '"', "\\"}


class Scan(NamedTuple):
    lines: list    # [(text, [context per character] or None for heredoc body lines)]
    bodies: list   # [(quoted delimiter?, body)]
    starts: list   # [(start, end)] of each heredoc operator in the stripped text
    flags: set


def scan_line(line, stack, flags, offset, starts):
    """Contexts for line's characters (None or "(" unquoted; ' " \\ quoted), heredocs found."""
    ctx, found, j = [], [], 0
    while j < len(line):
        top, c = (stack[-1] if stack else None), line[j]
        if top == "'":
            ctx.append("'")
            if c == "'":
                stack.pop()
            j += 1
            continue
        if c == "\\":
            n = len(line[j:j + 2])
            ctx += ["\\"] * n
            j += n
            if n == 1 and found:
                flags.add("continued")
            continue
        if top == '"':
            if line.startswith("$(", j):
                stack.append("(")
                ctx += [None, None]
                j += 2
                continue
            if c == '"':
                stack.pop()
            ctx.append('"')
            j += 1
            continue
        if c in "'\"":  # unquoted here, or inside $( … )
            if c == "'" and j and line[j - 1] == "$" and ctx[-1] not in QUOTED:
                flags.add("ansi")
            stack.append(c)
            ctx.append(c)
            j += 1
            continue
        n = 3 if line.startswith("<<<", j) else 2 if line.startswith("<<", j) else 0
        if n == 2:
            m = HEREDOC.match(line, j)
            if m:
                found.append(m)
                starts.append((offset + j, offset + m.end()))
                n = m.end() - j
            else:
                flags.add("bad-heredoc")
        if n:
            ctx += [top] * n
            j += n
            continue
        if c == "(":
            stack.append("(")
        elif c == ")" and top == "(":
            stack.pop()
        ctx.append(top)
        j += 1
    return ctx, found


def scan(cmd):
    lines, out, bodies, starts, flags, stack, i, offset = cmd.split("\n"), [], [], [], set(), [], 0, 0
    while i < len(lines):
        line = lines[i]
        i += 1
        ctx, found = scan_line(line, stack, flags, offset, starts)
        out.append((line, ctx))
        offset += len(line) + 1
        for m in found:  # several heredocs on one line: their bodies follow in order
            word = next(g for g in m.groups()[1:] if g is not None)
            end = i
            while end < len(lines) and (lines[end].lstrip("\t") if m.group(1) else lines[end]) != word:
                end += 1
            if end < len(lines):  # with no closing line bash reads to the end: left as commands
                bodies.append((m.group(5) is None, "\n".join(lines[i:end])))
                out += [(ln, None) for ln in lines[i:end + 1]]
                i = end + 1
    if any(s in ("'", '"') for s in stack):
        flags.add("unbalanced")
    return Scan(out, bodies, starts, flags)


def stripped(s):
    return "\n".join(text for text, ctx in s.lines if ctx is not None)


def blanked(s):
    """The stripped text with every quoted or escaped character replaced by `_`."""
    return "\n".join("".join("_" if k in QUOTED else ch for ch, k in zip(text, ctx))
                     for text, ctx in s.lines if ctx is not None)


def split_heredocs(cmd):
    """(the command without heredoc bodies, [(quoted delimiter?, body)]): bodies are data."""
    s = scan(cmd)
    return stripped(s), s.bodies
