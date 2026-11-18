"""The maintainer's opt-in that stops the guard asking about three in-project changes.

It lives outside the project and its worktrees, where agents don't write:
${XDG_CONFIG_HOME:-$HOME/.config}/postelmd/guard-trust.json (opt_in_path), holding exactly
  {"project": "<realpath of CLAUDE_PROJECT_DIR>", "trust": [<kinds>]}
It counts only as a regular file (not a symlink, FIFO or hard link: st_nlink 1) owned by
this user, not group- or world-writable, at most 4 KiB, naming this project. Anything else
(bad JSON, another key, an unknown kind, another project, any error) reads as off: the
guard asks exactly as without it. The kinds:
  shell-writes    SHELL WRITE (bash_writes.py) passes when every command is a plain text
                  tool (WRITERS: no cd, subshell, ln, cp, mv, mkdir, git, ...) and every
                  target is inside the project, with no symlink in it or its parents,
                  absent or a regular file with one link, and not protected();
  unparsed-local  CAN'T PARSE passes when localcmd.local_only() finds the command stays
                  local; the later checks (cd, foreign repository, outward) still run;
  accepted-adr    edits to Accepted ADRs pass (scripts/adr-immutable.sh still runs in
                  pre-commit and CI); without it, shell writes to docs/adr/ keep asking.
The opt-in never turns a deny into an allow and never answers "allow": the command goes on
to the rest of the guard and the permission rules. A Bash, Write or Edit whose text or path
names the opt-in asks (mentions, names_opt_in). protected_target() asks for shell writes to
guard, git and GitHub files whether or not they are git-ignored and whether or not the
opt-in is on. Like the rest of the guard, a guardrail, not a boundary.
"""
import functools
import json
import os
import re
import stat

from bash_writes import resolve, tokens, write_targets
from shparse import bare, segments

KINDS = frozenset({"shell-writes", "unparsed-local", "accepted-adr"})
OPT_IN = "the maintainer's opt-in that stops some guard prompts (trust.py)"
MAX_BYTES = 4096
MENTION = re.compile(r"guard-trust|xdg_config_home|\.config/postelmd", re.I)
# The only commands in a command whose shell writes the opt-in trusts: none moves, links,
# copies, unpacks or changes directory, so the targets are where they look.
WRITERS = {"echo", "printf", "cat", "sed", "perl", "tee", "sort", "uniq", "head", "tail",
           "grep", "wc", "ls", "jq", "cut", "tr", "true", "date", "diff", "nl", "column",
           "paste", "basename", "dirname"}


def opt_in_path():
    base = os.environ.get("XDG_CONFIG_HOME", "")
    if not os.path.isabs(base):
        home = os.environ.get("HOME", "")
        base = os.path.join(home, ".config") if os.path.isabs(home) else ""
    return os.path.join(base, "postelmd", "guard-trust.json") if base else None


def project_roots():
    project = os.environ.get("CLAUDE_PROJECT_DIR", "")
    if not os.path.isabs(project):
        return set()
    return {os.path.normpath(project), os.path.realpath(project)}


def under(path, roots):
    return any((path + "/").startswith(r + "/") for r in roots)


def read_kinds():
    project, path = os.environ.get("CLAUDE_PROJECT_DIR", ""), opt_in_path()
    if not os.path.isabs(project) or not path:
        return frozenset()
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid() or st.st_nlink != 1 \
                or st.st_mode & 0o022 or st.st_size > MAX_BYTES:
            return frozenset()
        data = os.read(fd, MAX_BYTES + 1)
    finally:
        os.close(fd)
    doc = json.loads(data.decode("utf-8"))
    if not isinstance(doc, dict) or set(doc) != {"project", "trust"} \
            or doc["project"] != os.path.realpath(project) or not isinstance(doc["trust"], list):
        return frozenset()
    kinds = doc["trust"]
    if not kinds or not all(isinstance(k, str) and k in KINDS for k in kinds):
        return frozenset()
    return frozenset(kinds)


@functools.lru_cache(maxsize=None)
def kinds():
    try:
        return read_kinds()
    except Exception:  # pylint: disable=broad-except  # fail closed: any error is "off"
        return frozenset()


def trusted(kind):
    return kind in kinds()


def mentions(text):
    return bool(MENTION.search(text or ""))


