#!/bin/sh
# The vulnerability gate of ADR-0015 on an image (default postelmd:dev): no critical
# vulnerability at all, and no high one that has a fix. Trivy runs from its own image,
# pinned by digest (TRIVY_IMAGE), on a `docker save` of the image, so it never needs the
# Docker socket. Its database is cached in bin/trivy-cache between local runs.
set -eu
image=${1:-postelmd:dev}
trivy=${TRIVY_IMAGE:?set TRIVY_IMAGE to a digest-pinned Trivy image}
cache=$(pwd)/bin/trivy-cache
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$cache"

docker save "$image" -o "$work/image.tar"
chmod a+r "$work/image.tar"

scan() {
	docker run --rm --user "$(id -u):$(id -g)" \
		-v "$work:/scan:ro" -v "$cache:/cache" \
		"$trivy" image --cache-dir /cache --scanners vuln --exit-code 1 --no-progress --skip-version-check \
		--input /scan/image.tar "$@"
}

status=0
echo "== critical (any)"
scan --severity CRITICAL || status=1
echo "== high (with a fix)"
scan --severity HIGH --ignore-unfixed --skip-db-update || status=1
if [ "$status" -ne 0 ]; then
	echo "FAIL: $image has vulnerabilities over the ADR-0015 threshold" >&2
	exit 1
fi
echo "OK: no critical vulnerabilities, and no high ones with a fix"
