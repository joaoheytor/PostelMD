#!/bin/sh
# Tests scripts/verify-release-tag.sh against throwaway repositories and SSH keys.
set -eu
script="$(cd "$(dirname "$0")" && pwd)/verify-release-tag.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
failures=0

ssh-keygen -q -t ed25519 -N '' -C maintainer -f "$work/trusted"
ssh-keygen -q -t ed25519 -N '' -C stranger -f "$work/untrusted"

g() { git -C "$work/repo" "$@"; }
git init -q -b main "$work/origin.git" --bare
git init -q -b main "$work/repo"
g config user.name Test
g config user.email test@example.invalid
g config gpg.format ssh
g config commit.gpgsign false
g config tag.gpgsign false
g config core.autocrlf false
g remote add origin "$work/origin.git"
mkdir -p "$work/repo/.github"
echo "maintainer $(cat "$work/trusted.pub")" >"$work/repo/.github/allowed_signers"
mkdir -p "$work/repo/internal/store/layout" "$work/repo/cmd/postelmd"
printf 'package layout\n\nconst FormatVersion = 1\n' >"$work/repo/internal/store/layout/format_linux.go"
printf 'package main\n\nvar buildFormat = layout.FormatVersion\n' >"$work/repo/cmd/postelmd/instance.go"
g add .github internal cmd && g commit -q -m "add signers" && g push -q origin main
g fetch -q origin

sign() { g -c user.signingkey="$work/$1.pub" tag -s -m "$2" "$2" "${3:-HEAD}"; }

expect() { # expect pass|fail <tag> <description> [text the error must contain]
  (cd "$work/repo" && "$script" "$2" >/dev/null 2>"$work/err") && got=pass || got=fail
  if [ "$got" = "$1" ] && { [ -z "${4:-}" ] || grep -qF -- "$4" "$work/err"; }; then echo "ok   $3"; else
    echo "FAIL $3: expected $1, got $got: $(cat "$work/err")"; failures=$((failures + 1)); fi
}

sign trusted v0.1.0
expect pass v0.1.0 "signed by a trusted key, on main"
sign trusted v0.1.1-rc.1
expect pass v0.1.1-rc.1 "release candidate"
g tag v0.2.0
expect fail v0.2.0 "lightweight tag"
g tag -a -m unsigned v0.2.1
expect fail v0.2.1 "annotated but unsigned"
sign untrusted v0.2.2
expect fail v0.2.2 "signed by a key not in allowed_signers"
for bad in v0.3 v01.0.0 0.3.0 v0.3.0-beta.1 v0.3.0-rc.01 "v0.3.0;x"; do
  g -c user.signingkey="$work/trusted.pub" tag -s -m "$bad" "$bad" 2>/dev/null || continue
  expect fail "$bad" "bad name $bad"
done
g switch -q -c feature && g commit -q --allow-empty -m "not on main"
sign trusted v0.4.0
expect fail v0.4.0 "tagged commit not on main"
g switch -q main
g tag -d v0.4.0 >/dev/null

# A tag can't vouch for itself: a key added only on a branch is ignored.
g switch -q -c rogue
echo "rogue $(cat "$work/untrusted.pub")" >>"$work/repo/.github/allowed_signers"
g commit -q -am "trust a rogue key"
sign untrusted v0.5.0
expect fail v0.5.0 "key trusted only on a branch"
g switch -q main

