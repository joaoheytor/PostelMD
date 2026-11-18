"""Whether a command the guard can't parse is simple enough to judge anyway (trust.py,
unparsed-local).

The guard asks (CAN'T PARSE) when a command that mentions git, go, gh, npm, npx, make or
scripts/ uses shell syntax its checks don't model: globs, braces, # comments, loops, $VAR.
With the opt-in, local_only() lets such a command on to the guard's remaining checks (cd,
foreign repository, outward and code-running options, shell writes) only when every word
those checks read is literal, so they judge what will run:
  - nothing runs that the text doesn't show: no $( ), backticks, <( ) or >( ) anywhere,
    no unquoted heredoc body with $ or `, no read, no shell, interpreter, eval, source,
    wrapper, xargs, awk or find -exec, no command name built from $ (HIDDEN, COMMAND);
  - no outward tool, git network or config word, URL or user@host: anywhere, even quoted
    (OUTWARD, GIT_OUTWARD, ADDRESS); git runs only GIT_LOCAL subcommands with -C or
    --no-pager, go only GO_LOCAL (env without -w), npm only NPM_LOCAL (no ci or install);
  - in a git, go, npm or make command no word holds $ or `, except an operand after a
    literal --; anywhere else a word with $ must start with an existing directory inside
    the project (sub/$f), and use only loop variables of `for NAME in` plain names
    (SAFE_VALUE: no /, no leading - or dot, no doubled dot) that the command never names
    elsewhere (printf -v, NAME=, let, unset, ...), never ones a program reads, written
    as a plain ${NAME} or as $NAME followed by a delimiter (no zsh :h, :s/a/b/, [1]);
  - the working directory and every word, taken as a path (logical and physical), are
    inside the project;
  - shscan raises no flag ($'…', backslash-newline, odd heredocs, open quotes) and there is
    no glob qualifier x(…) or zsh =cmd.
Downloads and code the project itself runs (make targets, npm run scripts, go test) are
local by this definition. Anything it can't decide is not local. A guardrail, not a boundary.
"""
import os
import re

from bash_writes import tokens
from shparse import SHELLS, WRAPPERS, bare, segments
from shscan import attached_paren, blanked, expandable, scan, stripped
from shvars import FOLLOWER, harmless
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
GO_LOCAL = {"build", "test", "vet", "list", "env"}
NPM_LOCAL = {"test", "t", "run", "run-script", "ls", "explain", "why"}
TOOLS = {"git", "go", "npm", "make"}
HIDDEN = SHELLS | WRAPPERS | {
    "eval", "source", ".", "exec", "alias", "builtin", "command", "node", "nodejs", "perl",
    "ruby", "php", "deno", "bun", "lua", "awk", "gawk", "mawk", "nawk", "osascript", "pwsh",
    "powershell", "tclsh", "expect", "java", "Rscript", "su", "pkexec", "typeset", "read",
    "declare", "local", "export", "readonly", "integer", "float", "set", "trap", "function",
    "mapfile", "readarray", "getopts", "select", "let", "wait", "unset", "vared"}
SCRIPTS = r"(\./)?scripts/(adr-immutable|check-file-size|fuzz|image-scan|image-smoke" \
          r"|image-smoke-adopt|pre-commit-adr_test|release-meta_test|verify-release-tag_test)\.sh"
COMMAND = re.compile(rf"[\w.+:-]+|\[\[?|\]\]?|\}}|{SCRIPTS}|(\./)?bin/postelmd")
EXEC_FLAGS = {"-exec", "-execdir", "-ok", "-okdir"}
SAFE_VALUE = re.compile(r"\w[\w-]*(\.[\w-]+)*")  # not an option, not . or ..
REF = re.compile(r"\$(?:\{(\w+)\}|(\w+))")
DOTDOT = re.compile(r"(^|/)\.\.(/|$)")


def outward(cmd):
    texts = [cmd, re.sub(r"\\\n|[\\'\"]", "", cmd), " ".join(tokens(cmd))]
    if any(OUTWARD.search(t) or ADDRESS.search(t) for t in texts):
        return True
    return any(re.search(EDGE_L + "git" + EDGE_R, t) and GIT_OUTWARD.search(t) for t in texts)


def hidden_code(s):
    """True when the shell may run a command the text doesn't show as words."""
    text = stripped(s)
    if "`" in text or re.search(r"[$<>]\(", text):
        return True
    return any(not quoted and ("$" in body or "`" in body) for quoted, body in s.bodies)


