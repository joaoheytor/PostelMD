"""Whether a command the guard can't parse still stays local (trust.py, unparsed-local).

The guard asks (CAN'T PARSE) when a command that mentions git, go, gh, npm, npx, make or
scripts/ uses shell syntax its checks don't model: $VAR, $(…), globs, braces, loops. With
the opt-in, local_only() lets such a command on to the guard's remaining checks (cd,
foreign repository, outward, shell writes) only when, on the raw text and on shlex's words:
  - no outward tool or address appears anywhere, even quoted (OUTWARD, GIT_OUTWARD, ://,
    user@host:), and git, npm and go run only local subcommands with literal names
    (GIT_LOCAL, NPM_LOCAL; git takes no global option but -C and --no-pager);
  - no command runs text the guard can't see: no shell, interpreter, eval or source, no
    wrapper, find -exec, xargs or awk, no command name built from $ or `, no path command
    except the allowlisted scripts (HIDDEN, COMMAND);
  - every $ is a variable the command sets itself (NAME=, for NAME, read NAME; not one a
    program reads, shvars.harmless) or $? $# $$ $! $- $0, plain or ${NAME%…}/${NAME#…};
  - the working directory and every path (absolute, ~ or with ..) are inside the project,
    at their logical and physical paths;
  - shscan raises no flag ($'…', backslash-newline, odd heredocs, open quotes) and there is
    no glob qualifier x(…) or zsh =cmd.
Anything it can't decide is not local. A guardrail like the rest of the guard, not a boundary.
"""
import os
import re

from bash_writes import tokens
from shparse import SHELLS, WRAPPERS, bare, segments
from shscan import attached_paren, blanked, expandable, scan
from shvars import harmless
from trust import project_roots, under

EDGE_L, EDGE_R = r"(?<![\w./-])", r"(?![\w./-])"  # a word, not part of a path or name
OUTWARD = re.compile(EDGE_L + r"(gh|curl|wget|ssh|scp|sftp|rsync|nc|ncat|netcat|socat|telnet"
                     r"|ftp|tftp|aria2c|http|https|xh|openssl|docker|podman|buildah|skopeo"
                     r"|crane|oras|cosign|kubectl|helm|aws|gcloud|az|terraform|npx|pnpm|yarn"
                     r"|twine|sendmail|mail|mutt|dig|nslookup)" + EDGE_R)
GIT_OUTWARD = re.compile(EDGE_L + r"(push|fetch|pull|clone|remote|config|submodule|send-email"
                         r"|send-pack|request-pull|credential[\w-]*|ls-remote|lfs|daemon|archive"
                         r"|bundle|svn|p4|http-\w+|upload-pack|receive-pack|fetch-pack|instaweb"
                         r"|core\.\w+|sshCommand)" + EDGE_R, re.I)
ADDRESS = re.compile(r"://|[\w.-]@[\w.-]+:|/dev/(tcp|udp)/")
GIT_LOCAL = {"status", "log", "diff", "show", "branch", "rev-parse", "rev-list", "ls-files",
             "ls-tree", "cat-file", "grep", "add", "rm", "mv", "restore", "checkout",
             "switch", "commit", "stash", "merge-base", "merge", "rebase", "cherry-pick",
             "revert", "reset", "blame", "shortlog", "describe", "for-each-ref", "show-ref",
             "name-rev", "worktree", "diff-tree", "diff-files", "diff-index", "check-ignore",
             "check-attr", "range-diff", "cherry", "reflog", "whatchanged", "count-objects",
             "verify-commit", "format-patch", "apply", "clean"}
NPM_LOCAL = {"test", "t", "run", "run-script", "ci", "ls", "explain", "why"}
HIDDEN = SHELLS | WRAPPERS | {
    "eval", "source", ".", "exec", "alias", "builtin", "command", "node", "nodejs", "perl",
    "ruby", "php", "deno", "bun", "lua", "awk", "gawk", "mawk", "nawk", "osascript", "pwsh",
    "powershell", "tclsh", "expect", "java", "Rscript", "su", "pkexec", "typeset",
    "declare", "local", "export", "readonly", "integer", "float", "set", "trap", "function"}
SCRIPTS = r"(\./)?scripts/(adr-immutable|check-file-size|fuzz|image-scan|image-smoke" \
          r"|image-smoke-adopt|pre-commit-adr_test|release-meta_test|verify-release-tag_test)\.sh"
