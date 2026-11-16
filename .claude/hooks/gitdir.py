"""Ask before git runs in a repository other than the project's (postelmd_guard.py, git mode).

A `.git` file (`gitdir: ../gd`) can point git at a crafted gitdir whose config runs a
program on any command (core.fsmonitor, hooksPath, diff.external, sshCommand). So every git
command must run where `git rev-parse --git-common-dir` (with SAFE_GIT, which can't run that
config) gives the project's own common dir; worktrees of the project share it. The guard
follows `cd` and `-C`, keeping every directory a cd may have left (a subshell's cd doesn't
last), and fails closed: an unknown directory or a rev-parse error asks.
"""
import os
import re

from bash_writes import PURE_REDIRECT, is_punct, tokens
from shparse import UNKNOWN, bare, git_parts, move, segments
from shscan import expandable, scan


def common_dir(d, git_out, cache):
    if d not in cache:
        out = git_out(d, "rev-parse", "--path-format=absolute", "--git-common-dir")
        cache[d] = os.path.realpath(out.strip()) if out and out.strip() else None
    return cache[d]


def cd_targets(d, args, cmd):
    """Where `cd args` may lead from d: {UNKNOWN} for zsh's `cd old new` (replaces old with
    new in $PWD), `cd +N`/`-N` (the directory stack) and CDPATH. Otherwise both the logical
    directory (the shell resolves L/.. as text) and the physical one (cd -P, chdir), since
    which one a shell uses depends on its options: git must be safe in each."""
    physical = False
    while args and args[0] in ("-L", "-P", "-e", "-@", "-q", "-s"):
        physical |= args[0] == "-P"
        args = args[1:]
    if args[:1] == ["--"]:
        args = args[1:]
    if len(args) > 1 or (args and re.fullmatch(r"[+-]\d*", args[0])) \
            or os.environ.get("CDPATH") or os.environ.get("cdpath") or "cdpath" in cmd.lower():
        return {UNKNOWN}  # zsh's cdpath array is tied to CDPATH
    target = args[0] if args else "$HOME"
    if target.startswith("~") and target != "~" and not target.startswith("~/"):
        return {UNKNOWN}  # ~- ~+ ~1 ~-1 (the directory stack) and ~name (named directories)
    real = move(d, target)
    return {real} if physical or real is UNKNOWN else {real, move(d, target, logical=True)}


CD_NAMES = {"cd", "pushd", "popd", "chdir"}
CD_WORD = re.compile(r"\b(cd|pushd|popd|chdir)\b")
SEPARATORS = " \t\n;&|()<>"


def raw_words(s):
    """The words of a scanned command as typed, with their quoting: a list of lists of
    (char, context) pairs, and "sep" between simple commands (unquoted ; & | ( ) < > or a
    newline). Heredoc bodies are left out."""
    out, word = [], []
    for text, ctx in s.lines:
        if ctx is None:
            continue
        for ch, k in list(zip(text, ctx)) + [("\n", None)]:
            if k is None and ch in SEPARATORS:
                if word:
                    out.append(word)
                    word = []
                if ch not in " \t":
                    out.append("sep")
            else:
                word.append((ch, k))
    return out


def substitutions(text):
    """The bodies of the $(…) and `…` command substitutions in text."""
    found, i = [], 0
    while i < len(text):
        if text.startswith("$(", i):
            depth, j = 1, i + 2
            while j < len(text) and depth:
                depth += {"(": 1, ")": -1}.get(text[j], 0)
                j += 1
            found.append(text[i + 2:j])
            i = j
        elif text[i] == "`":
            j = text.find("`", i + 1)
            j = len(text) if j < 0 else j
            found.append(text[i + 1:j])
            i = j + 1
        else:
            i += 1
    return found


