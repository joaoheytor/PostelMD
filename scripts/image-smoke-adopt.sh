#!/bin/sh
# Adopting an existing notes folder (ADR-0020; roadmap v0.1) in the image (default
# postelmd:dev): a host folder bind-mounted at /data/vaults/notes, next to a separate data
# directory at /data, with --user as in the README.
#   A. a plain folder (no .git): setup imports its notes as the first commit; they are
#      listed and readable through the API, the folder becomes a git repository, and the
#      files on the host are unchanged.
#   B. a git repository with one commit: setup imports nothing; that commit is still the
#      note's history and the branch head, and the files on the host are unchanged.
set -eu
image=${1:-postelmd:dev}
name=postelmd-smoke-adopt-$$
port=${SMOKE_PORT:-18090}
base=http://127.0.0.1:$port
# shellcheck source=scripts/image-smoke-lib.sh
. "$(dirname "$0")/image-smoke-lib.sh"
work=$(smoke_workdir)
jar=$work/cookies

cleanup() {
	docker rm -f "$name" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

notes='README.md
ops/on-call.md
runbooks/db/failover.md'

# make_notes DIR: the three notes above, with nested folders.
make_notes() {
	mkdir -p "$1/ops" "$1/runbooks/db"
	printf '# Team notes\n\nSee ops/on-call.md.\n' >"$1/README.md"
	printf '# On call\n\n- page the primary\n' >"$1/ops/on-call.md"
	printf '# Database failover\n\n1. Promote the replica.\n' >"$1/runbooks/db/failover.md"
}

# checksums DIR: a checksum of every regular file outside .git and PostelMD's own entries.
checksums() {
	(cd "$1" && find . \( -name .git -o -name '.postelmd-*' \) -prune -o -type f -print | sort | xargs sha256sum)
}

# boot DIR TAG: a fresh data directory (data-TAG), DIR mounted as the notes vault, and
# setup completed.
boot() {
	docker rm -f "$name" >/dev/null 2>&1 || true
	rm -f "$jar"
	# Created here, or Docker creates the mount point as root and cleanup can't remove it.
	mkdir -p "$work/data-$2/vaults/notes"
	docker run -d --name "$name" --stop-timeout 60 -p "127.0.0.1:$port:8080" --user "$(id -u):$(id -g)" \
		-v "$work/data-$2:/data" -v "$1:/data/vaults/notes" "$image" >/dev/null
	wait_healthy
	setup_from_logs
}

# check_notes DIR: every note is listed and reads back as the host file in DIR.
check_notes() {
	resp=$(api GET /api/v1/files)
	expect 200 "$resp" "list the files"
	for n in $notes; do
		body "$resp" | grep -qF "\"$n\"" || fail "$n is not listed: $(body "$resp")"
		note=$(api GET "/api/v1/notes/$n")
		expect 200 "$note" "read $n"
		[ "$(body "$note")" = "$(cat "$1/$n")" ] || fail "$n reads back differently through the API"
	done
}

echo "A1. a plain folder with nested notes and no .git"
make_notes "$work/plain"
before=$(checksums "$work/plain")
boot "$work/plain" a
body "$checked" | grep -qF '"files":3' || fail "setup check didn't count the 3 notes: $(body "$checked")"
body "$setup_resp" | grep -qF '"imported":3' || fail "setup didn't import the 3 notes: $(body "$setup_resp")"

echo "A2. its notes are listed and readable"
check_notes "$work/plain"

echo "A3. it is now a git repository, with the notes as the initial import"
[ -d "$work/plain/.git" ] || fail "the adopted folder has no .git"
resp=$(api GET /api/v1/history/runbooks/db/failover.md)
expect 200 "$resp" "history of an adopted note"
[ "$(body "$resp" | jq '.versions | length')" = 1 ] || fail "want one version: $(body "$resp")"
[ "$(body "$resp" | jq -r '.versions[0].message')" = "Initial import" ] ||
	fail "the first version isn't the initial import: $(body "$resp")"

echo "A4. the files on the host are unchanged"
[ "$(checksums "$work/plain")" = "$before" ] || fail "adoption changed the files on the host"

echo "B1. a git repository with one commit"
make_notes "$work/repo"
git -C "$work/repo" init -q
git -C "$work/repo" config user.name Smoke
git -C "$work/repo" config user.email smoke@example.invalid
git -C "$work/repo" config commit.gpgsign false
git -C "$work/repo" add -A
git -C "$work/repo" -c core.hooksPath=/dev/null commit -q -m "Existing notes"
commit=$(git -C "$work/repo" rev-parse HEAD)
before=$(checksums "$work/repo")
boot "$work/repo" b
body "$setup_resp" | grep -qF '"imported":0' || fail "setup imported into a repository with commits: $(body "$setup_resp")"

echo "B2. its notes are listed and readable"
check_notes "$work/repo"

echo "B3. the existing commit is still the history, and nothing was rewritten"
resp=$(api GET /api/v1/history/ops/on-call.md)
expect 200 "$resp" "history of a note in an adopted repository"
[ "$(body "$resp" | jq -r '[.versions[].commit] | join(" ")')" = "$commit" ] ||
	fail "the history isn't the existing commit $commit: $(body "$resp")"
[ "$(git -C "$work/repo" rev-parse HEAD)" = "$commit" ] || fail "the branch head moved"
[ "$(git -C "$work/repo" rev-list --count HEAD)" = 1 ] || fail "the history was rewritten"
[ "$(checksums "$work/repo")" = "$before" ] || fail "adoption changed the files on the host"

echo "OK"
