#!/usr/bin/env python3
"""Claude Code PreToolUse guard for PostelMD (a PUBLIC repository).

Modes (first CLI argument):
  secrets  Write/Edit: block high-confidence secret formats.
  public   Write/Edit: ask before writing content that should rarely be public
           (absolute home paths, private IPs, unknown email addresses, and
           terms from the git-ignored local denylist .claude/public-guard.local.txt).
  adr      Write/Edit: ask before changing the Decision section of an ADR that is Accepted
           on main (at the merge base of HEAD and origin/main, or at origin/main's tip; HEAD
           without origin/main). scripts/adr-immutable.sh is the enforced check (pre-commit, CI).
  git      Bash: enforce DCO sign-off + Conventional Commit subjects; forbid skipping hooks;
           routine branch and PR work passes (pushes of a non-main branch to origin, PR and
           issue create/edit/comment, a squash merge pinned to the reviewed head, run reruns;
           outward.py); everything else outward-facing (pushes to main/master, force, tags,
           releases, closing or reviewing PRs, repo settings, mutating API calls) gets a human
           permission prompt ("ask"); ask before shell writes to repository files (bash_writes.py).

These are guardrails for an agent, not a security boundary: CI and branch protection are the
hard gates.

The hook reads the tool call as JSON on stdin and answers with a
hookSpecificOutput JSON object (deny/ask) or exits 0 silently, leaving the decision to the
permission rules in .claude/settings.json.
Only the Python standard library is used. Tests: *_test.py here (make test-scripts).
"""
import json
import os
import re
import subprocess
import sys

from bash_writes import SAFE_GIT, SAFE_GIT_ENV, repo_write_targets
from outward import CMD_START, GH, GIT, check as check_outward

CONVENTIONAL = r"(feat|fix|perf|refactor|docs|test|build|ci|chore|revert)(\([\w./-]+\))?!?: \S"

SECRET_PATTERNS = [
    r"(AKIA|ASIA)[0-9A-Z]{16}",                           # AWS access key IDs
    r"AIza[0-9A-Za-z_-]{35}",                             # Google API key
    r"glpat-[0-9A-Za-z_-]{20,}",                          # GitLab PAT
    r"npm_[A-Za-z0-9]{36}",                               # npm token
    r"(sk|rk)_live_[0-9A-Za-z]{20,}",                     # Stripe live keys
    r"hooks\.slack\.com/services/T[0-9A-Z]+/B[0-9A-Z]+/[0-9A-Za-z]+",  # Slack webhook
    r"gh[pousr]_[A-Za-z0-9]{36}",                         # GitHub tokens
    r"github_pat_[A-Za-z0-9_]{22,}",                      # GitHub fine-grained PAT
    r"dckr_pat_[A-Za-z0-9_-]{20,}",                       # Docker Hub PAT
    r"sk-(ant-)?[A-Za-z0-9_-]{32,}",                      # OpenAI / Anthropic style keys
    r"xox[baprs]-[A-Za-z0-9-]{10,}",                      # Slack
    r"AGE-SECRET-KEY-1[0-9A-Z]{58}",                      # age identity
    r"-----BEGIN( [A-Z0-9]+)* PRIVATE KEY( BLOCK)?-----", # PEM / OpenSSH / PGP private keys
]
# Paths where secret-SHAPED fixtures are legitimate (the secret scanner's own tests):
# downgrade deny -> ask so a human confirms the value is fake. Failure scenarios live in
# _test.go files next to the code they cover.
SECRET_FIXTURE_PATHS = re.compile(r"(^|/)testdata/|_test\.go$")

ALLOWED_EMAILS = re.compile(
    r"^(jhkpereira@gmail\.com|noreply@anthropic\.com|.+@example\.(com|org|net)|you@example\.com)$",
    re.I,
)
HOME_PATH = re.compile(r"(/home/[a-z][a-z0-9_-]*/|/Users/[^/\s]+/)")
PRIVATE_IP = re.compile(
    r"\b(10\.\d{1,3}\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3}"
    r"|172\.(1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3}|100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.\d{1,3}\.\d{1,3})\b"
)
# Bounded quantifiers keep this linear on large inputs (an unbounded local part is quadratic).
EMAIL = re.compile(r"[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9-]{1,63}(?:\.[A-Za-z0-9-]{1,63}){1,8}")
TEST_PATHS = re.compile(r"(^|/)(test|testdata|tests)/|_test\.go$|\.test\.ts$")


def respond(decision, reason):
    print(json.dumps({"hookSpecificOutput": {
        "hookEventName": "PreToolUse",
        "permissionDecision": decision,
        "permissionDecisionReason": reason,
    }}))
    sys.exit(0)


def written_text(tool_input):
    parts = [tool_input.get("content") or "", tool_input.get("new_string") or ""]
    for edit in tool_input.get("edits") or []:
        parts.append(edit.get("new_string") or "")
    return "\n".join(p for p in parts if p)