def cd_syntax(cmd):
    """A reason to ask for cd syntax the directory check can't follow: a quoted or escaped
    command name ('c'd, c\\d), an unquoted glob or brace in a cd argument (cd L*, cd [L]),
    or a cd in a command substitution the shell runs (unquoted, in double quotes or in a
    heredoc with an unquoted delimiter)."""
    s = scan(cmd)
    after_cd = False
    for w in raw_words(s):
        if w == "sep":
            after_cd = False
            continue
        plain = "".join(ch for ch, _ in w if ch not in "'\"\\$")
        if plain in CD_NAMES and any(k is not None for _, k in w):
            return f"a quoted or escaped {plain}"
        if after_cd and any(k is None and ch in "*?[]{}" for ch, k in w):
            return "a glob or brace in a cd argument"
        after_cd = after_cd or plain in CD_NAMES
    texts = [expandable(s)] + [body for quoted, body in s.bodies if not quoted]
    if any(CD_WORD.search(sub) for t in texts for sub in substitutions(t)):
        return "cd in a command substitution"
    return None


def inside(path, roots):
    return path is not UNKNOWN and any((path + "/").startswith(r + "/") for r in roots)


def cd_outside(cmd, cwd):
    """A reason to ask when a cd, pushd, popd or chdir in cmd may leave the project:
    settings.json allows `Bash(cd:*)`, so this is the only check on where cd goes. Every
    target (logical and physical, cd_targets) must be under CLAUDE_PROJECT_DIR, its
    worktrees in .claude/worktrees/ included; cd with no argument, ~, -, an unknown $VAR,
    CDPATH, two arguments or +N/-N ask."""
    project = os.environ.get("CLAUDE_PROJECT_DIR", "")
    toks = tokens(cmd)  # unquoted: 'c'd is cd
    if not CD_WORD.search(cmd) and not CD_NAMES & set(toks):
        return None
    if not project:
        return "cd without a project directory to compare with"
    if not toks:
        return "cd in a command the guard can't split into words"
    why = cd_syntax(cmd)
    if why:
        return why
    roots = {os.path.normpath(project), os.path.realpath(project)}
    dirs = {cwd if isinstance(cwd, str) else UNKNOWN}
    # Joined only by &&, each cd runs only after the previous one succeeded, so it replaces
    # the directory (cd sub && cd ..). Otherwise (;, ||, |, subshells, or a keyword such as
    # ! that changes a command's status) every directory a cd may have left stays possible.
    chained = all(t == "&&" for t in toks if is_punct(t) and not PURE_REDIRECT.fullmatch(t)) \
        and all(bare(w) == w for w in segments(cmd))
    for words in segments(cmd):
        words = bare(words)
        if not words:
            continue
        if words[0] in ("pushd", "popd", "chdir"):
            return f"{words[0]} (only a literal cd inside the project passes)"
        if words[0] != "cd":
            continue
        targets = {t for d in dirs for t in cd_targets(d, words[1:], cmd)}
        dirs = targets if chained else dirs | targets
        outside = [d for d in dirs if not inside(d, roots)]
        if outside:
            where = "a directory the guard can't follow" if outside[0] is UNKNOWN else outside[0]
            return f"cd to {where}, outside the project ({project})"
    return None


def check(cmd, cwd, git_out):
    """A reason to ask, or None when every git command runs in the project's repository."""
    if "git" not in cmd:
        return None
    project = os.environ.get("CLAUDE_PROJECT_DIR", "")
    cache = {}
    want = common_dir(project, git_out, cache) if project else None
    dirs = {cwd}
    for words in segments(cmd):
        words = bare(words)
        if not words:
            continue
        if words[0] == "cd":
            dirs |= {t for d in dirs for t in cd_targets(d, words[1:], cmd)}
            continue
        if words[0] in ("pushd", "popd", "chdir"):  # chdir: zsh's cd, unknown to bash
            dirs.add(UNKNOWN)
            continue
        if os.path.basename(words[0]) != "git":
            continue
        for d in dirs:
            where = git_parts(words, d)[1]
            if where is UNKNOWN:
                return "git in a directory the guard can't follow (cd -, cd $X, pushd)"
            if want is None or common_dir(where, git_out, cache) != want:
                return (f"git in {where}, which isn't this project's repository (a .git file "
                        "there may point at a gitdir whose config runs commands)")
    return None
