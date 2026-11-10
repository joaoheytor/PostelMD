# Verifying PostelMD images

How users, the homelab and admission controllers check that an image is a real PostelMD
release, and the evidence behind the design ([ci-cd §4](../ci-cd.md#4-images-and-tags),
ADR-0015).

## Identities

All certificates are issued by `https://token.actions.githubusercontent.com` (keyless cosign
and `actions/attest-build-provenance`, a wrapper over `actions/attest`).

| What | Signed by (certificate identity) | Where |
|---|---|---|
| Release signature | `https://github.com/joaoheytor/PostelMD/.github/workflows/release.yaml@refs/tags/vX.Y.Z` | Docker Hub and GHCR |
| Build provenance (on the index digest) and SBOMs (one per platform digest) of a release | `https://github.com/joaoheytor/PostelMD/.github/workflows/prerelease.yaml@refs/tags/vX.Y.Z-rc.N` (the RC that was promoted) | Docker Hub, GHCR and GitHub's attestation API |
| Release candidate: signature, provenance, SBOMs | `prerelease.yaml@refs/tags/vX.Y.Z-rc.N` | GHCR and GitHub's attestation API |
| Candidate from `main` (for testers, never for production): signature, provenance, SBOMs | `https://github.com/joaoheytor/PostelMD/.github/workflows/candidate.yaml@refs/heads/main` | GHCR and GitHub's attestation API |

**A released image** is one that passes **both** the release-signature check and the
provenance check below. A release signature alone doesn't say how the image was built;
provenance alone doesn't say the maintainer released it.

## Commands

Use cosign v3 (cosign v2 needs `--new-bundle-format`). Every command works with
`ghcr.io/joaoheytor/postelmd` in place of `docker.io/joaoheytor/postelmd`. Verify a digest
(`@sha256:…`), not a tag: a tag can move after you check it.

```sh
IMG=docker.io/joaoheytor/postelmd@sha256:<index digest>
ISSUER=https://token.actions.githubusercontent.com

# 1. Release signature.
cosign verify "$IMG" --certificate-oidc-issuer "$ISSUER" \
  --certificate-identity-regexp '^https://github\.com/joaoheytor/PostelMD/\.github/workflows/release\.yaml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$'

# 2. Build provenance, made by the RC that was promoted.
cosign verify-attestation "$IMG" --type slsaprovenance1 --certificate-oidc-issuer "$ISSUER" \
  --certificate-identity-regexp '^https://github\.com/joaoheytor/PostelMD/\.github/workflows/prerelease\.yaml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$'

# 2'. The same with the GitHub CLI (logged in).
gh attestation verify "oci://$IMG" --repo joaoheytor/PostelMD --deny-self-hosted-runners \
  --bundle-from-oci \
  --cert-identity-regex '^https://github\.com/joaoheytor/PostelMD/\.github/workflows/prerelease\.yaml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$'
```

- **One version, not any release:** the regexps accept every release. To check that the
  image is 0.1.0, pin it: `--certificate-identity
  https://github.com/joaoheytor/PostelMD/.github/workflows/release.yaml@refs/tags/v0.1.0` in
  step 1, and `v0\.1\.0-rc\.[0-9]+` in the step 2 regexps.
- **`--bundle-from-oci`** makes `gh` read the bundles **from the registry you named**.
  Without it, `gh` looks the digest up in GitHub's attestation API, which passes for any
  registry holding that digest and says nothing about what Docker Hub (or GHCR) stores.

### SBOM (per platform)

Each platform image has its own SBOM, attested on the **platform manifest's digest**, not on
the index. Get the digest, then verify:

```sh
crane digest --platform linux/amd64 docker.io/joaoheytor/postelmd@sha256:<index digest>
# or: docker buildx imagetools inspect docker.io/joaoheytor/postelmd@sha256:<index digest>

cosign verify-attestation docker.io/joaoheytor/postelmd@sha256:<amd64 digest> \
  --type https://spdx.dev/Document/v2.3 --certificate-oidc-issuer "$ISSUER" \
  --certificate-identity-regexp '^https://github\.com/joaoheytor/PostelMD/\.github/workflows/prerelease\.yaml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$'
```

With `gh`, add `--predicate-type https://spdx.dev/Document/v2.3` to step 2' and use the
platform digest. The SBOM files are also assets of the GitHub Release.

### Binaries (GitHub Release assets)

A release carries `postelmd-linux-amd64`, `postelmd-linux-arm64`, their SBOMs, a
`SHA256SUMS` and its cosign bundle `SHA256SUMS.sigstore.json`. Check the release signature on
the checksums, the checksums on the files, then the build provenance of the binary itself:

```sh
V=0.1.0
gh release download "v$V" --repo joaoheytor/PostelMD
cosign verify-blob SHA256SUMS --bundle SHA256SUMS.sigstore.json \
  --certificate-oidc-issuer "$ISSUER" \
  --certificate-identity "https://github.com/joaoheytor/PostelMD/.github/workflows/release.yaml@refs/tags/v$V"
sha256sum -c SHA256SUMS
gh attestation verify postelmd-linux-amd64 --repo joaoheytor/PostelMD --deny-self-hosted-runners \
  --cert-identity-regex "^https://github\.com/joaoheytor/PostelMD/\.github/workflows/prerelease\.yaml@refs/tags/v${V//./\\.}-rc\.[0-9]+\$"
```

The binary reports the RC's version (`postelmd --version` prints `X.Y.Z-rc.N`): a release
promotes the RC's tested bytes and never rebuilds them. An RC's pre-release carries the same
files without the signed `SHA256SUMS.sigstore.json`; check them with the last command.

### Release candidates and candidates

An RC (`ghcr.io/joaoheytor/postelmd:X.Y.Z-rc.N`, GHCR only) has no release signature: check
steps 1 and 2 with the `prerelease.yaml` identity. For one RC, pin it exactly:

```sh
RC=v0.1.0-rc.1
IMG=ghcr.io/joaoheytor/postelmd@sha256:<index digest>   # the digest its pre-release records
cosign verify "$IMG" --certificate-oidc-issuer "$ISSUER" \
  --certificate-identity "https://github.com/joaoheytor/PostelMD/.github/workflows/prerelease.yaml@refs/tags/$RC"
cosign verify-attestation "$IMG" --type slsaprovenance1 --certificate-oidc-issuer "$ISSUER" \
  --certificate-identity "https://github.com/joaoheytor/PostelMD/.github/workflows/prerelease.yaml@refs/tags/$RC"
```

Add `--certificate-github-workflow-sha <tagged commit>` to bind it to the commit as well, as
`release.yaml` does (`scripts/release-verify-image.sh` is the exact check it runs on each
address). A candidate (`:sha-<short>`, `:main`) is
checked the same way with `--certificate-identity
https://github.com/joaoheytor/PostelMD/.github/workflows/candidate.yaml@refs/heads/main`.

### Admission control (Kyverno)

A `verifyImages` rule needs **both** identities: the release signature (`release.yaml`
regexp) as `attestors`, and the provenance attestation (`prerelease.yaml` regexp,
predicate `https://slsa.dev/provenance/v1`) under `attestations`.

## Evidence (spike #31)

**Spike 1, copying signatures** (2026-11-09; cosign v2.6.5 and v3.1.4, crane, two zot
v2.1.11 registries with the OCI referrers API and one `registry:3` without it; a test key,
no transparency log, nothing published):

| Signed with | Stored as | `cosign copy` to another registry | Plain copy (`crane copy`) | Signing at the destination |
|---|---|---|---|---|
| cosign v2 (legacy) | `sha256-<digest>.sig` / `.att` tags | Carried; signature and attestation verify there | Lost | Works |
| cosign v3 (default) | Sigstore bundle v0.3 as OCI **referrers** (the format `actions/attest` uses too) | **Not carried**, even to a registry with the referrers API | Lost | Works; without the referrers API (`registry:3`), cosign falls back to the OCI 1.1 tag schema |

**Spike 2, provenance at a second registry** (2026-11-10; cosign v3.1.4, gh 2.101.0, crane,
`registry:2` and zot v2.1.11 as the second registry; public keyless-signed images only, no
credentials, nothing published). The subject was an image whose provenance `actions/attest`
made for its GHCR address, with its digest copied to the second registry:

| Check at the second address | Plain copy (`registry:2`) | Image + referrer manifests copied by digest (zot) |
|---|---|---|
| `gh attestation verify oci://…` (GitHub API) | **Passes**: the lookup is `GET /orgs/<owner>/attestations/sha256:<digest>`, by digest only | Passes |
| `gh attestation verify --bundle-from-oci` | Fails: no attestations in the registry | **Passes** |
| `cosign verify-attestation --type slsaprovenance1` | Fails: no matching attestations | **Passes**; the statement's subject still names `ghcr.io/…` |

Also observed, read-only, on public data:
- Docker Hub serves the OCI 1.1 referrers API (`GET /v2/<name>/referrers/<digest>` returns
  an OCI index) and already holds cosign v3 bundles for other projects.
- GHCR answers that endpoint with 404 and uses the referrers **tag schema** (an index tagged
  `sha256-<digest>`).
- Keyless cosign v3 signatures of one digest verify at both `docker.io` and `ghcr.io`.

**Consequences:**
- Sign **at each address**: copying signatures only works for a format we don't use.
- Copy provenance and SBOMs **verbatim**: they are bound to the digest, not to an address.
- Verifiers check the digest, not the address. So the release gate verifies each exact
  registry address, never a digest alone.

**Measured only by the first promotion** (it needs the `release` token), which fails closed:
- keyless signing into Docker Hub under our identity;
- Docker Hub accepting copied bundle referrers whose subject is an index;
- the SPDX predicate URI;
- the `gh` token permissions in the gate;
- an equal digest at both registries after `crane copy`.
