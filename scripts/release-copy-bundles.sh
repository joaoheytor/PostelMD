#!/bin/sh
# Copies a release candidate's attestations from GHCR to another registry verbatim
# (docs/ci-cd.md §4, "What release.yaml copies to Docker Hub"). For the index and each
# platform digest it reads the referrers from GHCR's referrers tag (`sha256-<hex>`; GHCR has
# no referrers API), keeps the manifests whose artifactType is a Sigstore bundle v0.3 holding
# a DSSE attestation, verifies each bundle against the exact RC identity and commit, and
# copies only those, by digest, with crane. The index must carry build provenance and each
# platform an SPDX SBOM, or it fails. Signatures are not copied: each address is signed.
#
# Usage: release-copy-bundles.sh <src repo> <dst repo> <index> <amd64> <arm64> <rc tag> <commit>
# Needs: crane, jq, gh (GH_TOKEN) on PATH; write access to <dst repo> in the docker config.
set -eu
src=${1:?src repo} dst=${2:?dst repo} index=${3:?index} amd64=${4:?amd64} arm64=${5:?arm64}
rc=${6:?rc tag} commit=${7:?commit}

bundle_type=application/vnd.dev.sigstore.bundle.v0.3+json
slsa=https://slsa.dev/provenance/v1
spdx=https://spdx.dev/Document/v2.3
rc_id=https://github.com/joaoheytor/PostelMD/.github/workflows/prerelease.yaml@refs/tags/$rc
digest_re='^sha256:[0-9a-f]{64}$'
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

copy_for() { # copy_for <subject digest> <required predicate type>
	subject=$1 want=$2 found=0
	crane manifest "$src:sha256-${subject#sha256:}" >"$work/referrers.json" ||
		{ echo "no referrers for $src@$subject" >&2; exit 1; }
	jq -r --arg t "$bundle_type" '.manifests[] | select(.artifactType == $t) | .digest' \
		"$work/referrers.json" >"$work/candidates"
	while read -r ref; do
		printf '%s\n' "$ref" | grep -qE "$digest_re" || { echo "bad referrer digest: $ref" >&2; exit 1; }
		crane manifest "$src@$ref" >"$work/m.json"
		jq -e --arg s "$subject" '.subject.digest == $s and (.layers | length) == 1' "$work/m.json" \
			>/dev/null || { echo "skip $ref: not a single-layer referrer of $subject"; continue; }
		crane blob "$src@$(jq -r '.layers[0].digest' "$work/m.json")" >"$work/bundle.json"
		pt=$(jq -r '.dsseEnvelope.payload // empty' "$work/bundle.json" | base64 -d 2>/dev/null |
			jq -r '.predicateType // empty' 2>/dev/null) || pt=
		case $pt in
		"$slsa" | "$spdx") ;;
		*) echo "skip $ref: not a provenance or SBOM attestation"; continue ;;
		esac
		if ! gh attestation verify "oci://$src@$subject" --bundle "$work/bundle.json" \
			--repo joaoheytor/PostelMD --deny-self-hosted-runners --cert-identity "$rc_id" \
			--cert-oidc-issuer https://token.actions.githubusercontent.com \
			--source-ref "refs/tags/$rc" --source-digest "$commit" --predicate-type "$pt" \
			>/dev/null 2>&1; then
			echo "skip $ref: does not verify against $rc_id"
			continue
		fi
		crane copy "$src@$ref" "$dst@$ref"
		echo "copied $pt bundle $ref for $subject"
		[ "$pt" != "$want" ] || found=$((found + 1))
	done <"$work/candidates"
	[ "$found" -gt 0 ] || { echo "no verified $want bundle for $src@$subject" >&2; exit 1; }
}

copy_for "$index" "$slsa"
copy_for "$amd64" "$spdx"
copy_for "$arm64" "$spdx"
echo "OK: the RC's verified bundles are at $dst"
