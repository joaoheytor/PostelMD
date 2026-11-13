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
           routine branch and PR work passes, fail closed: one simple command with an
           allowlisted argument set (outward.py, gh_rules.py); everything else outward-facing
           (pushes to main/master, force, tags, releases, closing or reviewing PRs, repo
           settings, mutating API calls) gets a human permission prompt ("ask"); PR and issue
           text is scanned like Write/Edit content; options that make allowed commands run
           other programs ask (codeexec.py); so do shell writes to repository files
           (bash_writes.py). Leading literal assignments (`S=/tmp/x; git -C $S log`) are
           substituted first and the result judged as if typed (shvars.py).

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
from commitmsg import CONVENTIONAL, SUBJECT, check as check_message
from content import has_secret, public_reasons
from gitdir import check as check_gitdir
from outward import CMD_START, GH, GIT, check as check_outward
from shvars import normalize
# Paths where secret-SHAPED fixtures are legitimate (the secret scanner's own tests):
# downgrade deny -> ask so a human confirms the value is fake. Failure scenarios live in
# _test.go files next to the code they cover.
SECRET_FIXTURE_PATHS = re.compile(r"(^|/)testdata/|_test\.go$")


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
    if not has_secret(written_text(tool_input)):
        return
    path = rel_path(tool_input)
    if SECRET_FIXTURE_PATHS.search(path):
        respond("ask", f"Secret-shaped string in test fixture {path}. Confirm it is a FAKE value "
                       "(this repository is public).")
    respond("deny", "BLOCKED: secret-shaped string in the content being written (AWS/GitHub/Docker "
                    "Hub/Slack/API key/age/private key). PostelMD is a PUBLIC repository: reference "
                    "an env var or *_FILE path instead. Patterns: .claude/hooks/content.py.")


def check_public(tool_input):
    path = rel_path(tool_input)
    if path.endswith("public-guard.local.txt") or path.startswith(".claude/hooks/"):
        return
    reasons = public_reasons(written_text(tool_input), path)
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


# Outward-facing commands that always need the maintainer, wherever they appear. Pushes and
# the gh commands routine work uses are judged by outward.py and gh_rules.py.
GATED = [
    (GIT + r"tag\b(?!\s+(-l|--list)\b)\s+\S", "creating/changing git tags"),
    (GH + r"release\b", "GitHub releases"),
    (GH + r"repo\s+(create|edit|delete|rename|archive|fork)\b", "creating or changing repositories"),
    (GH + r"(secret|variable|ruleset|label|gist)\s+(?!list\b|view\b)\S", "repo secrets/variables/rulesets/labels or gists"),
    (GH + r"pr\s+(close|review|reopen)\b", "closing, reopening or reviewing pull requests"),
    (GH + r"issue\s+(reopen|delete|transfer)\b", "reopening, deleting or transferring issues"),
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
            respond("deny", SUBJECT)


def check_git(tool_input, cwd):
    cmd = normalize(tool_input.get("command") or "")  # `S=lit; ... $S`: judged as typed
    check_commit(cmd)  # denials first: `git commit -m x && git push` is still a bad commit
    found = check_message(cmd, cwd)
    if found:
        respond(found[0], f"COMMIT MESSAGE CHECK: {found[1]}" if found[0] == "ask" else found[1])
    for pattern, label in GATED:
        if re.search(pattern, cmd):
            respond("ask", f"OUTWARD-FACING: {label}. PostelMD is public; approve only if the "
                           "maintainer asked for exactly this in the conversation.")
    why = check_gitdir(cmd, cwd, git_out)
    if why:
        respond("ask", f"FOREIGN REPOSITORY: {why}.")
    found = check_outward(cmd, cwd, git_out)
    if found and found[0] == "deny":
        respond("deny", found[1])
    if found:
        respond("ask", f"NEEDS THE MAINTAINER: {found[1]}. Routine pushes and PR work pass only "
                       "as one simple command with allowlisted arguments (outward.py).")


def check_bash_writes(tool_input, cwd):
    targets = repo_write_targets(normalize(tool_input.get("command") or ""), cwd)
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
