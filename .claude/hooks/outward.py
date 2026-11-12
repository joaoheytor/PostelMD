"""Which outward-facing git and gh commands pass without a prompt (postelmd_guard.py, git mode).

Fail closed: a `git push` or an outward gh command (gh_rules.kind) passes only when the whole
Bash call is exactly ONE simple command (shparse.single: no cd, ;, &&, pipes, redirects,
heredocs, $ or backticks) whose arguments are all on an allowlist. Anything else asks.

  git [-C DIR] [--no-pager] push [-u] [--force-with-lease[=<branch>[:<sha>]]] [-q] [-v]
      [origin [<branch> | <branch>:<branch> | HEAD | HEAD:<branch>]]
<branch> is a plain name: no refs/ (except a full refs/heads/<name>), heads/, tags/,
remotes/ prefix, not HEAD, not main/master in any case, not tag-like (v<digit>...: a tag may
exist only on the remote) or a local tag's name, no `#`. The source must be a local branch,
pushed under its own name (HEAD:<branch> only for the current branch). A lease names that
same branch; one with :<sha> needs refs/remotes/origin/<branch>. With no refspec the current
branch must be such a name, push.default simple or current, no remote.*.push config, and
its @{upstream}/@{push} origin/<same name> (bare `git push` needs @{push}).
gh rules: gh_rules.py. Other code-running options: codeexec.py.
A guardrail, not a boundary: GitHub's branch protection is the hard gate.
"""
import re

import codeexec
import gh_rules
from bash_writes import strip_heredocs, tokens
from shparse import UNKNOWN, bare, git_parts, segments, single

# A command position: start of the command, or after ; & | ( or a newline, optionally after
# VAR=value assignments or sudo/env/command/xargs. Anchoring here keeps text inside
# heredocs, strings and file contents ("git commit -s" in docs) from matching.
CMD_START = r"(?:^|[;&|(]|\n)\s*(?:\w+=\S*\s+)*(?:(?:sudo|env|command|xargs)\s+)*"
# `git` followed by global options (-C dir, -c k=v, --git-dir=..., --no-pager, ...)
GIT = CMD_START + r"git(?:\s+(?:-[Cc]\s+\S+|--[\w-]+(?:=\S+)?|-[pP]))*\s+"
GH = CMD_START + r"gh\s+"
OUTWARD_GH = GH + r"(api|pr\s+(merge|create|comment|edit|ready)|issue\s+(create|comment|edit|close)|run\s+rerun)\b"

PUSH_FLAGS = {"-u", "--set-upstream", "--force-if-includes", "-q", "--quiet", "-v", "--verbose"}
BRANCH = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]*(/[A-Za-z0-9][A-Za-z0-9._-]*)*")
NAMESPACES = ("heads/", "tags/", "remotes/", "refs/", "notes/", "pull/")
TAG_LIKE = re.compile(r"[vV]\d")


def branch_name(ref):
    """The plain branch a ref names (refs/heads/<name> or <name>), or None."""
    name = ref.removeprefix("refs/heads/")
    if not BRANCH.fullmatch(name) or name.startswith(NAMESPACES) or ".." in name \
            or name.endswith(".lock") or name == "HEAD" or name.lower() in ("main", "master") \
            or TAG_LIKE.match(name):  # a tag may exist only on the remote
        return None
    return name


def current(cwd, git_out):
    name = (git_out(cwd, "rev-parse", "--abbrev-ref", "HEAD") or "").strip()
    return branch_name(name) if name and name != "HEAD" else None


def is_ref(cwd, ref, git_out):
    return git_out(cwd, "show-ref", "--verify", "-q", ref) is not None


def refspec_dest(ref, cwd, git_out):
    """(the plain branch the refspec writes, None) or (None, reason)."""
    src, colon, dst = ref.partition(":")
    if ref.startswith("+") or ":" in dst:
        return None, f"refspec {ref} (force or malformed; use --force-with-lease)"
    src_b = current(cwd, git_out) if src == "HEAD" else branch_name(src)
    if not src_b or not is_ref(cwd, f"refs/heads/{src_b}", git_out):
        return None, f"{src or 'an empty source'} is not a local branch other than main/master"
    dst_b = branch_name(dst) if colon else src_b
    if not dst_b:
        return None, f"{dst} is not a plain branch name"
    if colon and dst_b != src_b:  # HEAD:<dst> only for the current branch's own name
        return None, f"{ref} pushes a branch under another name"
    if is_ref(cwd, f"refs/tags/{dst_b}", git_out):
        return None, f"{dst_b} is also a tag name"
    return dst_b, None