# Release blocker (ADR-0030, #174): migration 0002 runs against existing databases, so no
# tag, release candidates included, on a commit that has it without `postelmd db rollback`.
mkdir -p "$work/repo/internal/store/sql/migrations" "$work/repo/cmd/postelmd"
echo "-- identity" >"$work/repo/internal/store/sql/migrations/0002_file_identity.sql"
echo "// postelmd db rollback arrives with #174 (a comment doesn't count)" >"$work/repo/cmd/postelmd/main.go"
g add internal cmd && g commit -q -m "migration 0002" && g push -q origin main && g fetch -q origin
sign trusted v0.6.0-rc.1
expect fail v0.6.0-rc.1 "migration 0002 without db rollback (release candidate)"
sign trusted v0.6.0
expect fail v0.6.0 "migration 0002 without db rollback (release)"
printf 'Commands:\n  db rollback      Undo the last migration\n' >"$work/repo/cmd/postelmd/main.go"
g commit -q -am "db rollback" && g push -q origin main && g fetch -q origin
sign trusted v0.6.0-rc.2
expect pass v0.6.0-rc.2 "migration 0002 with db rollback"
# This repository's own usage text passes the gate (#174 added its db rollback line).
cp "$(dirname "$script")/../cmd/postelmd/main.go" "$work/repo/cmd/postelmd/main.go"
g commit -q -am "this repository's main.go" && g push -q origin main && g fetch -q origin
sign trusted v0.6.0-rc.3
expect pass v0.6.0-rc.3 "migration 0002 with this repository's cmd/postelmd/main.go"

