"""Which gh commands pass without a prompt (postelmd_guard.py, git mode, via outward.py).

Each is judged as one simple command (shparse.single) against an allowlist of arguments;
anything not on it asks. Never --repo/-R. PR and issue text (--title, --body, --comment and
the file of --body-file/-F) is scanned like Write/Edit content: a secret denies, private-
looking content asks.
  merge  gh pr merge [N] --squash --match-head-commit <40-hex sha> [--delete-branch] [--auto]
  text   gh pr create|comment|edit, gh issue create|comment|edit|close
  plain  gh pr ready [N], gh run rerun [ID] [--failed] [--debug] [--job ID]
  api    only read-only forms: one endpoint (not graphql) and output flags; no -X/--method,
         -f/-F/--field/--raw-field, --input or -H. (.claude/settings.json allows no gh api.)
"""
import os
import re

from content import has_secret, public_reasons

SHA40 = re.compile(r"[0-9a-f]{40}")
NUMBER = re.compile(r"\d+")
TEXT_FLAGS = {"-t", "--title", "-b", "--body", "-c", "--comment"}
FILE_FLAGS = {"-F", "--body-file"}
VALUE_FLAGS = {"-B", "--base", "-H", "--head", "-l", "--label", "--add-label", "--remove-label",
               "-a", "--assignee", "--add-assignee", "--remove-assignee", "-r", "--reviewer",
               "--add-reviewer", "--remove-reviewer", "-m", "--milestone", "--reason"}
TEXT_BOOLS = {"--fill", "--fill-first", "--fill-verbose", "-d", "--draft", "--dry-run",
              "--no-maintainer-edit", "--edit-last"}
API_BOOLS = {"--paginate", "--slurp", "-i", "--include", "--silent", "--verbose"}
API_VALUES = {"--jq", "-q", "--template", "-t", "--cache"}


def kind(words):
    """'merge', 'text', 'plain', 'api' or None for gh words (gh at any position)."""
    names = [os.path.basename(w) for w in words]
    if "gh" not in names:
        return None
    sub = words[names.index("gh") + 1:][:2]
    if sub[:1] == ["api"]:
        return "api"
    if sub == ["pr", "merge"]:
        return "merge"
    if sub in (["pr", "create"], ["pr", "comment"], ["pr", "edit"], ["issue", "create"],
               ["issue", "comment"], ["issue", "edit"], ["issue", "close"]):
        return "text"
    if sub in (["pr", "ready"], ["run", "rerun"]):
        return "plain"
    return None


def options(args, bools, values):
    """({flag: [values]}, positionals), or a reason to ask for a flag not on the lists."""
    found, pos, i = {}, [], 0
    while i < len(args):
        a = args[i]
        name, eq, val = a.partition("=")
        if a in bools:
            found.setdefault(a, [])
        elif name in values and eq and name.startswith("--"):
            found.setdefault(name, []).append(val)
        elif a in values and i + 1 < len(args):
            found.setdefault(a, []).append(args[i + 1])
            i += 1
        elif a.startswith("-"):
            return None, f"argument {a} is not on the guard's allowlist"
        else:
            pos.append(a)
        i += 1
    return (found, pos), None


def check_merge(args):
    parsed, why = options(args, {"-s", "--squash", "-d", "--delete-branch", "--auto"},
                          {"--match-head-commit"})
    if why:
        return why
    found, pos = parsed
    shas = found.get("--match-head-commit", [])
    if len(pos) > 1 or (pos and not NUMBER.fullmatch(pos[0])):
        return "gh pr merge takes one PR number"
    if not ({"-s", "--squash"} & found.keys()) or len(shas) != 1 or not SHA40.fullmatch(shas[0]):
        return "gh pr merge without both --squash and --match-head-commit <40-hex sha>"
    return None


def read_text(path, cwd):
    if path == "-":
        return None
    try:
        with open(os.path.join(cwd, os.path.expanduser(path)), encoding="utf-8") as fh:
            return fh.read(1 << 20)
    except (OSError, UnicodeDecodeError, TypeError):
        return None


def check_text(args, cwd):
    parsed, why = options(args, TEXT_BOOLS, TEXT_FLAGS | FILE_FLAGS | VALUE_FLAGS)
    if why:
        return "ask", why
    found, pos = parsed
    if len(pos) > 1 or (pos and not NUMBER.fullmatch(pos[0])):
        return "ask", "PR and issue commands take one number here (a URL may name another repository)"
    texts = [v for f in TEXT_FLAGS for v in found.get(f, [])]
    for path in (v for f in FILE_FLAGS for v in found.get(f, [])):
        text = read_text(path, cwd if isinstance(cwd, str) else "/nonexistent")
        if text is None:
            return "ask", f"the guard can't read the body file {path} to scan it"
        texts.append(text)
    text = "\n".join(texts)
    if has_secret(text):
        return "deny", ("BLOCKED: secret-shaped string in the PR or issue text. PostelMD is a "
                        "PUBLIC repository.")
    reasons = public_reasons(text)
    if reasons:
        return "ask", "the PR or issue text contains " + "; ".join(reasons)
    return None


def check_api(args):
    if any(a.lower() == "graphql" for a in args):
        return "gh api graphql can mutate"
    parsed, why = options(args, API_BOOLS, API_VALUES)
    if why:
        return why
    if len(parsed[1]) != 1:
        return "gh api takes one endpoint"
    return None


def check(kind_, words, cwd):
    """(decision, reason) for one gh simple command of the given kind, or None to pass."""
    args = words[2:] if kind_ == "api" else words[3:]
    if any(a.startswith(("-R", "--repo")) for a in args):
        return "ask", "acting on another repository (-R/--repo)"
    if kind_ == "text":
        return check_text(args, cwd)
    if kind_ == "api":
        why = check_api(args)
    elif kind_ == "merge":
        why = check_merge(args)
    else:
        parsed, why = options(args, {"--failed", "--debug", "-d"}, {"--job", "-j"})
        if not why and (len(parsed[1]) > 1 or any(not NUMBER.fullmatch(p) for p in parsed[1])):
            why = "takes one number"
    return ("ask", why) if why else None
