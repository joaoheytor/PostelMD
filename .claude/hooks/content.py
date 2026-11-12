"""What the guard looks for in text bound for the public repository (postelmd_guard.py).

Used on Write/Edit content (secrets and public modes) and on PR and issue text
(gh_rules.py): secret formats deny; private-looking content asks.
"""
import os
import re

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
ALLOWED_EMAILS = re.compile(
    r"^(jhkpereira@gmail\.com|noreply@anthropic\.com|.+@example\.(com|org|net)|you@example\.com)$",
    re.I,
)
# Split literals: the pre-commit hook's home-path check must not match this source line.
HOME_PATH = re.compile(r"(/" r"home/[a-z][a-z0-9_-]*/|/" r"Users/[^/\s]+/)")
PRIVATE_IP = re.compile(
    r"\b(10\.\d{1,3}\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3}"
    r"|172\.(1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3}|100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7])\.\d{1,3}\.\d{1,3})\b"
)
# Bounded quantifiers keep this linear on large inputs (an unbounded local part is quadratic).
EMAIL = re.compile(r"[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9-]{1,63}(?:\.[A-Za-z0-9-]{1,63}){1,8}")
TEST_PATHS = re.compile(r"(^|/)(test|testdata|tests)/|_test\.go$|\.test\.ts$")


def has_secret(text):
    return any(re.search(p, text) for p in SECRET_PATTERNS)


def local_denylist():
    root = os.environ.get("CLAUDE_PROJECT_DIR", ".")
    path = os.path.join(root, ".claude", "public-guard.local.txt")
    try:
        with open(path, encoding="utf-8") as fh:
            return [ln.strip() for ln in fh if ln.strip() and not ln.startswith("#")]
    except OSError:
        return []


def public_reasons(text, path=""):
    """Why text should rarely be public: home paths, private IPs (outside test paths),
    unknown email addresses, local denylist terms. Empty when none."""
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
    if any(t.lower() in lowered for t in local_denylist()):
        reasons.append("term(s) from the local public-guard denylist")
    return reasons
