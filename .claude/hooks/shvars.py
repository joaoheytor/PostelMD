"""Shell variables assigned a literal before the command (postelmd_guard.py, git mode).

`S=/tmp/x; git -C $S log` and `S=x && cd $S && git status` set a shell variable, not an
environment variable for git. When a command starts with such assignments (each followed by
`;`, `&&` or a newline) the guard drops them and puts each value where `$S` or `${S}` appears
(outside single quotes and heredoc bodies), then judges the result exactly as if it had been
typed. Fail closed: nothing is rewritten when
  - a value isn't a plain literal (no $, `, spaces, quotes inside, globs or ~);
  - a name is assigned twice, or appears in the rest of the command other than as $NAME or
    ${NAME} (`read S`, `for S in`, `S=y`, `unset S`, `export S`);
  - a name may already be exported, so assigning it changes what programs see: it is in
    the hook's environment, or it is HOME, PATH, GIT_*, XDG_*, EDITOR, ... (DANGEROUS).
`export S=x`, `S=x git ...` (an environment prefix) and assignments later in the command
are left alone: codeexec.env_check judges them as before.
"""
import os
import re

from bash_writes import HEREDOC

NAME = r"[A-Za-z_][A-Za-z0-9_]*"
SAFE = r"[A-Za-z0-9_./:@%+,-][A-Za-z0-9_./:@%+,=-]*"  # no leading = (zsh: =cmd is a path)
PRELUDE = re.compile(rf"\s*({NAME})=(?:({SAFE})|'({SAFE})'|\"({SAFE})\")[ \t]*(?:;|&&|\n)")
REF = re.compile(rf"\$\{{({NAME})\}}|\$({NAME})")
DANGEROUS = re.compile(
    r"(HOME|PATH|IFS|CDPATH|ENV|BASH_ENV|BASH_FUNC\w*|SHELLOPTS|BASHOPTS|PS4|PROMPT_COMMAND"
    r"|EDITOR|VISUAL|PAGER|BROWSER|SHELL|TMPDIR|TERM|USER|LANG|LC_\w*|GIT_\w*|SSH_\w*|XDG_\w*"
    r"|LESS\w*|GO\w*|CGO_\w*|NODE_\w*|NPM_\w*|YARN_\w*|LD_\w*|DYLD_\w*|PYTHON\w*|PERL\w*"
    r"|RUBY\w*|MAKE\w*|GNUMAKE\w*|CC|CXX|AR|CFLAGS|LDFLAGS|DOCKER_\w*|GH_\w*|GITHUB_\w*"
    r"|CLAUDE_\w*|ZDOTDIR|HISTFILE)", re.I)


def harmless(name):
    return name not in os.environ and not DANGEROUS.fullmatch(name)


def prelude(cmd):
    """({name: value}, where the rest of cmd starts) for leading literal assignments."""
    vals, pos = {}, 0
    while True:
        m = PRELUDE.match(cmd, pos)
        if not m:
            return vals, pos
        name, value = m.group(1), next(g for g in m.groups()[1:] if g is not None)
        if name in vals or not harmless(name):
            return {}, 0
        vals[name], pos = value, m.end()


def expand(line, vals, quote):
    """line with $NAME/${NAME} of vals replaced outside single quotes; (text, open quote)."""
    out, j = [], 0
    while j < len(line):
        c = line[j]
        if quote == "'":
            quote = None if c == "'" else quote
        elif c == "\\":
            out.append(line[j:j + 2])
            j += 2
            continue
        elif c in "'\"" and quote is None:
            quote = c
        elif c == '"' and quote == '"':
            quote = None
        elif c == "$":
            m = REF.match(line, j)
            if m and (m.group(1) or m.group(2)) in vals:
                out.append(vals[m.group(1) or m.group(2)])
                j = m.end()
                continue
        out.append(c)
        j += 1
    return "".join(out), quote


def normalize(cmd):
    """cmd with its leading literal assignments dropped and substituted, or cmd unchanged."""
    vals, start = prelude(cmd)
    if not vals:
        return cmd
    body = cmd[start:]
    for name in vals:  # used only as $NAME or ${NAME}: never reassigned, read or exported
        if re.search(rf"(?<![\w$]){name}(?!\w)", body.replace("${" + name + "}", "")):
            return cmd
    lines, out, quote, i = body.split("\n"), [], None, 0
    while i < len(lines):
        text, quote = expand(lines[i], vals, quote)
        out.append(text)
        i += 1
        for m in HEREDOC.finditer(lines[i - 1]):  # bodies are data: copied as they are
            end = i
            while end < len(lines) and (lines[end].lstrip("\t") if m.group(1) else lines[end]) != m.group(3):
                end += 1
            if end < len(lines):
                out += lines[i:end + 1]
                i = end + 1
    return "\n".join(out)