def safe_names(segs, toks):
    """The loop variables of `for NAME in <SAFE_VALUE words>` whose name appears nowhere
    else as a word (printf -v NAME, NAME=, let, unset, ...), or None when the command
    assigns a name a program may read."""
    if not all(harmless(m.group(1)) for t in toks for m in [re.match(r"(\w+)\+?=", t)] if m):
        return None
    values = {}
    for words in segs:
        if words[0] == "for" and len(words) > 1:
            values.setdefault(words[1], []).extend(words[3:] if words[2:3] == ["in"] else ["?"])
    names = set()
    for name, vals in values.items():
        fors = sum(1 for w in segs if w[0] == "for" and w[1:2] == [name])
        other = re.compile(rf"{name}|-\w*{name}|{name}(\+?=|\[).*")
        if harmless(name) and sum(1 for t in toks if other.fullmatch(t)) == fors \
                and all(SAFE_VALUE.fullmatch(v) for v in vals):
            names.add(name)
    return names


def expansions_ok(cmd, names):
    """True when every $ the shell expands is a plain ${NAME}, or $NAME followed by a
    delimiter (not zsh's :s/a/b/, :h, :a, [1] or (…)), naming only variables in names."""
    text = expandable(scan(cmd))
    for i in (m.start() for m in re.finditer(r"\$", text)):
        m = REF.match(text, i)
        if not m or (m.group(1) or m.group(2)) not in names \
                or (m.group(2) and not FOLLOWER.match(text, m.end())):
            return False
    return True


def dollar_word_ok(word, cwd, roots):
    """A word with $ outside tool options: an existing directory inside the project, then
    `/` and the rest, with no literal .. in it."""
    m = re.match(r"([\w.-]+(?:/[\w.-]+)*)/", word)
    if not m or DOTDOT.search(word):
        return False
    d = os.path.join(cwd, m.group(1))
    return os.path.isdir(d) and under(os.path.normpath(d), roots) \
        and under(os.path.realpath(d), roots)


def command_ok(words, cwd, roots):
    w = bare(words)
    if not w:
        return True
    name = os.path.basename(w[0])
    if not COMMAND.fullmatch(w[0]) or name in HIDDEN or re.fullmatch(r"python[\d.]*", name) \
            or any(a in EXEC_FLAGS for a in w) \
            or (name in ("printf", "print") and any(a.startswith("-") for a in w[1:])):
        return False  # printf -v NAME, zsh print -v NAME: assignments
    for i, a in enumerate(w):
        if ("$" in a or "`" in a) and (name in TOOLS and ("--" not in w[:i] or a.startswith("-"))
                                       or not dollar_word_ok(a, cwd, roots)):
            return False
    if name == "git":
        i = 1
        while i < len(w) and w[i] in ("-C", "--no-pager"):
            i += 2 if w[i] == "-C" else 1
        return i < len(w) and w[i] in GIT_LOCAL
    if name == "npm":
        return len(w) > 1 and w[1] in NPM_LOCAL
    if name == "go":
        return len(w) > 1 and w[1] in GO_LOCAL and not (w[1] == "env" and "-w" in w)
    return True


def leaves(piece, cwd, roots):
    """True when piece, taken as a path from cwd, may lead outside the project."""
    if "$" in piece or piece in ("", "/dev/null", "/dev/stdout", "/dev/stderr", "/dev/stdin"):
        return False  # $ words: dollar_word_ok
    if piece.startswith("~") and not re.match(r"~(/|$)", piece):
        return True  # ~user, ~+, ~-: other users' homes, $PWD, $OLDPWD
    path = os.path.join(cwd, os.path.expanduser(piece))
    return not (under(os.path.normpath(path), roots) and under(os.path.realpath(path), roots))


def paths_ok(toks, cwd, roots):
    for t in toks:
        pieces = [t] + re.split(r"[=:,]", t)[1:] + ([t[2:]] if re.match(r"-\w[/~.]", t) else [])
        if any(leaves(p, cwd, roots) for p in pieces):
            return False
    return True


def local_only(cmd, cwd):
    """True when cmd, which the guard can't parse, is simple enough to judge (module doc)."""
    roots = project_roots()
    toks = tokens(cmd)
    s = scan(cmd)
    if not roots or not toks or s.flags or not isinstance(cwd, str):
        return False
    if not under(os.path.normpath(cwd), roots) or not under(os.path.realpath(cwd), roots):
        return False
    if hidden_code(s) or attached_paren(blanked(s)) \
            or re.search(r"(?:^|[\s;&|()<>])=[\w./~-]", blanked(s)) or outward(cmd):
        return False
    segs = segments(cmd)
    names = safe_names(segs, toks)
    if names is None or not expansions_ok(cmd, names):
        return False
    return all(command_ok(w, cwd, roots) for w in segs) and paths_ok(toks, cwd, roots)
