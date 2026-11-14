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

from shparse import UNKNOWN, bare, git_parts, move, segments


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
            or os.environ.get("CDPATH") or "CDPATH" in cmd:
        return {UNKNOWN}
    target = args[0] if args else "$HOME"
    real = move(d, target)
    return {real} if physical or real is UNKNOWN else {real, move(d, target, logical=True)}


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
