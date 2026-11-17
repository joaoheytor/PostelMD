#!/bin/sh
# Verifies a release tag before any release workflow uses it (docs/ci-cd.md, issue #32).
# GitHub rulesets restrict who may create v* tags but can't require a signed tag object, so:
#   1. the name is SemVer: vX.Y.Z or vX.Y.Z-rc.N;
#   2. it is an annotated tag pointing to a commit (not a lightweight tag);
#   3. it carries a valid SSH signature from a key in .github/allowed_signers, read from
#      origin/main (so removing a key there revokes it, and a tag can't bring its own key);
#   4. the tagged commit is on origin/main;
#   5. release blockers: a commit with migration 0002 has `postelmd db rollback` (#174); one
#      that writes data format 2 or later has the rollback runbook, the upgrade smoke test in
#      make ci and CI, db rebuild and vault claim on, and ErrDamaged naming db rebuild (#190).
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

# Release blocker (ADR-0030 follow-up 2, ADR-0042, #190 R6): a build that writes data format 2
# or later changes data 0.1 can't read, so it ships only with the way back and the commands its
# refusals name. Each piece is recognized by text in the tagged commit's files. It fails closed:
# a format it can't read fails the tag, never counts as format 1.
at() { git show "$commit:$1" 2>/dev/null | tr -d '\r'; }
isnum() { case $1 in '' | *[!0-9]*) return 1 ;; esac; }
# decl <file> <name>: the one top-level `[const|var] <name> [int] = <value> [// comment]` line's
# value, or nothing when there are none, several, or another form.
decl() {
  lines=$(at "$1" | grep -E "^[[:space:]]*((const|var)[[:space:]]+)?$2([[:space:]]|=|\$)") || return 0
  [ "$(printf '%s\n' "$lines" | wc -l)" -eq 1 ] || return 0
  printf '%s\n' "$lines" | sed -n -E "s#^[[:space:]]*((const|var)[[:space:]]+)?$2([[:space:]]+int)?[[:space:]]*=[[:space:]]*([A-Za-z0-9_.]+)[[:space:]]*(//.*)?\$#\\4#p"
}
format=$(decl internal/store/layout/format_linux.go FormatVersion)
isnum "$format" ||
  fail "can't read FormatVersion in internal/store/layout/format_linux.go (one \`const FormatVersion = <n>\`), so the format-2 release blockers can't be checked"
build=$(decl cmd/postelmd/instance.go buildFormat)
case $build in
  layout.FormatVersion) ;;
  *) isnum "$build" ||
    fail "can't read buildFormat in cmd/postelmd/instance.go (\`var buildFormat = layout.FormatVersion\` or a number)"
    [ "$build" -le "$format" ] || format=$build ;;
esac
if [ "$format" -ge 2 ]; then
  f2() { fail "writes data format $format but $*: no release, RC included (docs/operations/releasing.md)"; }
  at docs/operations/upgrading-to-format-2.md | grep -qx '## Rollback to 0.1' ||
    f2 "docs/operations/upgrading-to-format-2.md has no rollback section (## Rollback to 0.1)"
  at Makefile | grep -q '^image-smoke-upgrade:' ||
    f2 "the Makefile has no upgrade smoke test (image-smoke-upgrade)"
  at Makefile | grep -Eq '^ci:.* image-smoke-upgrade( |$)' ||
    f2 "the upgrade smoke test (image-smoke-upgrade) is not in make ci"
  # The job that runs it must be one the required check needs (`needs: [...]` on one line).
  wf=$(at .github/workflows/ci.yaml)
  runs=$(printf '%s\n' "$wf" | awk '
    /^  [A-Za-z0-9_-]+:[[:space:]]*$/ { job = $1; sub(/:$/, "", job) }
    /^[[:space:]]+(- )?run: make( [a-z0-9-]+)* image-smoke-upgrade( |$)/ { print job }')
  needs=$(printf '%s\n' "$wf" | awk '
    /^  [A-Za-z0-9_-]+:/ { r = ($1 == "required:") }
    r && /^    needs: *\[/ { sub(/^    needs: *\[/, ""); sub(/\].*/, ""); gsub(/[ ,]+/, " "); print " " $0 " " }')
  ok=
  for j in $runs; do case $needs in *" $j "*) ok=1 ;; esac; done
  [ -n "$ok" ] ||
    f2 "CI doesn't run make image-smoke-upgrade in a job the required check needs (.github/workflows/ci.yaml)"
  usage=$(at cmd/postelmd/main.go)
  printf '%s\n' "$usage" | grep -Eq '^ +db rebuild +[A-Za-z]' || f2 "has no \`postelmd db rebuild\`"
  printf '%s\n' "$usage" | grep -Eq '^ +vault claim +[A-Za-z]' || f2 "has no \`postelmd vault claim\`"
  # Part D removes their switch (errBeforeFormat2); while some non-test file still returns it,
  # the switch must turn them on at this format.
  if git grep -q 'return errBeforeFormat2' "$commit" -- 'cmd/postelmd/*.go' ':!*_test.go'; then
    on=$(at cmd/postelmd/rebuild_linux.go |
      sed -n 's/^func identityCommandsReady() bool { return buildFormat >= \([0-9][0-9]*\) }$/\1/p')
    { isnum "$on" && [ "$on" -le "$format" ]; } ||
      f2 "db rebuild and vault claim are off (errBeforeFormat2, identityCommandsReady in cmd/postelmd/rebuild_linux.go)"
  fi
  damaged="ErrDamaged doesn't name db rebuild (errRebuildFix in internal/store/sql/conn.go, NameRebuild in cmd/postelmd/appdb_linux.go)"
  at internal/store/sql/conn.go | grep -q 'postelmd db rebuild' || f2 "$damaged"
  at cmd/postelmd/appdb_linux.go | grep -q 'NameRebuild(' || f2 "$damaged"
fi

echo "verify-release-tag: $tag OK ($(printf '%s\n' "$out" | grep '^Good' | head -n 1))"
