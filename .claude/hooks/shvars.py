"""Shell variables assigned a literal before the command (postelmd_guard.py, git mode).

`S=/tmp/x; git -C $S log` and `S=x && cd $S && git status` set a shell variable, not an
environment variable for git. When a command starts with such assignments (each followed by
`;`, `&&` or a newline) the guard drops them and puts each value where `$S` or `${S}` appears
(outside single quotes, escapes and heredoc bodies, as shscan.py sees them), then judges the
result exactly as if it had been typed. Fail closed: nothing is rewritten when
  - a value isn't a plain literal (no $, `, spaces, quotes inside, globs, ~ or a leading =);
  - a name isn't all upper case or all lower case (S, WT, wt), is assigned twice, or appears
    in the rest of the command other than as $NAME or ${NAME} (`read S`, `for S in`, `S=y`,
    `unset S`, `export S`);
  - a name is special to bash or zsh (DIRSTACK and OLDPWD change on cd, RANDOM, SECONDS,
    path, status, ...) or may already be exported, so assigning it changes what programs
    see: it is in the hook's environment, or HOME, PATH, GIT_*, XDG_*, EDITOR, ... (SPECIAL);
  - shscan flags the command (ANSI-C quoting, odd heredocs, an open quote).
`export S=x`, `S=x git ...` (an environment prefix) and assignments later in the command
are left alone: codeexec.env_check judges them as before.
"""
import os
import re

from shscan import scan

NAME = r"[A-Za-z_][A-Za-z0-9_]*"
PLAIN = re.compile(r"[A-Z][A-Z0-9_]{0,30}|[a-z][a-z0-9_]{0,30}")
SAFE = r"[A-Za-z0-9_./:@%+,-][A-Za-z0-9_./:@%+,=-]*"  # no leading = (zsh: =cmd is a path)
PRELUDE = re.compile(rf"\s*({NAME})=(?:({SAFE})|'({SAFE})'|\"({SAFE})\")[ \t]*(?:;|&&|\n)")
REF = re.compile(rf"\$\{{({NAME})\}}|\$({NAME})")
SPECIAL = re.compile(
    # bash: set or rewritten by the shell itself
    r"DIRSTACK|OLDPWD|PWD|RANDOM|SRANDOM|SECONDS|LINENO|HISTCMD|PPID|UID|EUID|BASH\w*"
    r"|PIPESTATUS|FUNCNAME|OPTIND|OPTARG|OPTERR|REPLY|EPOCH\w*|IFS|PS\d?|PS\w*|ENV|CDPATH"
    r"|PROMPT_COMMAND|SHELLOPTS|GLOBIGNORE|COLUMNS|LINES|COMP_\w*|READLINE_\w*|HOSTNAME"
    r"|HOSTTYPE|MACHTYPE|OSTYPE|GROUPS|SHLVL|TMOUT|MAIL\w*|HIST\w*|INPUTRC|FIGNORE|TIMEFORMAT"
    # zsh: special parameters, many tied to arrays (path <-> PATH)
    r"|status|argv|dirstack|fpath|path|cdpath|manpath|mailpath|module_path|psvar|pipestatus"
    r"|options|parameters|commands|aliases|galiases|saliases|functions|builtins|modules"
    r"|reswords|histchars|prompt|signals|userdirs|nameddirs|jobstates|jobtexts|jobdirs"
    r"|funcstack|funcfiletrace|funcsourcetrace|termcap|terminfo|widgets|keymaps|watch"
    r"|zsh_\w*|ZSH\w*|ZDOTDIR|KEYTIMEOUT|WORDCHARS|NULLCMD|READNULLCMD|TRY_BLOCK_\w*"
    # read by git, go, npm, make and friends when exported
    r"|HOME|PATH|EDITOR|VISUAL|PAGER|BROWSER|SHELL|TMPDIR|TERM|USER|LOGNAME|LANG|LC_\w*"
    r"|GIT_\w*|SSH_\w*|XDG_\w*|LESS\w*|GO\w*|CGO_\w*|NODE_\w*|NPM_\w*|YARN_\w*|LD_\w*"
    r"|DYLD_\w*|PYTHON\w*|PERL\w*|RUBY\w*|MAKE\w*|GNUMAKE\w*|CC|CXX|AR|CFLAGS|LDFLAGS"
    r"|DOCKER_\w*|GH_\w*|GITHUB_\w*|CLAUDE_\w*", re.I)


def harmless(name):
    return bool(PLAIN.fullmatch(name)) and name not in os.environ \
        and not SPECIAL.fullmatch(name)


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


def expand(line, ctx, vals):
    """line with $NAME/${NAME} of vals replaced where the $ is unquoted or double-quoted."""
    out, j = [], 0
    while j < len(line):
        m = REF.match(line, j) if line[j] == "$" and ctx[j] not in ("'", "\\") else None
        if m and (m.group(1) or m.group(2)) in vals:
            out.append(vals[m.group(1) or m.group(2)])
            j = m.end()
            continue
        out.append(line[j])
        j += 1
    return "".join(out)


def normalize(cmd):
    """cmd with its leading literal assignments dropped and substituted, or cmd unchanged."""
    vals, start = prelude(cmd)
    if not vals:
        return cmd
    body = cmd[start:]
    for name in vals:  # used only as $NAME or ${NAME}: never reassigned, read or exported
        if re.search(rf"(?<![\w$]){name}(?!\w)", body.replace("${" + name + "}", "")):
            return cmd
    s = scan(body)
    if s.flags:
        return cmd
    return "\n".join(text if ctx is None else expand(text, ctx, vals) for text, ctx in s.lines)
