#!/bin/sh
# The image upgrade smoke test (ADR-0030 follow-up 2, ADR-0043; the release gate needs it):
# 0.1 data, upgraded by the image under test (default postelmd:dev) to data format 2, then
# rolled back with the runbook's own blocks, as written. Two passes: /data as a host folder
# (with --user, as the README runs it), and as a Compose-style named volume (<project>_
# postelmd-data, the image's own user).
#   1. 0.1 (v0.1.0-rc.1, pinned by digest) starts on new data; setup; a note.
#   2. The image runs the first upgrade: the `first upgrade` log record, format 2, a vault
#      marker naming a vault UUID at format 2, a complete pre-format-2/. The password works
#      and the note reads back.
#   3. 0.1 refuses the upgraded data.
#   4. docs/operations/upgrading-to-format-2.md's "Rollback to 0.1" steps 1-3, extracted
#      from the runbook and run in sh, take it back to format 1 and start 0.1 again.
#   5. 0.1 accepts the password and reads the note.
#   6. (host folder) At format 2 again, `db rebuild` and `vault claim --yes` run in a
#      container, as the runbook says, and the image starts on their result.
# shellcheck disable=SC2016 # in_data's scripts expand $1 in the container
set -eu
image=${1:-postelmd:dev}
old=ghcr.io/joaoheytor/postelmd:0.1.0-rc.1@sha256:7ef281d3e9f6ba6b4fe3c7989a1dec098e91900d4fd3b587fdf06f450d3b15fd
# busybox 1.37, the runbook's helper, from Google's mirror of Docker Hub (no pull rate limit
# on shared runners), at Docker Hub's index digest (ADR-0015, clarification of 2026-11-18).
busybox=mirror.gcr.io/library/busybox@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec
name=postelmd-smoke-upgrade-$$
volume=pmsmoke$$_postelmd-data
port=${SMOKE_PORT:-18090}
base=http://127.0.0.1:$port
runbook=$(dirname "$0")/../docs/operations/upgrading-to-format-2.md
# shellcheck source=scripts/image-smoke-lib.sh
. "$(dirname "$0")/image-smoke-lib.sh"
work=$(smoke_workdir)
jar=$work/cookies