def names_opt_in(file_path, text):
    """True when a Write/Edit names the opt-in, by text, by path or through a symlink."""
    target = opt_in_path()
    return mentions(file_path) or mentions(text) or bool(
        target and file_path and os.path.realpath(file_path) == os.path.realpath(target))


def protected(rel, adr=True):
    """True for a project-relative path to git, GitHub, guard or licence files (any case,
    in the project or a worktree under .claude/worktrees/); with adr, docs/adr/ too."""
    parts = rel.lower().split("/")
    for i, p in enumerate(parts):
        nxt = parts[i + 1] if i + 1 < len(parts) else None
        if (p.startswith(".git") and p != ".gitignore") or p in ("codeowners", "config.worktree") \
                or p.startswith("license") or (p == ".claude" and nxt != "worktrees") \
                or (adr and p == "docs" and nxt == "adr"):
            return True
    return False


def relative(path, roots):
    """path relative to the longest root it is under, or None (the root itself too)."""
    for r in sorted(roots, key=len, reverse=True):
        if path.startswith(r + "/"):
            return path[len(r) + 1:]
    return None


def cd_dirs(cmd, cwd):
    """cwd and every directory a literal cd in cmd may lead to, taken in order."""
    dirs, cur = [cwd], cwd
    for words in segments(cmd):
        w = bare(words)
        if w and w[0] in ("cd", "pushd", "chdir") and len(w) > 1 and "$" not in w[-1]:
            cur = os.path.normpath(os.path.join(cur, os.path.expanduser(w[-1])))
            dirs.append(cur)
    return dirs


def protected_target(cmd, cwd):
    """The first shell-write target that is a protected() file in the project, judged from
    every directory a cd may have left, git-ignored or not, or None."""
    roots, project = project_roots(), os.environ.get("CLAUDE_PROJECT_DIR", "")
    for t in write_targets(cmd):
        for d in cd_dirs(cmd, cwd):
            path = resolve(t, d, project)
            if path is None:
                continue
            rels = [relative(p, roots) for p in (path, os.path.realpath(path))]
            if any(r and protected(r, adr=False) for r in rels):
                return t
    return None


def plain_target(path, roots):
    """True when path is under a root with no symlink below the root, and is absent or a
    regular file with one link."""
    rel = relative(path, roots)
    if rel is None:
        return False
    cur = next(r for r in sorted(roots, key=len, reverse=True) if path.startswith(r + "/"))
    parts = rel.split("/")
    for i, part in enumerate(parts):
        cur = os.path.join(cur, part)
        try:
            st = os.lstat(cur)
        except FileNotFoundError:
            return True
        if stat.S_ISLNK(st.st_mode):
            return False
        if i < len(parts) - 1 and not stat.S_ISDIR(st.st_mode):
            return False
    return stat.S_ISREG(st.st_mode) and st.st_nlink == 1


def shell_writes_ok(cmd, targets, cwd):
    """True when cmd runs only WRITERS (no subshell or substitution) and every target is a
    plain_target() inside the project that isn't protected() (docs/adr/ without
    accepted-adr)."""
    roots = project_roots()
    if not roots or not targets or any("(" in t and not t.strip("();<>|&") for t in tokens(cmd)):
        return False
    if not all((bare(w) or ["true"])[0] in WRITERS for w in segments(cmd)):
        return False
    project = os.environ["CLAUDE_PROJECT_DIR"]
    for t in targets:
        path = resolve(t, cwd, project)
        if path is None or not plain_target(path, roots):
            return False
        if protected(relative(path, roots), adr=not trusted("accepted-adr")):
            return False
    return True


def bash_asks(raw, cmd, cwd):
    """Why a Bash command asks whatever the opt-in says: it names the opt-in, or it
    shell-writes a protected file (protected_target). Fail closed: an error asks."""
    try:
        if mentions(raw) or mentions(cmd):
            return f"This command names {OPT_IN}. Only the maintainer changes it."
        found = protected_target(cmd, cwd)
    except Exception:  # pylint: disable=broad-except
        return f"The guard couldn't check this command against {OPT_IN}."
    if found:
        return (f"SHELL WRITE to {found}, a git, GitHub or guard file. Use Write/Edit, or "
                "approve only if the maintainer asked for exactly this.")
    return None


def writes_trusted(cmd, targets, cwd):
    """True when the shell-writes opt-in covers targets (shell_writes_ok). Fail closed."""
    try:
        return trusted("shell-writes") and shell_writes_ok(cmd, targets, cwd)
    except Exception:  # pylint: disable=broad-except
        return False
