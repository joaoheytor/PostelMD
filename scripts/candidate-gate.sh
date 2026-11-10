#!/bin/sh
# The gates candidate.yaml runs on a pushed multi-arch image, by digest (ADR-0015 stage 10):
# resolve the linux/amd64 and linux/arm64 manifest digests from the index; run the
# `docker run` smoke tests and the Trivy gate on the amd64 digest and the Trivy gate on the
# arm64 digest; write a Syft SPDX SBOM of each platform digest. The arm64 binary is *run*
# on every commit by ci.yaml's image-arm64 job; here it is only scanned.
#
# Usage: candidate-gate.sh <repository> <index digest> <output dir>
# Writes <output dir>/digest-<arch> and <output dir>/sbom-<arch>.spdx.json.
# Needs: docker with buildx, jq, syft on PATH, TRIVY_IMAGE (make -s print-trivy-image).
set -eu
repo=${1:?repository}
index=${2:?index digest}
out=${3:?output dir}
: "${TRIVY_IMAGE:?set TRIVY_IMAGE to a digest-pinned Trivy image}"
here=$(dirname "$0")
digest_re='^sha256:[0-9a-f]{64}$'
printf '%s\n' "$index" | grep -qE "$digest_re" || { echo "not a digest: $index" >&2; exit 1; }
mkdir -p "$out"

docker buildx imagetools inspect --raw "$repo@$index" >"$out/index.json"
for arch in amd64 arm64; do
	d=$(jq -r --arg a "$arch" \
		'[.manifests[] | select(.platform.os == "linux" and .platform.architecture == $a) | .digest] | if length == 1 then .[0] else "" end' \
		"$out/index.json")
	printf '%s\n' "$d" | grep -qE "$digest_re" \
		|| { echo "no single linux/$arch manifest in $repo@$index" >&2; exit 1; }
	printf '%s\n' "$d" >"$out/digest-$arch"
done

amd64=$repo@$(cat "$out/digest-amd64")
arm64=$repo@$(cat "$out/digest-arm64")

echo "== linux/amd64: $amd64"
docker pull -q --platform linux/amd64 "$amd64"
"$here/image-smoke.sh" "$amd64"
"$here/image-smoke-adopt.sh" "$amd64"
"$here/image-scan.sh" "$amd64"

echo "== linux/arm64: $arm64"
docker pull -q --platform linux/arm64 "$arm64"
"$here/image-scan.sh" "$arm64"

for arch in amd64 arm64; do
	ref=$repo@$(cat "$out/digest-$arch")
	SYFT_CHECK_FOR_APP_UPDATE=false syft scan -q "registry:$ref" -o "spdx-json=$out/sbom-$arch.spdx.json"
	jq -e '[.packages[].name] | index("github.com/joaoheytor/PostelMD") != null' \
		"$out/sbom-$arch.spdx.json" >/dev/null \
		|| { echo "the linux/$arch SBOM doesn't list the postelmd module" >&2; exit 1; }
	echo "SBOM linux/$arch: $out/sbom-$arch.spdx.json"
done
echo "OK: $repo@$index passed the candidate gates"