cleanup() {
	docker rm -f "$name" >/dev/null 2>&1 || true
	docker volume rm -f "$volume" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

docker pull -q "$busybox" >/dev/null
docker pull -q "$old" >/dev/null

# src is what /data mounts (a host folder or a volume), user who the server runs as.
# run_server IMAGE: the server on $src.
run_server() {
	docker rm -f "$name" >/dev/null 2>&1 || true
	docker run -d --name "$name" --stop-timeout 60 -p "127.0.0.1:$port:8080" --user "$user" \
		-v "$src:/data" "$1" >/dev/null
}

# in_data SCRIPT [sh ARGS...]: runs a busybox sh script, as $user, with $src at /data. The
# script is single-quoted: its $1 is the container's argument, not this shell's (SC2016 is
# off for the file for that).
in_data() {
	docker run --rm --user "$user" -v "$src:/data" "$busybox" sh -c "$@"
}

# rd PATH: a file under /data.
rd() { in_data 'cat "/data/$1"' sh "$1" 2>&1; }

state=.postelmd
marker=vaults/notes/.postelmd-tmp/.created-by-postelmd

# wait_old: 0.1 has no /readyz; it is healthy once it serves, so wait for any HTTP answer.
wait_old() {
	wait_healthy alive-only
	i=0
	while [ "$(curl -s -o /dev/null --max-time 5 -w '%{http_code}' "$base/api/v1/session")" = 000 ]; do
		i=$((i + 1))
		[ "$i" -le 60 ] || fail "0.1 is healthy but doesn't answer after 30 s more"
		sleep 0.5
	done
}

# login_and_read: the password logs in, and the note reads back.
login_and_read() {
	rm -f "$jar"
	resp=$(api POST /api/v1/session -H 'Content-Type: application/json' -d '{"password":"smoke test password"}')
	expect 204 "$resp" "$1: log in"
	resp=$(api GET /api/v1/notes/kept.md)
	expect 200 "$resp" "$1: read kept.md"
}

# block N: the Nth sh block of the runbook's "Rollback to 0.1" section, as written.
block() {
	awk -v n="$1" '/^## Rollback to 0\.1$/ { s = 1 } /^### / { s = 0 }
		s && /^```sh$/ { b = 1; i++; next } b && /^```$/ { b = 0; next } b && i == n { print }' "$runbook"
}

# upgrade_and_roll_back WHAT: steps 1 to 5 on $src.
upgrade_and_roll_back() {
	echo "$1: 1. 0.1 on new data"
	run_server "$old"
	wait_old
	setup_from_logs
	docker stop "$name" >/dev/null
	in_data 'printf "# Kept\n\nWritten under 0.1.\n" >/data/vaults/notes/kept.md'

	echo "$1: 2. $image runs the first upgrade"
	run_server "$image"
	wait_healthy
	docker logs "$name" 2>&1 | grep -q '"msg":"first upgrade"' || fail "no first upgrade record in the logs"
	[ "$(rd "$state/format")" = 2 ] || fail "format is $(rd "$state/format"), want 2"
	m=$(rd "$marker")
	uuid=$(printf '%s\n' "$m" | sed -n 2p)
	printf '%s\n' "$uuid" | grep -Eqx '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}' ||
		fail "the vault marker names no vault UUID: $m"
	[ "$(printf '%s\n' "$m" | sed -n 3p)" = 2 ] || fail "the vault marker isn't at format 2: $m"
	rd "$state/pre-format-2/complete" | grep -Eqx "$uuid [0-9].*" ||
		fail "pre-format-2/ isn't complete: $(rd "$state/pre-format-2/complete")"
	login_and_read "$1, after the upgrade"

	echo "$1: 3. 0.1 refuses format 2"
	docker stop "$name" >/dev/null
	run_server "$old"
	i=0
	until docker logs "$name" 2>&1 | grep -q 'newer PostelMD (format 2)'; do
		i=$((i + 1))
		[ "$i" -le 60 ] || fail "0.1 didn't refuse the upgraded data"
		sleep 0.5
	done

	echo "$1: 4. the runbook's rollback, as written"
	run_server "$image"
	wait_healthy
	# As written, but for the container's name and busybox:1.37, pinned here by its digest.
	{
		printf 'PREV=%s\n' "$old"
		block 1 | sed "s/^c=postelmd\$/c=$name/"
		block 2
		block 4
	} | sed "s#busybox:1\.37#$busybox#g" >"$work/rollback.sh"
	grep -q "^c=$name\$" "$work/rollback.sh" || fail "the runbook's step 1 doesn't start with c=postelmd"
	grep -q "$busybox" "$work/rollback.sh" || fail "the runbook's step 2 doesn't use busybox:1.37"
	HOME=$work sh "$work/rollback.sh" >"$work/rollback.out" 2>&1 || true
	grep -q 'rolled back to format 1' "$work/rollback.out" || fail "the rollback didn't finish: $(cat "$work/rollback.out")"
	! grep -q 'STOPPED' "$work/rollback.out" || fail "the rollback stopped: $(cat "$work/rollback.out")"
	[ "$(rd "$state/format")" = 1 ] || fail "format is $(rd "$state/format") after the rollback, want 1"
	in_data '[ -f "/data/$1" ] && [ ! -s "/data/$1" ]' sh "$marker" || fail "the vault marker isn't empty after the rollback"

	echo "$1: 5. 0.1 starts again"
	[ "$(docker inspect -f '{{.Config.Image}}' "$name")" = "$old" ] || fail "step 3 didn't start 0.1"
	wait_old
	login_and_read "$1, 0.1 after the rollback"
	docker rm -f "$name" >/dev/null
}

# The host folder, with --user, as the README runs it.
src=$work/data
user="$(id -u):$(id -g)"
mkdir -p "$src"
upgrade_and_roll_back "host folder"

echo "host folder: 6. db rebuild and vault claim in a container, at format 2"
run_server "$image"
wait_healthy
docker stop "$name" >/dev/null
docker rm "$name" >/dev/null
out=$(docker run --rm --user "$user" -v "$src:/data" "$image" db rebuild 2>&1) || fail "db rebuild: $out"
printf '%s\n' "$out" | grep -q 'Made a new database' || fail "db rebuild said: $out"
in_data ': >"/data/$1"' sh "$marker"
out=$(docker run --rm --user "$user" -v "$src:/data" "$image" vault claim --yes 2>&1) || fail "vault claim: $out"
[ "$(rd "$marker" | sed -n 2p)" != "" ] || fail "vault claim wrote no vault UUID: $out"
run_server "$image"
wait_healthy
login_and_read "after db rebuild and vault claim"
docker rm -f "$name" >/dev/null

# A Compose-style named volume, as the image's own user (no --user in the README's Compose).
src=$volume
user=65532:65532
docker volume create "$src" >/dev/null
upgrade_and_roll_back "named volume"
echo "OK: the upgrade and the runbook's rollback (a host folder and a named volume), and db rebuild and vault claim at format 2"
