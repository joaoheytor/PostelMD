"""The maintainer's local opt-in that stops the guard asking about three in-project changes.

Off unless CLAUDE_PROJECT_DIR/.claude/guard-trust.local.json (git-ignored) is a regular file,
not a symlink, owned by this user, not group- or world-writable, at most 4 KiB, holding
exactly {"trust": [...]} with one or more of:
  shell-writes    SHELL WRITE (bash_writes.py) passes when every target is inside the project,
                  at its logical and its physical path, and none is PROTECTED;
  unparsed-local  CAN'T PARSE passes when localcmd.local_only() finds the command stays local;
                  the later checks (cd, foreign repository, outward) still run;
  accepted-adr    edits to Accepted ADRs pass (scripts/adr-immutable.sh still runs in
                  pre-commit and CI); without it, shell writes to docs/adr/ keep asking.
Anything else (bad JSON, another key, an unknown kind, any error) reads as off: the guard
asks exactly as without the file. The opt-in never turns a deny into an allow, and it lets
a command through to the rest of the guard and the permission rules; it never answers
"allow". Agents can't switch it on: a Write/Edit or a shell write to the file asks
(writes_opt_in). Like the rest of the guard, a guardrail, not a boundary.
"""
import functools
import json
import os
import re
import stat

from bash_writes import resolve, write_targets

NAME = "guard-trust.local.json"
KINDS = frozenset({"shell-writes", "unparsed-local", "accepted-adr"})
MAX_BYTES = 4096
# Shell writes that keep asking with the opt-in, in the project or any worktree under it:
# git internals, the guard and its settings, local guard files, hooks, workflows, licences.
PROTECTED = re.compile(r"(^|/)(\.git(/|$)|config\.worktree$|\.claude/hooks/|\.claude/settings"
                       r"(\.local)?\.json$|\.claude/[^/]*\.local\.[^/]*$|\.githooks/"
                       r"|\.github/workflows/|\.gitleaks\.toml$|LICENSE$)")
ADR = re.compile(r"(^|/)docs/adr/")


def project_roots():
    project = os.environ.get("CLAUDE_PROJECT_DIR", "")
    if not os.path.isabs(project):
        return set()
    return {os.path.normpath(project), os.path.realpath(project)}


def under(path, roots):
    return any((path + "/").startswith(r + "/") for r in roots)


def read_kinds():
    roots = project_roots()
    if not roots:
        return frozenset()
    project = os.path.normpath(os.environ["CLAUDE_PROJECT_DIR"])
    folder = os.path.join(project, ".claude")
    if os.path.islink(folder) or not under(os.path.realpath(folder), roots):
        return frozenset()
    fd = os.open(os.path.join(folder, NAME), os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        st = os.fstat(fd)
        if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid() \
                or st.st_mode & 0o022 or st.st_size > MAX_BYTES:
            return frozenset()
        data = os.read(fd, MAX_BYTES + 1)
    finally:
        os.close(fd)
    doc = json.loads(data.decode("utf-8"))
    if not isinstance(doc, dict) or set(doc) != {"trust"} or not isinstance(doc["trust"], list):
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


def relative(path, roots):
    return next(path[len(r) + 1:] for r in sorted(roots, key=len, reverse=True)
                if (path + "/").startswith(r + "/") and path != r)


def shell_writes_ok(targets, cwd):
    """True when every target (as written) is inside the project at both its logical and
    physical path, and none of those paths is PROTECTED (docs/adr/ without accepted-adr)."""
    roots = project_roots()
    if not roots or not targets:
        return False
    project = os.environ["CLAUDE_PROJECT_DIR"]
    for t in targets:
        path = resolve(t, cwd, project)
        if path is None:
            return False
        for p in (os.path.normpath(path), os.path.realpath(path)):
            if not under(p, roots) or p in roots:
                return False
            rel = relative(p, roots)
            if PROTECTED.search(rel) or (ADR.search(rel) and not trusted("accepted-adr")):
                return False
    return True


def writes_opt_in(cmd, cwd):
    """The first shell-write target of cmd that may be the opt-in file, or None."""
    project = os.environ.get("CLAUDE_PROJECT_DIR", "")
    for t in write_targets(cmd):
        path = resolve(t, cwd, project)
        if NAME in t or (path and os.path.basename(os.path.realpath(path)) == NAME):
            return t
    return None