# Release blocker (ADR-0030 follow-up 2, ADR-0042, #190 R6): a commit whose build writes data
# format 2 or later ships only with the way back and the commands its refusals name. The
# fixtures fake FormatVersion; each piece is removed in turn, then all are present.
commit_main() { g add -A && g commit -q --allow-empty -m "$1" && g push -q origin main && g fetch -q origin; }
r=$work/repo
mkdir -p "$r/internal/store/layout" "$r/docs/operations" "$r/.github/workflows"
pieces() { # writes every piece the format-2 gate asks for
  printf 'package layout\n\nconst FormatVersion = %s\n' "$1" >"$r/internal/store/layout/format_linux.go"
  printf 'Commands:\n  db rollback      Undo\n  db rebuild       Rebuild\n  vault claim      Claim\n' >"$r/cmd/postelmd/main.go"
  printf 'package main\n\nvar buildFormat = layout.FormatVersion\n' >"$r/cmd/postelmd/instance.go"
  switch 2
  printf 'package sql\n\nvar ErrDamaged = errors.New("sql: damaged. With the server stopped, run " +\n\t"postelmd db rebuild: it moves app.db")\n' >"$r/internal/store/sql/conn.go"
  printf '# Upgrading\n\n## Rollback to 0.1\n\nSteps.\n' >"$r/docs/operations/upgrading-to-format-2.md"
  printf 'ci: lint test image-smoke image-smoke-upgrade image-scan\n\nimage-smoke-upgrade: image\n\tscripts/x.sh\n' >"$r/Makefile"
  workflow image "run: make image-smoke-upgrade"
}
switch() { # switch <N>: db rebuild and vault claim on from format N, as #193 left them
  printf 'package main\n\nfunc identityCommandsReady() bool { return buildFormat >= %s }\n\nfunc run() error {\n\tif !identityCommandsReady() {\n\t\treturn errBeforeFormat2\n\t}\n\treturn nil\n}\n' "$1" >"$r/cmd/postelmd/rebuild_linux.go"
}
workflow() { # workflow <job> <step line>: ci.yaml with that step in that job; required needs lint and image
  printf 'jobs:\n  lint:\n    steps:\n      - run: make lint\n  %s:\n    steps:\n      - uses: actions/checkout@0000\n      - %s\n  required:\n    if: always()\n    needs: [lint, image]\n    steps:\n      - run: "true"\n' "$1" "$2" >"$r/.github/workflows/ci.yaml"
}
case_() { # case_ pass|fail <description> [error text]: commits the fixture and checks a new tag
  commit_main "$2"
  n=$((n + 1))
  sign trusted "v0.8.0-rc.$n"
  expect "$1" "v0.8.0-rc.$n" "$2" "${3:-}"
}
pieces 1
: >"$r/Makefile"
: >"$r/docs/operations/upgrading-to-format-2.md"
commit_main "format 1 without the format-2 pieces"
sign trusted v0.7.0-rc.1
expect pass v0.7.0-rc.1 "format 1 needs none of the format-2 pieces"
n=1
for case in "docs/operations/upgrading-to-format-2.md|## Rollback to 0.1|rollback section" \
  "Makefile|image-smoke-upgrade:|upgrade smoke test" \
  "Makefile|lint test image-smoke image-smoke-upgrade|is not in make ci" \
  ".github/workflows/ci.yaml|make image-smoke-upgrade|CI doesn't run" \
  "cmd/postelmd/main.go|  db rebuild|db rebuild" \
  "cmd/postelmd/main.go|  vault claim|vault claim" \
  "cmd/postelmd/rebuild_linux.go|buildFormat >= 2|db rebuild and vault claim are off" \
  "internal/store/sql/conn.go|postelmd db rebuild|ErrDamaged"; do
  file=${case%%|*} rest=${case#*|}
  drop=${rest%%|*} why=${rest#*|}
  pieces 2
  grep -vF -- "$drop" "$r/$file" >"$work/cut" || true
  cp "$work/cut" "$r/$file"
  commit_main "format 2 without $drop"
  n=$((n + 1))
  sign trusted "v0.7.0-rc.$n"
  expect fail "v0.7.0-rc.$n" "format 2 without '$drop' in $file" "$why"
done
pieces 2
switch 3
commit_main "identity commands gated above the build's format"
sign trusted v0.7.0-rc.20
expect fail v0.7.0-rc.20 "format 2 with the identity commands on only from format 3" "are off"
pieces 2
commit_main "every format-2 piece"
sign trusted v0.7.0-rc.21
expect pass v0.7.0-rc.21 "format 2 with every piece"
# ErrDamaged's own text is what every caller shows: format 1's manual move would lose the IDs.
pieces 2
printf 'package sql\n\n// the old text: run postelmd db rebuild later\nvar ErrDamaged = errors.New("sql: damaged. Move app.db into a new folder; " +\n\t"nothing is lost")\n' \
  >"$r/internal/store/sql/conn.go"
commit_main "format 2 with ErrDamaged still giving format 1's fix"
sign trusted v0.7.0-rc.26
expect fail v0.7.0-rc.26 "format 2 with ErrDamaged still giving format 1's fix" "ErrDamaged"
pieces 12
commit_main "format 12"
sign trusted v0.7.0-rc.22
expect pass v0.7.0-rc.22 "format 12 (two digits) with every piece"
rm "$r/docs/operations/upgrading-to-format-2.md"
commit_main "format 12 without the runbook"
sign trusted v0.7.0-rc.23
expect fail v0.7.0-rc.23 "format 12 without the runbook" "rollback section"

# G1: every way to spell FormatVersion either reads as its value or fails the tag; an
# unreadable one never means format 1. Each spelling of 2 below has no runbook, so it fails.
n=0
fv=$r/internal/store/layout/format_linux.go
for spelling in 'const FormatVersion = 2 // file IDs' 'const FormatVersion int = 2' \
  'const (\n\tFormatVersion = 2\n\tMaxFormat = 2\n)' 'const FormatVersion = 2\r' 'var FormatVersion = 2'; do
  pieces 2
  rm "$r/docs/operations/upgrading-to-format-2.md"
  printf '%b\n' "package layout\n\n$spelling" >"$fv"
  case_ fail "FormatVersion spelled '$spelling' is read as 2" "writes data format 2"
done
for spelling in 'const FormatVersion = 1 + 1' 'const FormatVersion = 2\nconst FormatVersion = 1' \
  'const FormatVersion = two' 'const FormatVersion = layout.Other'; do
  pieces 1
  printf '%b\n' "package layout\n\n$spelling" >"$fv"
  case_ fail "FormatVersion spelled '$spelling' fails" "can't read FormatVersion"
done
pieces 1
rm "$fv"
case_ fail "format_linux.go moved away" "can't read FormatVersion"
pieces 1
printf 'package layout\n\nconst FormatVersion = 1 // v0.1\n' >"$fv"
case_ pass "FormatVersion = 1 with a comment needs no format-2 piece"

# G2: the format the build writes is buildFormat; raised alone, it is gated too.
pieces 1
rm "$r/docs/operations/upgrading-to-format-2.md"
printf 'package main\n\nvar buildFormat = 2\n' >"$r/cmd/postelmd/instance.go"
case_ fail "buildFormat raised to 2 with FormatVersion at 1" "writes data format 2"
pieces 1
printf 'package main\n\nvar buildFormat = layout.MaxFormat\n' >"$r/cmd/postelmd/instance.go"
case_ fail "buildFormat set to something else than a number or FormatVersion" "can't read buildFormat"
pieces 1
printf 'package main\n\nvar buildFormat = 1\n' >"$r/cmd/postelmd/instance.go"
case_ pass "buildFormat = 1 with FormatVersion at 1"
pieces 2
printf 'package main\n\nvar buildFormat = 1\n' >"$r/cmd/postelmd/instance.go"
case_ pass "buildFormat lower than FormatVersion: the larger one gates"

# G3: the switch on db rebuild and vault claim may go (part D removes it), or stay on.
pieces 2
printf 'package main\n\nfunc run() error { return rebuild() }\n' >"$r/cmd/postelmd/rebuild_linux.go"
case_ pass "format 2 with the switch removed"
pieces 2
printf 'package main\n\nfunc run() error {\n\treturn errBeforeFormat2\n}\n' >"$r/cmd/postelmd/rebuild_linux.go"
case_ fail "format 2 still returning errBeforeFormat2, with no switch" "are off"
pieces 2
printf 'package main\n\nfunc run() error { return rebuild() }\n' >"$r/cmd/postelmd/rebuild_linux.go"
printf 'package main\n\nfunc claim() error {\n\treturn errBeforeFormat2\n}\n' >"$r/cmd/postelmd/claim_linux.go"
case_ fail "format 2 with the switch gone but another file still refusing" "are off"
rm "$r/cmd/postelmd/claim_linux.go"
printf 'package main\n\nfunc claim() error {\n\treturn errBeforeFormat2\n}\n' >"$r/cmd/postelmd/claim_linux_test.go"
pieces 2
printf 'package main\n\nfunc run() error { return rebuild() }\n' >"$r/cmd/postelmd/rebuild_linux.go"
case_ pass "format 2: a test file mentioning errBeforeFormat2 doesn't count"
rm "$r/cmd/postelmd/claim_linux_test.go"
# Any use of the identifier counts, not only `return errBeforeFormat2`: D deletes it, or keeps
# the switch line the threshold check reads.
for use in 'return nil, errBeforeFormat2' 'return fmt.Errorf("claim: %w", errBeforeFormat2)' \
  'var errBeforeFormat2 = errors.New("later")'; do
  pieces 2
  printf 'package main\n\nfunc run() error { return rebuild() }\n\nfunc claim() (any, error) {\n\t%s\n}\n' "$use" \
    >"$r/cmd/postelmd/rebuild_linux.go"
  case_ fail "format 2 with no switch, and '$use'" "are off"
done
pieces 2
printf 'package main\n\nfunc run() error { return rebuild() }\n\nfunc g() error { return errBeforeFormat2x }\n' \
  >"$r/cmd/postelmd/rebuild_linux.go"
case_ pass "format 2: a longer identifier isn't errBeforeFormat2"

# G5: intent recovery's placeholder for the kinds without recovery yet (noRecoveryYet) blocks a
# format-2 release while any non-test file names it; a test file or a longer name doesn't.
mkdir -p "$r/internal/intents"
pieces 2
printf 'package intents\n\nfunc noRecoveryYet(kind string) bool { return kind != "save" }\n' >"$r/internal/intents/recover.go"
case_ fail "format 2 with noRecoveryYet" "intent recovery is unfinished"
pieces 2
printf 'package intents\n\nfunc f() bool { return noRecoveryYet("move") }\n' >"$r/internal/intents/recover.go"
case_ fail "format 2 with a call to noRecoveryYet" "intent recovery is unfinished"
pieces 2
printf 'package intents\n\nfunc recoverAll() {}\n' >"$r/internal/intents/recover.go"
printf 'package intents\n\n// noRecoveryYet is gone\n' >"$r/internal/intents/recover_test.go"
printf 'package intents\n\nfunc noRecoveryYetx() {}\n' >"$r/internal/intents/other.go"
case_ pass "format 2: noRecoveryYet gone, named only in a test file or as a longer name"
rm -r "$r/internal/intents"
pieces 1
mkdir -p "$r/internal/intents"
printf 'package intents\n\nfunc noRecoveryYet(kind string) bool { return true }\n' >"$r/internal/intents/recover.go"
case_ pass "format 1: noRecoveryYet doesn't block it"
rm -r "$r/internal/intents"

# buildFormat is assigned only where it is declared: a later assignment in any non-test file
# under cmd/ (an init, say) would change the format the gate didn't read.
pieces 1
printf 'package main\n\nfunc init() { buildFormat = layout.MaxFormat }\n' >"$r/cmd/postelmd/format_init.go"
case_ fail "buildFormat assigned again in another file" "buildFormat"
pieces 1
printf 'package main\n\nvar buildFormat = layout.FormatVersion\n\nfunc init() {\n\tbuildFormat = 2\n}\n' \
  >"$r/cmd/postelmd/instance.go"
rm "$r/cmd/postelmd/format_init.go"
case_ fail "buildFormat assigned again in instance.go" "buildFormat"
pieces 1
printf 'package main\n\nfunc TestX() { buildFormat = 2 }\n' >"$r/cmd/postelmd/format_test.go"
printf 'package main\n\nfunc f() bool { return buildFormat == 2 || buildFormat >= 3 }\n' >"$r/cmd/postelmd/use.go"
case_ pass "buildFormat set in a test, or compared, doesn't count"
rm "$r/cmd/postelmd/format_test.go" "$r/cmd/postelmd/use.go"

# G4: CI must run the target in a job the required check needs.
for step in "run: make image-multiarch image-smoke-upgrade" "run: make image-smoke-upgrade image-scan"; do
  pieces 2
  workflow image "$step"
  case_ pass "CI step '$step'"
done
for step in "run: make image-smoke-upgrade-x" "# run: make image-smoke-upgrade" "run: echo make image-smoke-upgrade"; do
  pieces 2
  workflow image "$step"
  case_ fail "CI step '$step'" "CI doesn't run"
done
pieces 2
workflow optional "run: make image-smoke-upgrade"
case_ fail "CI runs it in a job the required check doesn't need" "CI doesn't run"
# This repository's own files, at format 2: the gate passes them (the release's own dry run),
# and refuses them without the upgrade smoke test in make ci.
root=$(dirname "$script")/..
for f in internal/store/layout/format_linux.go cmd/postelmd/main.go cmd/postelmd/rebuild_linux.go cmd/postelmd/instance.go \
  cmd/postelmd/appdb_linux.go cmd/postelmd/claim_linux.go internal/store/sql/conn.go \
  docs/operations/upgrading-to-format-2.md Makefile .github/workflows/ci.yaml; do
  cp "$root/$f" "$r/$f"
done
grep -q '^const FormatVersion = 2$' "$r/internal/store/layout/format_linux.go" ||
  { echo "FAIL this repository isn't at FormatVersion 2"; failures=$((failures + 1)); }
commit_main "this repository's files at their own format"
sign trusted v0.7.0-rc.24
expect pass v0.7.0-rc.24 "this repository's files at FormatVersion 2"
sed 's/^\(ci:.*\) image-smoke-upgrade /\1 /' "$root/Makefile" >"$r/Makefile"
{ grep -q '^ci: lint ' "$r/Makefile" && ! grep -Eq '^ci:.* image-smoke-upgrade( |$)' "$r/Makefile"; } ||
  { echo "FAIL the fixture couldn't drop image-smoke-upgrade from make ci"; failures=$((failures + 1)); }
commit_main "this repository's files without the upgrade smoke test in make ci"
sign trusted v0.7.0-rc.25
expect fail v0.7.0-rc.25 "this repository's files without the upgrade smoke test in make ci" "upgrade smoke test"

# Revocation: removing the key on main makes its existing tags fail.
: >"$work/repo/.github/allowed_signers"
g commit -q -am "revoke" && g push -q origin main && g fetch -q origin
expect fail v0.1.0 "key revoked on main"

[ "$failures" -eq 0 ] && echo "all verify-release-tag tests passed" || exit 1
