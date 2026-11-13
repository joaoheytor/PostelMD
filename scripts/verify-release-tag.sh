#!/bin/sh
# Verifies a release tag before any release workflow uses it (docs/ci-cd.md, issue #32).
# GitHub rulesets restrict who may create v* tags but can't require a signed tag object, so:
#   1. the name is SemVer: vX.Y.Z or vX.Y.Z-rc.N;
#   2. it is an annotated tag pointing to a commit (not a lightweight tag);
#   3. it carries a valid SSH signature from a key in .github/allowed_signers, read from
#      origin/main (so removing a key there revokes it, and a tag can't bring its own key);
#   4. the tagged commit is on origin/main;
#   5. release blockers: a commit with migration 0002 has `postelmd db rollback` (#174).
# Usage: scripts/verify-release-tag.sh <tag>. Needs full history and the tag object:
# actions/checkout can turn an annotated tag into a lightweight one, so workflows re-fetch
#   git fetch --force origin "refs/tags/$TAG:refs/tags/$TAG" refs/heads/main:refs/remotes/origin/main
set -eu

tag=${1:?usage: verify-release-tag.sh <tag>}
fail() { echo "verify-release-tag: $tag: $*" >&2; exit 1; }

num='(0|[1-9][0-9]*)'
printf '%s\n' "$tag" | grep -Eqx "v$num\\.$num\\.$num(-rc\\.$num)?" ||
  fail "not a release tag name (vX.Y.Z or vX.Y.Z-rc.N)"

[ "$(git cat-file -t "refs/tags/$tag" 2>/dev/null)" = tag ] ||
  fail "not an annotated tag (lightweight or missing; was the tag object fetched?)"
[ "$(git cat-file -t "refs/tags/$tag^{}")" = commit ] || fail "does not point to a commit"

signers=$(mktemp)
trap 'rm -f "$signers"' EXIT
git show origin/main:.github/allowed_signers >"$signers" 2>/dev/null ||
  fail "origin/main has no .github/allowed_signers"
# git tag -v exits non-zero for a missing, bad or untrusted signature; also require that the
# key matched a principal, so a signature git merely "parsed" can never pass.
reason() { printf '%s\n' "$out" | grep -iE 'signature|principal|error' | tail -n 1; }
out=$(git -c gpg.format=ssh -c gpg.ssh.allowedSignersFile="$signers" tag -v "$tag" 2>&1) ||
  fail "signature check failed: $(reason)"
printf '%s\n' "$out" | grep -q '^Good "git" signature for ' ||
  fail "no trusted signature: $(reason)"

git merge-base --is-ancestor "refs/tags/$tag^{commit}" origin/main ||
  fail "tagged commit is not on main"

# Release blocker (ADR-0030, #174): migration 0002 is the first to run against existing
# databases, and a release that ships it must also ship `postelmd db rollback`. That command
# is recognized by its line in the usage text of cmd/postelmd/main.go ("  db rollback  ...").
commit=$(git rev-parse "refs/tags/$tag^{commit}")
if git ls-tree -r --name-only "$commit" -- internal/store/sql/migrations/ | grep -q '/0002_'; then
  git show "$commit:cmd/postelmd/main.go" 2>/dev/null | grep -Eq '^ +db rollback +[A-Za-z]' ||
    fail "has migration 0002 but no \`postelmd db rollback\` (#174): no release, RC included, until it is on main"
fi

echo "verify-release-tag: $tag OK ($(printf '%s\n' "$out" | grep '^Good' | head -n 1))"
