#!/bin/sh
# The `docker run` smoke test of ADR-0009 against an image (default postelmd:dev):
#   boot on a named volume; read the setup token from the logs; complete setup; save a
#   note, within FIRST_NOTE_BUDGET seconds of `docker run` (roadmap v0.1: < 2 minutes,
#   human steps included); restart; check that the note and the session persist; check
#   that a second container on the same /data refuses to start; then boot on a bind mount
#   with --user, as in the README. Also checks the image's size, user and lack of a shell.
#   Adopting an existing notes folder is image-smoke-adopt.sh.
set -eu
image=${1:-postelmd:dev}
name=postelmd-smoke-$$
volume=$name-data
port=${SMOKE_PORT:-18090}
budget=${FIRST_NOTE_BUDGET:-120}
base=http://127.0.0.1:$port
work=$(mktemp -d)
jar=$work/cookies
# shellcheck source=scripts/image-smoke-lib.sh
. "$(dirname "$0")/image-smoke-lib.sh"

cleanup() {
	docker rm -f "$name" "$name-2" >/dev/null 2>&1 || true
	docker volume rm "$volume" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT

size=$(docker image inspect -f '{{.Size}}' "$image")
echo "image $image: $size bytes"
[ "$size" -lt 40000000 ] || fail "the image is $size bytes; the budget is 40 MB (docs/ci-cd.md)"
[ "$(docker image inspect -f '{{.Config.User}}' "$image")" = 65532:65532 ] || fail "the image doesn't run as 65532:65532"
if docker run --rm --entrypoint /bin/sh "$image" -c true >/dev/null 2>&1; then
	fail "the image has a shell"
fi

echo "1. boot"
docker volume create "$volume" >/dev/null
started=$(date +%s)
docker run -d --name "$name" --stop-timeout 60 -p "127.0.0.1:$port:8080" -v "$volume:/data" "$image" >/dev/null
wait_healthy

echo "2-3. setup, with the token from the logs"
setup_from_logs

echo "4. save a note"
expect 201 "$(api PUT /api/v1/notes/smoke.md -H 'If-None-Match: *' -H 'Content-Type: text/markdown; charset=utf-8' \
	--data-binary '# Smoke test')" "create a note"
elapsed=$(($(date +%s) - started))
echo "   first note saved $elapsed s after docker run (budget $budget s)"
[ "$elapsed" -le "$budget" ] || fail "the first note took $elapsed s from docker run; the budget is $budget s"

echo "5. restart"
docker restart -t 60 "$name" >/dev/null
wait_healthy

echo "6. the note and the session persist"
resp=$(api GET /api/v1/notes/smoke.md)
expect 200 "$resp" "read the note after a restart"
[ "$(body "$resp")" = "# Smoke test" ] || fail "the note changed across the restart"

echo "7. a second container on the same /data refuses to start"
if docker run --name "$name-2" -v "$volume:/data" "$image" >"$work/second.log" 2>&1; then
	fail "a second container started on the same /data"
fi
grep -q "another PostelMD process is using this data directory" "$work/second.log" ||
	fail "the second container failed for another reason: $(cat "$work/second.log")"

echo "8. the canonical bind mount, owned by the host user, with --user (README)"
docker rm -f "$name" >/dev/null
mkdir "$work/data"
docker run -d --name "$name" --stop-timeout 60 -p "127.0.0.1:$port:8080" \
	--user "$(id -u):$(id -g)" -v "$work/data:/data" "$image" >/dev/null
wait_healthy
[ -d "$work/data/vaults/notes/.git" ] || fail "no vault in the bind mount"

echo "OK"
