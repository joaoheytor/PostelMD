# shellcheck shell=sh
# Helpers shared by the image smoke tests (image-smoke.sh, image-smoke-adopt.sh); sourced,
# never run. The caller sets: name (the container), base (http://127.0.0.1:<port>) and jar
# (a cookie file). Helpers set globals (status, token, checked, setup_resp): don't reuse
# those names for your own values.
# shellcheck disable=SC2154 # name, base and jar come from the caller

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

body() { # body RESPONSE: the response without its status line
	printf '%s\n' "$1" | sed '$d'
}

expect() { # expect STATUS RESPONSE CONTEXT
	status=$(printf '%s\n' "$2" | tail -n 1)
	[ "$status" = "$1" ] || fail "$3: HTTP $status, want $1: $(body "$2")"
}

# setup_from_logs reads the setup token from the container's logs and completes setup.
# It leaves the setup check's response in $checked and the setup's in $setup_resp.
setup_from_logs() {
	token=$(docker logs "$name" 2>/dev/null | sed -n 's/.*(valid [^)]*): *\([^ ]*\).*/\1/p' | head -n 1)
	[ -n "$token" ] || fail "no setup token in the logs"
	checked=$(api POST /api/v1/setup/check -H 'Content-Type: application/json' -d "{\"token\":\"$token\"}")
	expect 200 "$checked" "setup check"
	setup_resp=$(api POST /api/v1/setup -H 'Content-Type: application/json' \
		-d "{\"token\":\"$token\",\"password\":\"smoke test password\",\"name\":\"Smoke\",\"email\":\"smoke@example.invalid\"}")
	expect 201 "$setup_resp" "setup"
}
