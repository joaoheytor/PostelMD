#!/bin/sh
# The `docker run` smoke test of ADR-0009 against an image (default postelmd:dev):
#   boot on a named volume; read the setup token from the logs; complete setup; save a
#   note; restart; check that the note and the session persist; check that a second
#   container on the same /data refuses to start; then boot on a bind mount with --user,
#   as in the README. Also checks the image's size, user and lack of a shell.
set -eu
image=${1:-postelmd:dev}
name=postelmd-smoke-$$
volume=$name-data
port=${SMOKE_PORT:-18090}
base=http://127.0.0.1:$port
work=$(mktemp -d)
jar=$work/cookies

cleanup() {
	docker rm -f "$name" "$name-2" >/dev/null 2>&1 || true
	docker volume rm "$volume" >/dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT

fail() {
	echo "FAIL: $*" >&2
	docker logs "$name" >&2 2>&1 || true
	exit 1
}

# wait_healthy waits for Docker's own health check, which runs /postelmd healthcheck.
wait_healthy() {
	i=0
	while [ "$(docker inspect -f '{{.State.Health.Status}}' "$name")" != healthy ]; do
		i=$((i + 1))
		[ "$i" -le 60 ] || fail "not healthy after 30 s"
		sleep 0.5
	done
}

api() { # api METHOD PATH [curl args...]: prints the body, then the status on its own line
	method=$1 path=$2
	shift 2
	curl -sS -b "$jar" -c "$jar" -X "$method" -w '\n%{http_code}' "$@" "$base$path"
}

expect() { # expect STATUS RESPONSE CONTEXT
	got=$(printf '%s\n' "$2" | tail -n 1)
	[ "$got" = "$1" ] || fail "$3: HTTP $got, want $1: $(printf '%s\n' "$2" | sed '$d')"
}

size=$(docker image inspect -f '{{.Size}}' "$image")
echo "image $image: $size bytes"
[ "$size" -lt 40000000 ] || fail "the image is $size bytes; the budget is 40 MB (docs/ci-cd.md)"
[ "$(docker image inspect -f '{{.Config.User}}' "$image")" = 65532:65532 ] || fail "the image doesn't run as 65532:65532"
if docker run --rm --entrypoint /bin/sh "$image" -c true >/dev/null 2>&1; then
	fail "the image has a shell"
fi

echo "1. boot"
docker volume create "$volume" >/dev/null
docker run -d --name "$name" --stop-timeout 60 -p "127.0.0.1:$port:8080" -v "$volume:/data" "$image" >/dev/null
wait_healthy

echo "2. setup token from the logs"
token=$(docker logs "$name" 2>/dev/null | sed -n 's/.*(valid [^)]*): *\([^ ]*\).*/\1/p' | head -n 1)
[ -n "$token" ] || fail "no setup token in the logs"

echo "3. setup"
expect 200 "$(api POST /api/v1/setup/check -H 'Content-Type: application/json' -d "{\"token\":\"$token\"}")" "setup check"
expect 201 "$(api POST /api/v1/setup -H 'Content-Type: application/json' \
	-d "{\"token\":\"$token\",\"password\":\"smoke test password\",\"name\":\"Smoke\",\"email\":\"smoke@example.invalid\"}")" "setup"

echo "4. save a note"
expect 201 "$(api PUT /api/v1/notes/smoke.md -H 'If-None-Match: *' -H 'Content-Type: text/markdown; charset=utf-8' \
	--data-binary '# Smoke test')" "create a note"

echo "5. restart"
docker restart -t 60 "$name" >/dev/null
wait_healthy

echo "6. the note and the session persist"
resp=$(api GET /api/v1/notes/smoke.md)
expect 200 "$resp" "read the note after a restart"
[ "$(printf '%s\n' "$resp" | sed '$d')" = "# Smoke test" ] || fail "the note changed across the restart"

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
