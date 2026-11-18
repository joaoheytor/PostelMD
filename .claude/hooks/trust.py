"""The maintainer's opt-in that stops the guard asking about three in-project changes.

It lives outside the project and its worktrees, where agents don't write:
${XDG_CONFIG_HOME:-$HOME/.config}/postelmd/guard-trust.json (opt_in_path), holding exactly
  {"project": "<realpath of CLAUDE_PROJECT_DIR>", "trust": [<kinds>]}
It counts only as a regular file (not a symlink, FIFO or hard link: st_nlink 1) owned by
this user, not group- or world-writable, at most 4 KiB, naming this project. Anything else
(bad JSON, another key, an unknown kind, another project, any error) reads as off: the
guard asks exactly as without it. The kinds:
  shell-writes    SHELL WRITE (bash_writes.py) passes when every command is one of a few
                  text tools that write only through a redirect or tee (writer_ok: WRITERS,
                  sed only with --sandbox, no sort -o or uniq output file) and every target
                  is a literal path (no glob, brace, ~, $ or backslash) inside the project,
                  with no symlink in it or its parents, absent or a regular file with one
                  link, and not protected();
  unparsed-local  CAN'T PARSE passes when localcmd.local_only() finds the command stays
                  local; the later checks (cd, foreign repository, outward) still run;
  accepted-adr    edits to Accepted ADRs pass (scripts/adr-immutable.sh still runs in
                  pre-commit and CI); without it, shell writes to docs/adr/ keep asking.
The opt-in never turns a deny into an allow and never answers "allow": the command goes on
to the rest of the guard and the permission rules. A Bash, Write or Edit whose text or path
names the opt-in asks, also split by quotes or built with $ or a glob next to a home or
.config path (mentions, names_opt_in); in the end that relies on Claude Code's sandbox
keeping agents' writes inside the project. protected_target() asks for shell writes, by
redirect, tee, sed -i or w, perl -i, sort -o or uniq, to guard, git and GitHub files
whether or not they are git-ignored and whether or not the opt-in is on, and for any such
target with a glob, brace, ~, $ or backslash whose literal start is in the project. Like
the rest of the guard, a guardrail, not a boundary.
"""
import functools
import json
import os
import re
import stat

from bash_writes import resolve, sort_targets, tokens, uniq_targets, write_targets
from shparse import bare, segments

KINDS = frozenset({"shell-writes", "unparsed-local", "accepted-adr"})
OPT_IN = "the maintainer's opt-in that stops some guard prompts (trust.py)"
MAX_BYTES = 4096
MENTION = re.compile(r"guard-trust|xdg_config_home|\.config/postelmd|postelmd.*\.config"
                     r"|\.config.*postelmd", re.I | re.S)
HOME_REF = re.compile(r"~/|\bHOME\b|/home/|/Users/|\.config", re.I)
BUILT = re.compile(r"[$`*?\[{]")
# The only commands in a command whose shell writes the opt-in trusts: each writes only
# through the redirect or tee the guard judges (writer_ok), and none moves, links, copies,
# unpacks or changes directory, so the targets are where they look.
WRITERS = {"echo", "printf", "cat", "sed", "tee", "sort", "uniq", "head", "tail", "grep",
           "wc", "cut", "tr", "true", "nl", "paste"}
SPECIAL = re.compile(r"[*?\[\]{}~$`\\]")  # the shell may expand or unquote these


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


def unquoted(text):
    return re.sub(r"\\\n|[\\'\"]", "", text or "")


def mentions(text):
    """True when text names the opt-in, also with quotes or backslashes splitting the name."""
    return any(MENTION.search(t) for t in (text or "", unquoted(text), " ".join(tokens(text or ""))))


def bash_mentions(cmd):
    """mentions(), or a command that names postelmd and .config together, or builds a path
    from a home or .config path with $, ` or a glob (paths in the project don't count)."""
    if mentions(cmd):
        return True
    text = unquoted(cmd)
    for root in sorted(project_roots(), key=len, reverse=True):
        text = text.replace(root, "")
    return bool(re.search(r"postelmd", text, re.I) and re.search(r"\.config", text, re.I)) \
        or bool(HOME_REF.search(text) and BUILT.search(text))


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
            if path is None or SPECIAL.search(path):  # judge the literal start
                start = (path or t)[:SPECIAL.search(path or t).start()]
                start = os.path.normpath(os.path.join(d, start))
                if any(under(p, roots) for p in (start, os.path.realpath(start))):
                    return t
                continue
            rels = [relative(p, roots) for p in (path, os.path.realpath(path))]
            if any(r and protected(r, adr=False) for r in rels):
                return t
    return None


def writer_ok(words):
    """True for a WRITERS command that writes only through redirects or tee: sed only with
    --sandbox (no e, r or w) and without -f, -s or --follow-symlinks; sort without -o or
    --compress-program; uniq without an output file."""
    w = bare(words) or ["true"]
    name, args = w[0], w[1:]
    if name not in WRITERS:
        return False
    if name == "sed":
        return "--sandbox" in args and not any(
            a in ("-f", "--file", "-s", "--separate") or a.startswith(("--file=", "--follow"))
            or re.fullmatch(r"-[a-zA-Z]*[fs][a-zA-Z]*", a) for a in args)
    if name == "sort":
        return not sort_targets(args) and not any(a.startswith("--compress") for a in args)
    return name != "uniq" or not uniq_targets(args)


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
    """True when cmd runs only writer_ok() commands (no subshell or substitution) and every
    target is a literal plain_target() inside the project that isn't protected()
    (docs/adr/ without accepted-adr)."""
    roots = project_roots()
    if not roots or not targets or any("(" in t and not t.strip("();<>|&") for t in tokens(cmd)):
        return False
    if any(SPECIAL.search(t) for t in targets) or not all(writer_ok(w) for w in segments(cmd)):
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
        if bash_mentions(raw) or bash_mentions(cmd):
            return (f"This command may name {OPT_IN}, or builds a home or .config path at run "
                    "time. Only the maintainer changes the opt-in.")
        found = protected_target(cmd, cwd)
    except Exception:  # pylint: disable=broad-except
        return f"The guard couldn't check this command against {OPT_IN}."
    if found:
        return (f"SHELL WRITE to {found}: a git, GitHub or guard file, or a target the shell "
                "expands (glob, brace, ~, $) in the project. Use Write/Edit, or approve only "
                "if the maintainer asked for exactly this.")
    return None


def writes_trusted(cmd, targets, cwd):
    """True when the shell-writes opt-in covers targets (shell_writes_ok). Fail closed."""
    try:
        return trusted("shell-writes") and shell_writes_ok(cmd, targets, cwd)
    except Exception:  # pylint: disable=broad-except
        return False