def rel_path(tool_input):
    path = tool_input.get("file_path") or ""
    root = os.environ.get("CLAUDE_PROJECT_DIR", "")
    if root and path.startswith(root.rstrip("/") + "/"):
        return path[len(root.rstrip("/")) + 1:]
    return path


def check_secrets(tool_input):
    text = written_text(tool_input)
    hits = [p for p in SECRET_PATTERNS if re.search(p, text)]
    if not hits:
        return
    path = rel_path(tool_input)
    if SECRET_FIXTURE_PATHS.search(path):
        respond("ask", f"Secret-shaped string in test fixture {path}. Confirm it is a FAKE value "
                       "(this repository is public).")
    respond("deny", "BLOCKED: secret-shaped string in the content being written (AWS/GitHub/Docker "
                    "Hub/Slack/API key/age/private key). PostelMD is a PUBLIC repository: reference "
                    "an env var or *_FILE path instead. Patterns: .claude/hooks/postelmd_guard.py.")


def local_denylist():
    root = os.environ.get("CLAUDE_PROJECT_DIR", ".")
    path = os.path.join(root, ".claude", "public-guard.local.txt")
    try:
        with open(path, encoding="utf-8") as fh:
            return [ln.strip() for ln in fh if ln.strip() and not ln.startswith("#")]
    except OSError:
        return []


def check_public(tool_input):
    path = rel_path(tool_input)
    if path.endswith("public-guard.local.txt") or path.startswith(".claude/hooks/"):
        return
    text = written_text(tool_input)
    reasons = []
    if HOME_PATH.search(text):
        reasons.append("an absolute home-directory path")
    if not TEST_PATHS.search(path) and PRIVATE_IP.search(text):
        reasons.append("a private/Tailscale IP address")
    emails = {m.group(0) for m in EMAIL.finditer(text)}
    unknown = sorted(e for e in emails if not ALLOWED_EMAILS.match(e))
    if unknown:
        reasons.append("email address(es) " + ", ".join(unknown))
    lowered = text.lower()
    terms = [t for t in local_denylist() if t.lower() in lowered]
    if terms:
        reasons.append("term(s) from the local public-guard denylist")
    if reasons:
        respond("ask", f"PUBLIC REPO CHECK for {path or 'file'}: content contains "
                       + "; ".join(reasons) + ". Confirm this is meant to be published.")


def git_out(cwd, *args):
    """git's stdout, or None when git fails or isn't there. SAFE_GIT: a repository's own
    config can't make this run its commands (core.fsmonitor, hooks)."""
    try:
        r = subprocess.run([*SAFE_GIT, "-C", cwd, *args], capture_output=True, text=True,
                           timeout=5, env=SAFE_GIT_ENV)
    except (OSError, subprocess.SubprocessError):
        return None
    return r.stdout if r.returncode == 0 else None


ACCEPTED = re.compile(r"^- \*\*Status:\*\* Accepted", re.M)


def adr_on_main(full):
    """(the ADR as Accepted on main, where): main is the merge base of HEAD and origin/main
    or origin/main's tip (a stale fork point mustn't hide an ADR accepted since), or HEAD
    without origin/main. The text is "" when no ref has it Accepted (Proposed, or still being
    written on this branch), and None outside a git repository."""
    d = os.path.dirname(full) or "."
    if git_out(d, "rev-parse", "--verify", "-q", "HEAD^{commit}") is None:
        return None, "HEAD"
    base = (git_out(d, "merge-base", "HEAD", "origin/main") or "").strip()
    refs = [(base, "main"), ("origin/main", "main")] if base else [
        ("HEAD", "HEAD (no origin/main to compare with)")]
    for ref, where in refs:
        text = git_out(d, "show", f"{ref}:./{os.path.basename(full)}")
        if text and ACCEPTED.search(text):
            return text, where
    return "", refs[0][1]


def check_adr(tool_input):
    path = rel_path(tool_input)
    if not re.match(r"docs/adr/\d{4}-.+\.md$", path):
        return
    full = tool_input.get("file_path") or ""
    try:
        with open(full, encoding="utf-8") as fh:
            current = fh.read()
    except OSError:
        return
    on_main, where = adr_on_main(full)
    if on_main is None:  # not in git: judge the file as it is
        on_main, where = current, "the file on disk"
    if not ACCEPTED.search(on_main):
        return  # Proposed on main, or new on this branch: still editable
    if tool_input.get("content") is not None:
        respond("ask", f"{path} is ACCEPTED on {where}: rewriting the whole file may change its "
                       "Decision. Accepted decisions change only by superseding; clarifications "
                       "are append-only.")
    m = re.search(r"^## Decision\n(.*?)(?=^## )", current, re.M | re.S)
    olds = [tool_input.get("old_string") or ""] + [e.get("old_string") or "" for e in tool_input.get("edits") or []]
    if m and any(o and o in m.group(1) for o in olds):
        respond("ask", f"{path} is ACCEPTED on {where} and this edit touches its Decision "
                       "section. Supersede that part with a new ADR instead (the new-adr skill); "
                       "a dated Clarification may only add rationale, links or typo fixes.")