def bare_dest(cwd, git_out, explicit_remote):
    """(the current branch, None) when a push without a refspec writes only that branch."""
    b = current(cwd, git_out)
    if not b:
        return None, "the current branch is main/master, detached or not a plain name"
    if (git_out(cwd, "config", "--get", "push.default") or "").strip() not in ("", "simple", "current"):
        return None, "push.default isn't simple or current"
    if git_out(cwd, "config", "--get-regexp", r"^remote\..*\.push$"):
        return None, "a remote.*.push refspec is configured"
    want = f"origin/{b}"
    for rev in ("@{upstream}", "@{push}"):
        up = (git_out(cwd, "rev-parse", "--abbrev-ref", "--symbolic-full-name", rev) or "").strip()
        if up and up != want:
            return None, f"{b}'s {rev} is {up}"
        if not up and rev == "@{push}" and not explicit_remote:
            return None, f"{b} has no push target (use git push -u origin {b})"
    return b, None


def check_push(words, cwd, git_out):
    """A reason to ask for one `git ... push ...` simple command, or None."""
    ok, cwd, sub, args = git_parts(words, cwd)
    if not ok or sub != "push":
        return "git options other than -C/--no-pager before push"
    if cwd is UNKNOWN:
        return "a git -C directory the guard can't follow"
    leases, pos = [], []
    for a in args:
        if "#" in a:
            return f"git push argument {a} contains #"
        if a == "--force-with-lease" or a.startswith("--force-with-lease="):
            leases.append(a.partition("=")[2])
        elif a.startswith("-"):
            if a not in PUSH_FLAGS:
                return f"git push {a}"
        else:
            pos.append(a)
    if pos and pos[0] != "origin":
        return f"git push to {pos[0]}, not origin"
    if len(pos) > 2:
        return "git push with more than one refspec"
    dest, why = refspec_dest(pos[1], cwd, git_out) if len(pos) == 2 else bare_dest(cwd, git_out, bool(pos))
    if why:
        return f"git push: {why}"
    for lease in leases:
        name, sha = lease.partition(":")[::2]
        if lease and branch_name(name) != dest:
            return f"--force-with-lease={lease} names a ref other than {dest}"
        if sha and not is_ref(cwd, f"refs/remotes/origin/{dest}", git_out):
            return f"--force-with-lease={lease} on a branch with no refs/remotes/origin/{dest}"
    return None


def kind(words):
    words = bare(words)
    names = [w.rsplit("/", 1)[-1] for w in words]
    if words and words[0] == "git":
        ok, _, sub, args = git_parts(words, UNKNOWN)
        if sub == "push" or (not ok and "push" in args):
            return "push"
    elif "git" in names and "push" in words:
        return "push"
    return gh_rules.kind(words)


def check(cmd, cwd, git_out):
    """(decision, reason) when cmd needs the maintainer or must be denied, else None."""
    segs = segments(cmd)
    why = codeexec.check(segs, cwd)
    if why:
        return "ask", why
    kinds = [k for k in map(kind, segs) if k]
    text = strip_heredocs(cmd)
    if not kinds:
        if re.search(GIT + r"push\b", text) or re.search(OUTWARD_GH, text) \
                or (not tokens(cmd) and re.search(r"\b(push|gh)\b", cmd)):
            return "ask", "a git push or gh command the guard can't parse"
        return None
    words = single(cmd)
    if words is None or len(segs) != 1:
        return "ask", (f"{kinds[0]} must run as one simple command (no cd, ;, &&, pipes, "
                       "redirects, heredocs, $ or backticks; use git -C and --body-file)")
    if words[0] != ("git" if kinds[0] == "push" else "gh"):
        return "ask", f"{kinds[0]} behind another command ({words[0]})"
    if kinds[0] == "push":
        why = check_push(words, cwd, git_out)
        return ("ask", why) if why else None
    return gh_rules.check(kinds[0], words, cwd)
