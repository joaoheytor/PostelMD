#!/bin/sh
# Verifies one exact registry address of a release candidate or a release (docs/ci-cd.md §4,
# release.yaml): the signature, the build provenance on the index and the SPDX SBOM on each
# platform digest, with cosign and with `gh attestation verify --bundle-from-oci` (both read
# the registry named, never only GitHub's attestation API).
#
# Usage: release-verify-image.sh <repo> <index> <amd64> <arm64> <rc tag> <commit> [<release tag>]
#   The provenance and SBOMs must verify against prerelease.yaml@refs/tags/<rc tag>, built
#   from <commit>. The signature must too, or, with <release tag>, against
#   release.yaml@refs/tags/<release tag> (the signature release.yaml makes at each address).
# Needs: cosign v3 and gh (GH_TOKEN) on PATH.
set -eu
repo=${1:?repo} index=${2:?index digest} amd64=${3:?amd64 digest} arm64=${4:?arm64 digest}
rc=${5:?rc tag} commit=${6:?commit} release=${7:-}

wf=https://github.com/joaoheytor/PostelMD/.github/workflows
issuer=https://token.actions.githubusercontent.com
rc_id=$wf/prerelease.yaml@refs/tags/$rc
spdx=https://spdx.dev/Document/v2.3
digest_re='^sha256:[0-9a-f]{64}$'
for d in "$index" "$amd64" "$arm64"; do
	printf '%s\n' "$d" | grep -qE "$digest_re" || { echo "not a digest: $d" >&2; exit 1; }
done
printf '%s\n' "$commit" | grep -qE '^[0-9a-f]{40}$' || { echo "not a commit: $commit" >&2; exit 1; }

sig_id=$rc_id
[ -z "$release" ] || sig_id=$wf/release.yaml@refs/tags/$release

echo "== $repo@$index: signature by $sig_id"
cosign verify "$repo@$index" --certificate-oidc-issuer "$issuer" \
	--certificate-identity "$sig_id" --certificate-github-workflow-sha "$commit" >/dev/null

attest() { # attest <digest> <cosign type> <gh predicate type>
	echo "== $repo@$1: $2 by $rc_id"
	cosign verify-attestation "$repo@$1" --type "$2" --certificate-oidc-issuer "$issuer" \
		--certificate-identity "$rc_id" --certificate-github-workflow-sha "$commit" >/dev/null
	gh attestation verify "oci://$repo@$1" --bundle-from-oci --repo joaoheytor/PostelMD \
		--deny-self-hosted-runners --cert-identity "$rc_id" --cert-oidc-issuer "$issuer" \
		--source-ref "refs/tags/$rc" --source-digest "$commit" --predicate-type "$3" >/dev/null
}
attest "$index" slsaprovenance1 https://slsa.dev/provenance/v1
attest "$amd64" "$spdx" "$spdx"
attest "$arm64" "$spdx" "$spdx"
echo "OK: $repo@$index"
