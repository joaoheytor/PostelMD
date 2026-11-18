"""Finds Bash commands that write repository files outside Write/Edit (postelmd_guard.py, git mode).

Write/Edit calls pass through the guard's secrets, public-repo and ADR checks; `sed -i`,
`perl -pi`, `tee` and `>`/`>>` redirects don't. This finds their targets so the guard can ask
first. It keeps false positives low rather than catching everything: heredoc bodies and quoted
text are not commands, fd duplications (2>&1) and /dev/* aren't files, targets in /tmp,
$TMPDIR or outside any git repository are scratch (unless they are inside the project, which
may itself live in a temp dir), git-ignored paths are build output, and a target that is an
unexpanded variable can't be judged before the command runs.

It asks; it doesn't block, and it is a guardrail, not a boundary. It does NOT catch: cp, mv,
install, ln, rsync, dd of=, truncate, touch, git apply/checkout/restore, patch, editors,
`python -c`/`node -e`/`awk` writing files, `bash -c "… > f"` and `eval` (the inner string is
quoted text) and `$var` or `$(…)` targets. Heredocs are found by shscan.py, outside quotes.
"""
import os
import re
import shlex
import subprocess

from shscan import split_heredocs

# Every git call in the guard: a repository's own config must not make the hook run its
# commands before the permission prompt (core.fsmonitor; hooks for good measure).
SAFE_GIT = ["git", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"]
SAFE_GIT_ENV = {**{k: v for k, v in os.environ.items() if not k.startswith("GIT_")},
                "GIT_CONFIG_NOSYSTEM": "1"}

PURE_REDIRECT = re.compile(r"&?>>?\|?|>&|<>")


def is_redirect(tok):
    """True for a redirect token that may open a file for writing: any unquoted run of
    shell punctuation with a > that isn't a process substitution >( (<>, >>|, &>>, >&, ...;
    shlex groups `;>` and `&&>` too, which then count as both)."""
    return is_punct(tok) and ">" in tok and "(" not in tok and ")" not in tok
PREFIXES = {"sudo", "env", "command", "xargs", "time", "nohup", "exec"}


def strip_heredocs(cmd):
    return split_heredocs(cmd)[0]


def tokens(cmd):
    text = strip_heredocs(cmd).replace("\\\n", " ").replace("\n", " ; ")
    lex = shlex.shlex(text, posix=True, punctuation_chars=True)
    lex.whitespace_split = True
    lex.commenters = ""  # shlex ends a word at `#`; bash doesn't (x#y), so a comment hid args
    try:
        return list(lex)
    except ValueError:  # unbalanced quotes: not judged
        return []


def is_punct(tok):
    return bool(tok) and all(c in "();<>|&" for c in tok)


SED_ADDRESS = re.compile(r"(?:\d+|\$|/(?:\\.|[^/\\])*/[IM]?)(?:\s*,\s*(?:\d+|\$|[+~]\d+"
                         r"|/(?:\\.|[^/\\])*/[IM]?))?\s*!?\s*")


def sed_w_files(script):
    """The files a sed script's w/W commands and s///w flags write (an approximation: it
    splits commands at ; and newlines)."""
    files = []
    for line in re.split(r"[;\n]", script):
        line = line.strip().lstrip("{}").strip()
        m = SED_ADDRESS.match(line)
        rest = line[m.end():] if m else line
        if rest[:1] in ("w", "W"):
            files.append(rest[1:].strip())
        elif rest[:1] == "s" and len(rest) > 2:
            delim, i, seen = rest[1], 2, 0
            while i < len(rest) and seen < 2:
                seen += rest[i] == delim
                i += 2 if rest[i] == "\\" else 1
            flags = re.match(r"[gpiImMe0-9]*w\s*(.*)", rest[i:]) if seen == 2 else None
            if flags:
                files.append(flags.group(1).strip())
    return [f for f in files if f]


def sed_targets(args):
    """sed -i's files, and the files its scripts write with w, W and s///w."""
    files, scripts, skip = [], [], None
    for a in args:
        if skip:
            scripts += [a] if skip == "e" else []
            skip = None
        elif a in ("-e", "--expression", "-f", "--file"):
            skip = "e" if a in ("-e", "--expression") else "f"
        elif a.startswith(("--expression=", "--file=")):
            scripts += [a.split("=", 1)[1]] if a.startswith("--e") else ["\n"]
        elif not a.startswith("-"):
            files.append(a)
    if not scripts and files and skip is None and not any(
            a in ("-f", "--file") or a.startswith("--file=") for a in args):
        scripts, files = [files[0]], files[1:]
    suffixes = sed_suffixes(args)
    backups = [backup(f, s) for f in files for s in suffixes if s]
    return (files if suffixes else []) + backups + [f for s in scripts for f in sed_w_files(s)]


def sed_suffixes(args):
    """The backup suffix of each -i/--in-place in args ("" for none)."""
    out = []
    for a in args:
        m = re.match(r"-[a-zA-Z]*i(.*)", a) if not a.startswith("--") else None
        if m or a == "--in-place" or a.startswith("--in-place="):
            out.append(m.group(1) if m else a.partition("=")[2])
    return out


def backup(path, suffix):
    """Where sed -i<suffix> backs up path: each * in suffix is the file's name, and a suffix
    with a / names a path of its own."""
    if "*" not in suffix:
        return path + suffix
    name = suffix.replace("*", os.path.basename(path))
    return name if "/" in name else os.path.join(os.path.dirname(path), name)


def sort_targets(args):
    """sort -o FILE, -oFILE, -ro FILE, --output[=]FILE."""
    out = []
    for i, a in enumerate(args):
        nxt = args[i + 1] if i + 1 < len(args) else ""
        if a.startswith("--output"):
            out.append(a.split("=", 1)[1] if "=" in a else nxt)
        elif re.match(r"-[a-zA-Z]*o", a):
            out.append(a[a.index("o", 1) + 1:] or nxt)
    return [o for o in out if o]


def uniq_targets(args):
    """uniq's second operand, its output file."""
    pos, skip = [], False
    for a in args:
        if skip:
            skip = False
        elif a in ("-f", "-s", "-w", "--skip-fields", "--skip-chars", "--check-chars"):
            skip = True
        elif a == "-" or not a.startswith("-"):
            pos.append(a)
    return pos[1:2] if pos[1:2] != ["-"] else []


def perl_targets(args):
    if not any(re.match(r"-[a-zA-Z]*i", a) for a in args):
        return []
    files, script_given, skip = [], False, False
    for a in args:
        if skip:
            skip = False
        elif re.match(r"-[a-zA-Z]*[eE]$", a):
            script_given = skip = True
        elif not a.startswith("-"):
            files.append(a)
    return files if script_given else files[1:]


def command_targets(words):
    """Files the command itself writes: tee's arguments, sed -i's and perl -i's files, sed's
    w files, sort -o's and uniq's output."""
    while words and (words[0] in PREFIXES or re.match(r"\w+=", words[0])):
        words = words[1:]
    if not words:
        return []
    name, args = os.path.basename(words[0]), words[1:]
    if name == "tee":
        return [a for a in args if not a.startswith("-")]
    if name == "sed":
        return sed_targets(args)
    if name == "perl":
        return perl_targets(args)
    return {"sort": sort_targets, "uniq": uniq_targets}.get(name, lambda _: [])(args)


def write_targets(cmd):
    """Every path the command writes with a redirect (zsh's >! and >>! too: bash reads the
    `!` as the file name), tee, sed -i (and its backup), sed w, perl -i, sort -o or uniq,
    as written."""
    targets, words, toks, i = [], [], tokens(cmd), 0
    while i < len(toks):
        tok = toks[i]
        nxt = toks[i + 1] if i + 1 < len(toks) else ""
        if is_redirect(tok) and nxt == "!" and tok in (">", ">>", "&>", "&>>"):
            nxt = toks[i + 2] if i + 2 < len(toks) else ""  # zsh's >! and >>!: clobber
            i += 1
        if is_redirect(tok):
            dup = tok.endswith(">&") and re.fullmatch(r"\d+|-", nxt)  # 2>&1 duplicates
            if nxt and not is_punct(nxt) and not dup:
                targets.append(nxt)
            if not PURE_REDIRECT.fullmatch(tok):  # `;>` or `&&>`: also ends the command
                targets += command_targets(words)
                words = []
            i += 2
            continue
        if is_punct(tok):  # ; && || | ( ) >( <( and the like end a simple command
            targets += command_targets(words)
            words = []
        elif not (re.fullmatch(r"\d+", tok) and is_redirect(nxt)):
            words.append(tok)
        i += 1
    return targets + command_targets(words)


def under(path, root):
    return bool(root) and (path + "/").startswith(os.path.normpath(root) + "/")


def scratch(path, homes=()):
    """/dev/*, or a temp dir that isn't one of homes (the project, which may live in one)."""
    if path.startswith("/dev/"):
        return True
    if any(under(path, h) for h in homes):
        return False
    tmps = ["/tmp", "/var/tmp", os.environ.get("TMPDIR", "")]
    return any(under(path, t) for t in tmps)


def git_tracks(path):
    """True when path is in a git work tree and not ignored there."""
    d = os.path.dirname(path)
    while d and not os.path.isdir(d):
        d = os.path.dirname(d)
    try:
        top = subprocess.run([*SAFE_GIT, "-C", d or "/", "rev-parse", "--show-toplevel"],
                             capture_output=True, text=True, timeout=5, env=SAFE_GIT_ENV)
        if top.returncode != 0:
            return False  # outside any repository
        ignored = subprocess.run([*SAFE_GIT, "-C", d, "check-ignore", "-q", "--", path],
                                 capture_output=True, timeout=5, env=SAFE_GIT_ENV)
    except (OSError, subprocess.SubprocessError):
        return False
    return ignored.returncode == 1  # 0: ignored (build output); 128: git can't tell


def resolve(target, cwd, project):
    """The normalized path a write target names, or None when it holds a variable or a
    substitution the guard can't know before it runs ($CLAUDE_PROJECT_DIR is known)."""
    for var in ("${CLAUDE_PROJECT_DIR}", "$CLAUDE_PROJECT_DIR"):
        if project and target.startswith(var):
            target = project + target[len(var):]
    if "$" in target or "`" in target:
        return None
    return os.path.normpath(os.path.join(cwd, os.path.expanduser(target)))


def repo_write_targets(cmd, cwd):
    """The targets of cmd that are repository files (in a git work tree, not ignored)."""
    if not re.search(r">|\btee\b|\bsed\b|\bperl\b|\bsort\b|\buniq\b", cmd):
        return []
    found = []
    project = os.environ.get("CLAUDE_PROJECT_DIR", "")
    for t in write_targets(cmd):
        path = resolve(t, cwd, project)
        if path is None:
            continue  # unexpanded variable or substitution: unknown before it runs
        if ".git" in path.split(os.sep) or os.path.basename(path) == "config.worktree":
            found.append(t)  # git config and hooks: core.fsmonitor, hooksPath, diff.external...
        elif not scratch(path, (project,)) and git_tracks(path):
            found.append(t)
    return found
