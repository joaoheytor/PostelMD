#!/bin/sh
# The image upgrade smoke test (ADR-0030 follow-up 2, ADR-0043; the release gate needs it):
# 0.1 data, upgraded by the image under test (default postelmd:dev) to data format 2, then
# rolled back with the runbook's own blocks, as written.
#   1. 0.1 (v0.1.0-rc.1, pinned by digest) starts on a new data directory; setup; a note.
#   2. The image runs the first upgrade: the `first upgrade` log record, format 2, a vault
#      marker naming a vault UUID at format 2, a complete pre-format-2/. The password works
#      and the note reads back.
#   3. 0.1 refuses the upgraded data.
#   4. docs/operations/upgrading-to-format-2.md's "Rollback to 0.1" steps 1-3, extracted
#      from the runbook and run in sh, take it back to format 1 and start 0.1 again.
#   5. 0.1 accepts the password and reads the note.
#   6. At format 2 again, `db rebuild` and `vault claim --yes` run in a container, as the
#      runbook says, and the image starts on their result.
set -eu
image=${1:-postelmd:dev}
old=ghcr.io/joaoheytor/postelmd:0.1.0-rc.1@sha256:7ef281d3e9f6ba6b4fe3c7989a1dec098e91900d4fd3b587fdf06f450d3b15fd
busybox=busybox@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec
name=postelmd-smoke-upgrade-$$
port=${SMOKE_PORT:-18090}
base=http://127.0.0.1:$port
runbook=$(dirname "$0")/../docs/operations/upgrading-to-format-2.md
# shellcheck source=scripts/image-smoke-lib.sh
. "$(dirname "$0")/image-smoke-lib.sh"
work=$(smoke_workdir)
jar=$work/cookies
data=$work/data
user="$(id -u):$(id -g)"

cleanup() {
	docker rm -f "$name" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

docker pull -q "$busybox" >/dev/null
docker pull -q "$old" >/dev/null

# run_server IMAGE: the server on $data, as the README runs it.
run_server() {
	docker rm -f "$name" >/dev/null 2>&1 || true
	docker run -d --name "$name" --stop-timeout 60 -p "127.0.0.1:$port:8080" --user "$user" \
		-v "$data:/data" "$1" >/dev/null
}

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

state=$data/.postelmd
marker=$data/vaults/notes/.postelmd-tmp/.created-by-postelmd
mkdir -p "$data"

echo "1. 0.1 on a new data directory"
run_server "$old"
wait_old
setup_from_logs
docker stop "$name" >/dev/null
printf '# Kept\n\nWritten under 0.1.\n' >"$data/vaults/notes/kept.md"

echo "2. $image runs the first upgrade"
run_server "$image"
wait_healthy
docker logs "$name" 2>&1 | grep -q '"msg":"first upgrade"' || fail "no first upgrade record in the logs"
[ "$(cat "$state/format")" = 2 ] || fail "format is $(cat "$state/format"), want 2"
uuid=$(sed -n 2p "$marker")
printf '%s\n' "$uuid" | grep -Eqx '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}' ||
	fail "the vault marker names no vault UUID: $(cat "$marker")"
[ "$(sed -n 3p "$marker")" = 2 ] || fail "the vault marker isn't at format 2: $(cat "$marker")"
grep -Eqx "$uuid [0-9].*" "$state/pre-format-2/complete" ||
	fail "pre-format-2/ isn't complete: $(cat "$state/pre-format-2/complete" 2>&1)"
login_and_read "after the upgrade"

echo "3. 0.1 refuses format 2"
docker stop "$name" >/dev/null
run_server "$old"
i=0
until docker logs "$name" 2>&1 | grep -q 'newer PostelMD (format 2)'; do
	i=$((i + 1))
	[ "$i" -le 60 ] || fail "0.1 didn't refuse the upgraded data"
	sleep 0.5
done

echo "4. the runbook's rollback, as written"
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
[ "$(cat "$state/format")" = 1 ] || fail "format is $(cat "$state/format") after the rollback, want 1"
[ -f "$marker" ] && [ ! -s "$marker" ] || fail "the vault marker isn't empty after the rollback"

echo "5. 0.1 starts again"
[ "$(docker inspect -f '{{.Config.Image}}' "$name")" = "$old" ] || fail "step 3 didn't start 0.1"
wait_old
login_and_read "0.1 after the rollback"

echo "6. db rebuild and vault claim in a container, at format 2"
docker stop "$name" >/dev/null
run_server "$image"
wait_healthy
docker stop "$name" >/dev/null
docker rm "$name" >/dev/null
out=$(docker run --rm --user "$user" -v "$data:/data" "$image" db rebuild 2>&1) || fail "db rebuild: $out"
printf '%s\n' "$out" | grep -q 'Made a new database' || fail "db rebuild said: $out"
: >"$marker"
out=$(docker run --rm --user "$user" -v "$data:/data" "$image" vault claim --yes 2>&1) || fail "vault claim: $out"
[ "$(sed -n 2p "$marker")" != "" ] || fail "vault claim wrote no vault UUID: $out"
run_server "$image"
wait_healthy
login_and_read "after db rebuild and vault claim"
echo "OK: the upgrade, the runbook's rollback, and db rebuild and vault claim at format 2"
