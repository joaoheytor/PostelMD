#!/bin/sh
# Tests the Accepted-ADR check of .githooks/pre-commit against a throwaway repository.
set -eu
hook="$(cd "$(dirname "$0")/.." && pwd)/.githooks/pre-commit"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM
failures=0

g() { git -C "$work/repo" "$@"; }
git init -q -b main "$work/repo"
g config user.name Test
g config user.email test@example.invalid
g config commit.gpgsign false
mkdir -p "$work/repo/docs/adr"

adr() { # adr <file> <status> <decision text> [extra trailing text]
  printf '# ADR\n\n- **Status:** %s\n\n## Context\nWhy.\n\n## Decision\n%s\n\n## Consequences\nSome.\n%s' \
    "$2" "$3" "${4:-}" >"$work/repo/docs/adr/$1"
}

expect() { # expect pass|fail <description>; runs the hook on what is staged, then resets
  (cd "$work/repo" && "$hook" >/dev/null 2>"$work/err") && got=pass || got=fail
  if [ "$got" = "$1" ]; then echo "ok   $2"; else
    echo "FAIL $2: expected $1, got $got: $(cat "$work/err")"; failures=$((failures + 1)); fi
  g reset -q --hard
}

adr 0001-accepted.md Accepted "We use X."
adr 0002-proposed.md Proposed "We might use Y."
g add docs && g commit -q -m "add ADRs"

adr 0001-accepted.md Accepted "We use Z."
g add docs
expect fail "changing an Accepted ADR's Decision"

adr 0001-accepted.md Accepted "We use X." "
## Clarifications
- 2026-01-01: X means the stdlib one.
"
g add docs
expect pass "appending a Clarification to an Accepted ADR"

adr 0001-accepted.md "Superseded by ADR-0003" "We use X."
g add docs
expect pass "superseding an Accepted ADR (status line only)"

adr 0001-accepted.md Superseded "We use Z."
g add docs
expect fail "changing the Decision while flipping the status away from Accepted"

adr 0002-proposed.md Proposed "We use Y."
g add docs
expect pass "editing a Proposed ADR's Decision"

adr 0003-new.md Accepted "Brand new."
g add docs
expect pass "adding a new ADR"

# A branch: the ADRs above are on main; 0004 is written and Accepted on the branch only.
main=$(g rev-parse HEAD)
g switch -q -c feature
adr 0004-branch.md Accepted "First draft."
g add docs && g commit -q -m "add ADR 0004"

adr 0004-branch.md Accepted "Fixed draft."
g add docs
expect fail "without origin/main, an ADR Accepted at HEAD can't change (HEAD fallback)"
if grep -q "comparing Accepted ADRs with HEAD" "$work/err"; then echo "ok   the HEAD fallback says so"
else echo "FAIL the HEAD fallback doesn't say so: $(cat "$work/err")"; failures=$((failures + 1)); fi

g update-ref refs/remotes/origin/main "$main"
adr 0004-branch.md Accepted "Fixed draft."
g add docs
expect pass "an ADR new in this branch can be edited, though Accepted at HEAD"

adr 0001-accepted.md Accepted "We use Z."
g add docs
expect fail "an ADR Accepted on main still can't change on a branch"

[ "$failures" -eq 0 ] && echo "all pre-commit ADR tests passed" || exit 1
