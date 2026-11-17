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
g remote add origin "$work/origin.git"
mkdir -p "$work/repo/.github"
echo "maintainer $(cat "$work/trusted.pub")" >"$work/repo/.github/allowed_signers"
g add .github && g commit -q -m "add signers" && g push -q origin main
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
commit_main() { g add -A && g commit -q -m "$1" && g push -q origin main && g fetch -q origin; }
r=$work/repo
mkdir -p "$r/internal/store/layout" "$r/docs/operations" "$r/.github/workflows"
pieces() { # writes every piece the format-2 gate asks for
  printf 'package layout\n\nconst FormatVersion = %s\n' "$1" >"$r/internal/store/layout/format_linux.go"
  printf 'Commands:\n  db rollback      Undo\n  db rebuild       Rebuild\n  vault claim      Claim\n' >"$r/cmd/postelmd/main.go"
  printf 'package main\n\nfunc identityCommandsReady() bool { return buildFormat >= 2 }\n' >"$r/cmd/postelmd/rebuild_linux.go"
  printf 'package main\n\nfunc f() { return nil, sqlstore.NameRebuild(err) }\n' >"$r/cmd/postelmd/appdb_linux.go"
  printf 'package sql\n\nconst errRebuildFix = "run postelmd db rebuild: it moves app.db"\n' >"$r/internal/store/sql/conn.go"
  printf '# Upgrading\n\n## Rollback to 0.1\n\nSteps.\n' >"$r/docs/operations/upgrading-to-format-2.md"
  printf 'ci: lint test image-smoke image-smoke-upgrade image-scan\n\nimage-smoke-upgrade: image\n\tscripts/x.sh\n' >"$r/Makefile"
  printf 'jobs:\n  image:\n    steps:\n      - run: make image-smoke-upgrade\n' >"$r/.github/workflows/ci.yaml"
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
  "internal/store/sql/conn.go|postelmd db rebuild|ErrDamaged" \
  "cmd/postelmd/appdb_linux.go|NameRebuild(|ErrDamaged"; do
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
printf 'package main\n\nfunc identityCommandsReady() bool { return buildFormat >= 3 }\n' >"$r/cmd/postelmd/rebuild_linux.go"
commit_main "identity commands gated above the build's format"
sign trusted v0.7.0-rc.20
expect fail v0.7.0-rc.20 "format 2 with the identity commands on only from format 3" "are off"
pieces 2
commit_main "every format-2 piece"
sign trusted v0.7.0-rc.21
expect pass v0.7.0-rc.21 "format 2 with every piece"
pieces 12
commit_main "format 12"
sign trusted v0.7.0-rc.22
expect pass v0.7.0-rc.22 "format 12 (two digits) with every piece"
rm "$r/docs/operations/upgrading-to-format-2.md"
commit_main "format 12 without the runbook"
sign trusted v0.7.0-rc.23
expect fail v0.7.0-rc.23 "format 12 without the runbook" "rollback section"
# This repository's own files, with FormatVersion faked to 2: until the upgrade smoke test
# lands (part D), the gate refuses them.
root=$(dirname "$script")/..
for f in internal/store/layout/format_linux.go cmd/postelmd/main.go cmd/postelmd/rebuild_linux.go \
  cmd/postelmd/appdb_linux.go internal/store/sql/conn.go docs/operations/upgrading-to-format-2.md \
  Makefile .github/workflows/ci.yaml; do
  cp "$root/$f" "$r/$f"
done
commit_main "this repository's files at their own format"
sign trusted v0.7.0-rc.24
expect pass v0.7.0-rc.24 "this repository's files at FormatVersion 1"
sed 's/^const FormatVersion = 1$/const FormatVersion = 2/' "$root/internal/store/layout/format_linux.go" \
  >"$r/internal/store/layout/format_linux.go"
grep -q '^const FormatVersion = 2$' "$r/internal/store/layout/format_linux.go" ||
  { echo "FAIL the fixture couldn't fake FormatVersion 2"; failures=$((failures + 1)); }
commit_main "this repository's files with FormatVersion faked to 2"
sign trusted v0.7.0-rc.25
expect fail v0.7.0-rc.25 "this repository's files with FormatVersion faked to 2" "upgrade smoke test"

# Revocation: removing the key on main makes its existing tags fail.
: >"$work/repo/.github/allowed_signers"
g commit -q -am "revoke" && g push -q origin main && g fetch -q origin
expect fail v0.1.0 "key revoked on main"

[ "$failures" -eq 0 ] && echo "all verify-release-tag tests passed" || exit 1