# Outward-facing commands that always need the maintainer. Pushes and `gh pr merge` are
# judged by outward.py; PR/issue create, edit and comment, and `gh run rerun`, pass.
GATED = [
    (GIT + r"tag\b(?!\s+(-l|--list)\b)\s+\S", "creating/changing git tags"),
    (GH + r"release\b", "GitHub releases"),
    (GH + r"repo\s+(create|edit|delete|rename|archive|fork)\b", "creating or changing repositories"),
    (GH + r"(secret|variable|ruleset|label|gist)\s+(?!list\b|view\b)\S", "repo secrets/variables/rulesets/labels or gists"),
    (GH + r"pr\s+(close|review|reopen)\b", "closing, reopening or reviewing pull requests"),
    (GH + r"issue\s+(reopen|delete|transfer)\b", "reopening, deleting or transferring issues"),
    (GH + r"(pr\s+(create|ready|edit|comment|merge)|issue\s+(create|comment|edit|close)|run\s+rerun)"
          r"\b[^;&|\n]*(\s-R|--repo\b)", "acting on another repository"),
    (GH + r"(workflow\s+(run|enable|disable)|run\s+(cancel|delete))\b", "running or changing workflows"),
    (GH + r"api\b[^;&|\n]*(-X\s*|--method[=\s]+)(POST|PUT|PATCH|DELETE)\b", "mutating GitHub API calls"),
    (GH + r"api\b[^;&|\n]*(\s-[fF]\b|--field\b|--raw-field\b|--input\b)", "mutating GitHub API calls (fields imply POST)"),
]


def check_commit(cmd):
    if re.search(GIT + r"(commit|push|merge|rebase|am|cherry-pick)\b[^;&|\n]*--no-verify", cmd) \
            or re.search(GIT + r"commit\b[^;&|\n]*\s-[a-zA-Z]*n[a-zA-Z]*\b", cmd) \
            or re.search(CMD_START + r"git\b[^;&|\n]*-c\s*core\.hooksPath", cmd):
        respond("deny", "BLOCKED: this skips or replaces the repository's git hooks "
                        "(--no-verify, commit -n, -c core.hooksPath). Fix the hook failure instead.")
    if re.search(GIT + r"commit\b", cmd) and not re.search(r"--amend\b.*--no-edit", cmd):
        if not re.search(r"(\s-s\b|\s--signoff\b|\s-[a-zA-Z]*s[a-zA-Z]*\b)", cmd):
            respond("deny", "BLOCKED: commits need a DCO sign-off. Add -s (git commit -s ...).")
        if re.search(r"\s-m\b", cmd) and not re.search(r"(-m\s+[\"']?\$?\(?(cat\s+<<-?'?EOF'?\s*)?|\n)\s*" + CONVENTIONAL, cmd):
            respond("deny", "BLOCKED: the commit subject must be a Conventional Commit, e.g. "
                            "'docs(adr): clarify save protocol' (types: feat fix perf refactor docs "
                            "test build ci chore revert; '!' for breaking).")


def check_git(tool_input, cwd):
    cmd = tool_input.get("command") or ""
    if not re.search(r"\b(git|gh)\b", cmd):
        return
    check_commit(cmd)  # denials first: `git commit -m x && git push` is still a bad commit
    for pattern, label in GATED:
        if re.search(pattern, cmd):
            respond("ask", f"OUTWARD-FACING: {label}. PostelMD is public; approve only if the "
                           "maintainer asked for exactly this in the conversation.")
    reason = check_outward(cmd, cwd, git_out)
    if reason:
        respond("ask", f"OUTWARD-FACING: {reason}. Branch pushes to origin and pinned squash "
                       "merges pass; this needs the maintainer's go-ahead in the conversation.")


def check_bash_writes(tool_input, cwd):
    targets = repo_write_targets(tool_input.get("command") or "", cwd)
    if targets:
        respond("ask", "SHELL WRITE to repository file(s) " + ", ".join(targets[:5]) + ". Use "
                       "Write/Edit so the guard hook sees the change (secrets, public-repo and ADR "
                       "checks); approve only for generated output that Write/Edit can't produce.")


def main():
    mode = sys.argv[1] if len(sys.argv) > 1 else ""
    try:
        payload = json.load(sys.stdin)
    except (ValueError, OSError):
        return  # never block on a malformed payload
    tool_input = payload.get("tool_input") or {}
    if mode == "git":
        cwd = payload.get("cwd") or os.getcwd()
        check_git(tool_input, cwd)  # deny/ask on git and gh commands wins over the write check
        check_bash_writes(tool_input, cwd)
        return
    {"secrets": check_secrets, "public": check_public, "adr": check_adr}.get(
        mode, lambda _: None)(tool_input)


if __name__ == "__main__":
    main()