COMMAND = re.compile(rf"[\w.+:-]+|\[\[?|\]\]?|\}}|{SCRIPTS}|(\./)?bin/postelmd")
EXEC_FLAGS = {"-exec", "-execdir", "-ok", "-okdir"}
REF = re.compile(r"\$(?:\{(\w+)(?:(?:%%?|##?)[^}$`]*)?\}|(\w+)|([?#$!0-]|(?=[\s\"(])|$))")


def outward(cmd):
    words = " ".join(tokens(cmd))
    texts = [cmd, re.sub(r"\\\n|[\\'\"]", "", cmd), words]
    if any(OUTWARD.search(t) or ADDRESS.search(t) for t in texts):
        return True
    return any(re.search(EDGE_L + "git" + EDGE_R, t) and GIT_OUTWARD.search(t) for t in texts)


def assigned(segs, toks):
    """The variables cmd sets, or None when one of them is a name a program may read."""
    names = [m.group(1) for t in toks for m in [re.match(r"(\w+)=", t)] if m]
    for words in segs:
        if words[0] == "for" and len(words) > 1:
            names.append(words[1])
        w = bare(words)
        if w and w[0] == "read":
            names += [a for a in w[1:] if re.fullmatch(r"\w+", a)]
    return set(names) if all(harmless(n) for n in names) else None


def expansions_ok(cmd, names):
    """True when every $ the shell expands is a REF form, naming only variables in names."""
    text = expandable(scan(cmd))
    for i in (m.start() for m in re.finditer(r"\$", text)):
        m = REF.match(text, i)
        if not m or ((m.group(1) or m.group(2)) and (m.group(1) or m.group(2)) not in names):
            return False
    return True


def command_ok(words):
    w = bare(words)
    if not w:
        return True
    name = os.path.basename(w[0])
    if not COMMAND.fullmatch(w[0]) or name in HIDDEN or re.fullmatch(r"python[\d.]*", name):
        return False
    if any(a in EXEC_FLAGS for a in w):
        return False
    if name == "git":
        i = 1
        while i < len(w) and w[i].startswith("-"):
            if w[i] == "-C" and i + 1 < len(w) and "$" not in w[i + 1]:
                i += 2
            elif w[i] == "--no-pager":
                i += 1
            else:
                return False
        return i < len(w) and w[i] in GIT_LOCAL
    if name == "npm":
        return len(w) > 1 and w[1] in NPM_LOCAL
    if name == "go":
        return len(w) > 1 and w[1] not in ("get", "install") and not any("@" in a for a in w)
    return True


def leaves(piece, cwd, roots):
    """True when piece may name a path outside the project."""
    if piece in ("/dev/null", "/dev/stdout", "/dev/stderr", "/dev/stdin"):
        return False
    if piece.startswith("~") and not re.match(r"~(/|$)", piece):
        return True  # ~user, ~+, ~-: other users' homes, $PWD, $OLDPWD
    if not (piece.startswith(("/", "~")) or re.search(r"(^|/)\.\.(/|$)", piece)):
        return False
    path = os.path.join(cwd, os.path.expanduser(piece))
    return not (under(os.path.normpath(path), roots) and under(os.path.realpath(path), roots))


def paths_ok(toks, cwd, roots):
    for t in toks:
        pieces = [t] + re.split(r"[=:,]", t)[1:] + ([t[2:]] if re.match(r"-\w[/~.]", t) else [])
        if any(leaves(p, cwd, roots) for p in pieces):
            return False
    return True


def local_only(cmd, cwd):
    """True when cmd, which the guard can't parse, provably stays local (module doc)."""
    roots = project_roots()
    toks = tokens(cmd)
    s = scan(cmd)
    if not roots or not toks or s.flags or not isinstance(cwd, str):
        return False
    if not under(os.path.normpath(cwd), roots) or not under(os.path.realpath(cwd), roots):
        return False
    if attached_paren(blanked(s)) or re.search(r"(?:^|[\s;&|()<>])=[\w./~-]", blanked(s)):
        return False
    if outward(cmd):
        return False
    segs = segments(cmd)
    names = assigned(segs, toks)
    if names is None or not expansions_ok(cmd, names):
        return False
    return all(command_ok(w) for w in segs) and paths_ok(toks, cwd, roots)
