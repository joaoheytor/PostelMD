"""Finds Bash commands that write repository files outside Write/Edit (postelmd_guard.py, git mode).

Write/Edit calls pass through the guard's secrets, public-repo and ADR checks; `sed -i`,
`perl -pi`, `tee` and `>`/`>>` redirects don't. This finds their targets so the guard can ask
first. It keeps false positives low rather than catching everything: heredoc bodies and quoted
text are not commands, fd duplications (2>&1) and /dev/* aren't files, targets in /tmp,
$TMPDIR or outside any git repository are scratch, git-ignored paths are build output, and a
target that is an unexpanded variable can't be judged before the command runs.
"""
import os
import re
import shlex
import subprocess

REDIRECTS = {">", ">>", ">|", "&>", "&>>"}
PREFIXES = {"sudo", "env", "command", "xargs", "time", "nohup", "exec"}
# A heredoc start, not a here-string (<<<): <<EOF, <<-EOF, <<'EOF', << "EOF"
HEREDOC = re.compile(r"(?<!<)<<(-?)\s*(['\"]?)([\w.-]+)\2")


def strip_heredocs(cmd):
    """The command without heredoc bodies: their text is data, not shell."""
    out, lines, i = [], cmd.split("\n"), 0
    while i < len(lines):
        line = lines[i]
        out.append(line)
        i += 1
        for m in HEREDOC.finditer(line):  # several heredocs on one line: bodies follow in order
            while i < len(lines) and (lines[i].lstrip("\t") if m.group(1) else lines[i]) != m.group(3):
                i += 1
            i += 1
    return "\n".join(out)


def tokens(cmd):
    text = strip_heredocs(cmd).replace("\\\n", " ").replace("\n", " ; ")
    lex = shlex.shlex(text, posix=True, punctuation_chars=True)
    lex.whitespace_split = True
    try:
        return list(lex)
    except ValueError:  # unbalanced quotes: not judged
        return []


def is_punct(tok):
    return bool(tok) and all(c in "();<>|&" for c in tok)


def sed_targets(args):
    if not any(re.match(r"-[a-zA-Z]*i", a) or a.startswith("--in-place") for a in args):
        return []
    files, script_given, skip = [], False, False
    for a in args:
        if skip:
            skip = False
        elif a in ("-e", "-f", "--expression", "--file"):
            script_given = skip = True
        elif a.startswith(("--expression=", "--file=")):
            script_given = True
        elif not a.startswith("-"):
            files.append(a)
    return files if script_given else files[1:]


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
    """Files the command itself writes: tee's arguments, sed -i's and perl -i's files."""
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
    return []


def write_targets(cmd):
    """Every path the command writes with a redirect, tee, sed -i or perl -i, as written."""
    targets, words, toks, i = [], [], tokens(cmd), 0
    while i < len(toks):
        tok = toks[i]
        nxt = toks[i + 1] if i + 1 < len(toks) else ""
        if tok in REDIRECTS or tok == ">&":
            if nxt and not is_punct(nxt) and not (tok == ">&" and re.fullmatch(r"\d+|-", nxt)):
                targets.append(nxt)
            i += 2
            continue
        if is_punct(tok):  # ; && || | ( ) >( <( and the like end a simple command
            targets += command_targets(words)
            words = []
        elif not (re.fullmatch(r"\d+", tok) and nxt in REDIRECTS | {">&"}):
            words.append(tok)
        i += 1
    return targets + command_targets(words)


def scratch(path):
    prefixes = ["/dev/", "/tmp/", "/var/tmp/"]
    if os.environ.get("TMPDIR"):
        prefixes.append(os.environ["TMPDIR"].rstrip("/") + "/")
    return path == "/dev/null" or any(path.startswith(p) for p in prefixes)


def git_tracks(path):
    """True when path is in a git work tree and not ignored there."""
    d = os.path.dirname(path)
    while d and not os.path.isdir(d):
        d = os.path.dirname(d)
    try:
        top = subprocess.run(["git", "-C", d or "/", "rev-parse", "--show-toplevel"],
                             capture_output=True, text=True, timeout=5)
        if top.returncode != 0:
            return False  # outside any repository
        ignored = subprocess.run(["git", "-C", d, "check-ignore", "-q", "--", path],
                                 capture_output=True, timeout=5)
    except (OSError, subprocess.SubprocessError):
        return False
    return ignored.returncode == 1  # 0: ignored (build output); 128: git can't tell


def repo_write_targets(cmd, cwd):
    """The targets of cmd that are repository files (in a git work tree, not ignored)."""
    if not re.search(r">|\btee\b|\bsed\b|\bperl\b", cmd):
        return []
    found = []
    project = os.environ.get("CLAUDE_PROJECT_DIR", "")
    for t in write_targets(cmd):
        for var in ("${CLAUDE_PROJECT_DIR}", "$CLAUDE_PROJECT_DIR"):
            if project and t.startswith(var):
                t = project + t[len(var):]
        if "$" in t or "`" in t:
            continue  # unexpanded variable or substitution: unknown before it runs
        path = os.path.normpath(os.path.join(cwd, os.path.expanduser(t)))
        if not scratch(path) and git_tracks(path):
            found.append(t)
    return found
