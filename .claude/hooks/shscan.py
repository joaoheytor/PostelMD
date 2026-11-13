"""A quote-aware pass over a Bash command (bash_writes, shparse, commitmsg, shvars).

It tracks single quotes, double quotes, backslash escapes and $( … ) (also inside double
quotes), so it knows which characters are quoted and where real heredocs start: a `<<WORD`
inside quotes is text, not a heredoc (`echo "<<ls"` must not hide the next lines). A
delimiter must be exactly WORD, 'WORD', "WORD" or \\WORD followed by a space, end of line or
an operator; anything else (`<<'E'OF`, `<<E"OF"`) is flagged. Flags, which make the guard ask:
  ansi            `$'…'` outside quotes (ANSI-C quoting: shlex reads it differently)
  bad-heredoc     a `<<` whose delimiter isn't one of the forms above
  continued       a line with a heredoc ends in a backslash (the body starts later)
  joined          any backslash-newline outside single quotes (`git pu\\<newline>sh`: the
                  shell removes it, joining words; the guard doesn't emulate that)
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
        if c == "\\":  # "\\": escaped outside quotes; '"': inside double quotes
            n = len(line[j:j + 2])
            ctx += ["\\" if top != '"' else '"'] * n
            j += n
            if n == 1:  # a backslash-newline: the shell joins the lines
                flags.add("joined")
                if found:
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


# Characters the guard doesn't model when they are unquoted (globs, brace expansion,
# comments, history, tilde and zsh's =cmd, ^ and ~ globs, non-ASCII and control
# characters). Only these forms of $, ^ and ~ are understood: $?, $!, $#, $$, $@, $*,
# $0-$9, $-, ${NAME}, and HEAD^2 or HEAD~3 (after a word character, before a non-word one).
MODELLED = re.compile(r"\$\{\w+\}|\$[?!#$@*0-9-]|(?<=\w)[~^]\d*(?![\w~^])")
UNMODELLED = re.compile(r"[{}*?\[\]!~^]|(?:^|(?<=[\s;&|()<>]))[#=]|[^\t\n\x20-\x7e]")
TOOL_WORD = re.compile(r"\b(git|go|gh|npm|npx|make)\b|scripts/")
REASONS = {
    "ansi": "ANSI-C quoting ($'...')",
    "bad-heredoc": "a heredoc delimiter other than WORD, 'WORD', \"WORD\" or \\WORD",
    "continued": "a heredoc line ending in a backslash",
    "joined": "a backslash-newline (the shell joins the words around it)",
    "unbalanced": "an unbalanced quote",
}


# A glob in a word that starts with a literal path up to a `/` (internal/vault/*.go,
# docs/adr/0*.md) can only expand to names under that path, never to an option; nor can
# --include=*.go. Such a word passes when no part of it is quoted or expanded.
GLOB_WORD = re.compile(r"(?<![^\s;&|()<>])(?:[A-Za-z0-9_.][\w.-]*/[\w./*?-]*"
                       r"|--(?:include|exclude)=[\w.*?-]+)(?![^\s;&|<>])")  # x/*(e:…:) runs code
TOOLS = {"git", "go", "gh", "npm", "npx", "make"}


def mentions_tool(cmd):
    """True when a word of cmd is git, go, gh, npm, npx, make or a scripts/ path, also when
    spelled g''it, g\\<newline>it or $'g'it; any mention when cmd can't be tokenized."""
    from bash_writes import tokens  # bash_writes imports this module
    words = []
    for text in (cmd, cmd.replace("\\\n", "")):
        toks = tokens(text)
        if not toks:
            return bool(TOOL_WORD.search(cmd) or TOOL_WORD.search(re.sub(r"[\\'\"$]", "", text)))
        words += [re.sub(r"[$\\]", "", t) for t in toks]
    return any(w.rsplit("/", 1)[-1] in TOOLS or re.match(r"(\./)?scripts/", w) for w in words)


def modelled(s):
    """The blanked text with the forms the guard understands (MODELLED, GLOB_WORD) blanked
    too, so that UNMODELLED finds only what it can't judge."""
    raw, text = stripped(s), blanked(s)
    for m in GLOB_WORD.finditer(text):
        if raw[m.start():m.end()] == m.group(0):
            text = text[:m.start()] + "_" * len(m.group(0)) + text[m.end():]
    return MODELLED.sub(lambda x: "_" * len(x.group(0)), text)


def unmodelled(cmd):
    """Why the guard can't judge cmd as the shell will run it, or None. Only for commands
    that mention a tool: refuse, rather than model, every shell feature the other checks
    don't parse (character allowlisting; see MODELLED and UNMODELLED)."""
    if not mentions_tool(cmd):
        return None
    s = scan(cmd)
    if s.flags:
        return REASONS[sorted(s.flags)[0]]
    if any("\\" in ctx for _, ctx in s.lines if ctx):
        return "a backslash outside quotes"
    m = UNMODELLED.search(modelled(s))
    if m:
        return (f"{m.group(0)!r} outside quotes (globs, brace expansion, # comments, history, "
                "~ and zsh expansions are not modelled: quote it)")
    return None


def split_heredocs(cmd):
    """(the command without heredoc bodies, [(quoted delimiter?, body)]): bodies are data."""
    s = scan(cmd)
    return stripped(s), s.bodies
