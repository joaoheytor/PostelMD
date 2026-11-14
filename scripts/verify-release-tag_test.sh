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

expect() { # expect pass|fail <tag> <description>
  (cd "$work/repo" && "$script" "$2" >/dev/null 2>"$work/err") && got=pass || got=fail
  if [ "$got" = "$1" ]; then echo "ok   $3"; else
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

# Revocation: removing the key on main makes its existing tags fail.
: >"$work/repo/.github/allowed_signers"
g commit -q -am "revoke" && g push -q origin main && g fetch -q origin
expect fail v0.1.0 "key revoked on main"

[ "$failures" -eq 0 ] && echo "all verify-release-tag tests passed" || exit 1
