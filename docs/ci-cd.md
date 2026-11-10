# PostelMD: CI/CD

- **Status:** Draft v0.1
- **Last updated:** 2026-11-10
- **Decisions:** [ADR-0015](adr/0015-supply-chain-and-ci.md). This document describes the
  pipelines, repository settings and the hand-off to the homelab.

## 1. Principles
- **Public repository from day one** (`github.com/joaoheytor/PostelMD`). Every workflow
  assumes untrusted contributors.
- **GitHub-hosted runners only** for this repository. The homelab's ARC runners never run
  PostelMD workflows.
- **Gates are per milestone** (see [roadmap](roadmap.md#release-gates-by-milestone)). A gate
  turns on when its artifact exists and never turns off.
- **Local parity:** `make ci` runs the same checks as the `ci` workflow (except signing and
  publishing), so you don't debug CI by pushing.
- **One artifact, many addresses:** an image is built once (for its RC), and the **tested digest
  is promoted**, never rebuilt, to Docker Hub (canonical) and GHCR (mirror).
- **Trunk-based:** `main` is always releasable; short-lived PR branches; releases are tags on
  `main`; `release/X.Y` branches only when an older minor needs a fix (ADR-0015).

## 2. Repository settings (v0.0, before the first push of code)

| Setting | Value |
|---|---|
| Default branch | `main` |
| Ruleset on `main` | PRs required (no direct pushes, including the maintainer); required checks: `required` (from the GitHub Actions app; it's the **job** name) and `DCO`; linear history; no force pushes or deletions; conversation resolution required. **Bypass:** the repository admin role, in *pull request only* mode: the maintainer can merge a PR without its checks (for example during a GitHub Actions outage), but can never push to `main` directly. Every bypass is visible in the PR and the ruleset insights; say why in the PR. |
| Ruleset on tags `v*` | Only the maintainer (admin role) may create, update or delete them; verified to work on this personal repository (#32). Rulesets can't require a *signed tag object*, so every release workflow first runs `scripts/verify-release-tag.sh`, which requires: a SemVer name (`vX.Y.Z` or `vX.Y.Z-rc.N`); an annotated tag pointing to a commit; a valid SSH signature whose key matches a principal in `.github/allowed_signers` **as of `origin/main`** (a tag can't bring its own key, and removing a key on `main` revokes it); and the tagged commit on `main`. `actions/checkout` can turn an annotated tag into a lightweight one, so workflows re-fetch the tag object and `main` first. The script's tests (`make test-scripts`) run on every PR. |
| Merge method | **Squash only**, with the default squash message set to **"Pull request title and commit details"**. (Otherwise GitHub uses the commit's own message for single-commit PRs, bypassing the `pr-title` check.) The title must be a Conventional Commit; the commit details keep the `Signed-off-by:` trailers. |
| DCO | DCO GitHub App installed; required check |
| Actions | Default `GITHUB_TOKEN` permissions **read-only**; "Require approval for all outside collaborators" for fork PR workflows; allow only GitHub-owned actions, verified Marketplace actions, and an explicit allow-list of repositories: currently `golangci/golangci-lint-action@*` (a verified organization isn't enough; the action itself must be a verified Marketplace listing). Workflows still pin every action by SHA. An action outside the policy makes the whole workflow fail at startup (`startup_failure`), with no jobs and no checks. |
| Security | Private vulnerability reporting **on** (SECURITY.md relies on it); secret scanning + push protection **on**; Dependabot alerts **on** (Renovate opens the update PRs) |
| Environments | `release`: deployment only from stable tags (`vX.Y.Z`, no pre-release suffix); required reviewer: the maintainer; holds the `DOCKERHUB_TOKEN` secret. `DOCKERHUB_USERNAME` is a **variable** (`vars`), not a secret, so image names aren't masked in logs. |
| Packages | After the first GHCR push, set the `postelmd` package to **public** (packages from a personal account start private) and link it to the repository. |
| Files | `CODEOWNERS` (`* @joaoheytor`), issue templates (bug report per CONTRIBUTING, feature request requiring a Non-Goals check), PR template with the Definition of Done |

## 3. Workflows

| Workflow | Trigger | Purpose | Permissions |
|---|---|---|---|
| `ci.yaml` | `pull_request`, `push` to `main` | Lint, tests, failure scenarios, build, scan. Ends with a `required` aggregator job (the single required check, so jobs can be added without editing the ruleset). | `contents: read` |
| `codeql.yaml` | `pull_request`, `push` to `main`, weekly | CodeQL for Go and JS/TS | `contents: read`, `actions: read`, `security-events: write` |
| `scorecard.yaml` | weekly, `push` to `main` | OpenSSF Scorecard (keeps the hygiene honest) | `contents: read`, `security-events: write`, `id-token: write` |
| `perf-noise.yaml` | `workflow_dispatch`; `pull_request` touching `test/perf/editor/**` | Runs the editor benchmark (`test/perf/editor`, PRD procedure) on 10 runners in parallel and summarizes the noise between and within runners (#33). Informational, not required. | `contents: read` |
| `perf.yaml` | `workflow_dispatch`; `pull_request` touching `test/perf/app/**` | Runs the PRD budgets for one user against the real binary (`test/perf/app`, `make perf`): typing with the preview open against a `<textarea>` control, saves while typing, cold start and idle RSS on a 10,000-note vault. Fails on a missed budget; not a required check until the v0.2 regression gate |
| `candidate.yaml` (from v0.1) | `push` to `main`; `pull_request` touching the image build (`build` job only) | Builds and publishes **candidate** images (`:sha-<short>`, `:main`) to GHCR only: signed (`cosign sign --recursive`), with build provenance and a per-platform SBOM attested (§4, *Candidates*). Publish job in concurrency group `ghcr-referrers`. | `build`: `contents: read`; `publish`: `contents: read`, `packages: write`, `id-token: write`, `attestations: write` |
| `prerelease.yaml` (from v0.1) | `push` of tags matching `v[0-9]+.[0-9]+.[0-9]+-rc.[0-9]+` | **The only workflow that builds a release image.** A `build` job (no secrets, no caches) checks the tag (`scripts/verify-release-tag.sh`), builds the binaries once and gates each platform digest on a registry on the runner, as `candidate.yaml` does. A `publish` job (concurrency group `ghcr-referrers`) pushes the same bytes **untagged** to GHCR and fails unless the digest equals the gated one, signs it (`--recursive`), attests build provenance (index) and an SBOM per platform (the only build provenance a release has; §4), attests build provenance on the binaries and SBOM files, applies `:X.Y.Z-rc.N`, and creates a GitHub *pre-release* that records the index digest (a `postelmd-image-digest:` line) and carries those files as assets. A re-run that reaches a different digest fails: that is a new RC. Never touches Docker Hub or the stable tags; no `release` environment. | `build`: `contents: read`; `publish`: `contents: write`, `packages: write`, `id-token: write`, `attestations: write` |
| `release.yaml` (from v0.1) | `push` of tags matching `v[0-9]+.[0-9]+.[0-9]+` (stable only; RC tags don't match) | **Promotes, never rebuilds.** A `verify` job (no secrets) checks the tag, finds the highest `vX.Y.Z-rc.N` on the same commit (and checks that tag too), takes its digest from that pre-release record, checks its signature, provenance and SBOMs pinned to that exact RC, checks the vulnerability thresholds again (§4) and that `CHANGELOG.md` has the release's section. A `publish` job (environment `release`, concurrency group `ghcr-referrers`) then signs at GHCR, copies the digest and the RC's verified bundles to Docker Hub and signs there, verifies both addresses, moves the stable tags and creates the GitHub Release (the RC's binaries and SBOMs, each verified against the RC identity, and a `SHA256SUMS` signed with `cosign sign-blob`; chart from v0.3). It never creates build provenance (§4). | `verify`: `contents: read`, `attestations: read`; `publish`: `contents: write`, `packages: write`, `id-token: write`, `attestations: read` (no `attestations: write`: it attests nothing) |

`prerelease.yaml` and `release.yaml` run harden-runner with `egress-policy: block`. Each job's
`allowed-endpoints` are the hosts the `candidate.yaml` and `ci.yaml` runs on `main` reached
(harden-runner's post-step log), plus what only these jobs need: a cold Go and npm cache,
Docker Hub, GitHub release uploads, Sigstore and GitHub's Sigstore trust root (for `gh
attestation verify`), and `vuln.go.dev`. A missing endpoint fails closed; the fix is a
workflow commit and a new RC ([releasing](operations/releasing.md)). The text logic both
workflows share (tag parsing, choosing the RC, reading the digest record, the `X.Y` rule, the
release notes) is `scripts/release-meta.sh`, tested by `make test-scripts`.

Every workflow sets top-level `permissions: {}` and grants per job; `contents: read` is listed
explicitly wherever a job checks out code.

**Never used:** `pull_request_target` together with a checkout of PR code, and
`workflow_run` jobs that consume PR artifacts with write tokens.

### `ci.yaml` jobs by milestone

| Job | Contents | From |
|---|---|---|
| `pr-title` | Conventional Commit format for the PR title (types: `feat`, `fix`, `perf`, `refactor`, `docs`, `test`, `build`, `ci`, `chore`, `revert`; `!` or `BREAKING CHANGE` for breaking changes); runs only on `pull_request` | v0.0 |
| `changelog` | `feat`, `fix`, `perf`, breaking, and security-fix PRs must add an entry under `## [Unreleased]` in `CHANGELOG.md`, unless labelled `skip-changelog`; also checks the Keep a Changelog structure | v0.0 |
| `lint` | `golangci-lint` (with depguard/forbidigo rules for the layering and no-`os.Getenv`), `eslint`, `tsc --noEmit`, file-size check (300 warn / 500 fail), `actionlint`, `shellcheck`, `yamllint` | v0.0 |
| `test-go` | `go test -race ./...` and the build; short fuzz runs (`make fuzz`, 30 s per target) | v0.0 |
| `test-web` | Vitest for `web/src/core` and `packages/plugin-api` | v0.0 |
| `scenarios` | The failure-scenario suite (fault injection, real temp dirs); enabled with the first `internal/store` package | v0.0 |
| `vuln` | `govulncheck`, `npm audit --omit=dev` | v0.0 |
| `e2e` | Playwright smoke tests against the built binary | v0.1 |
| `image` | Multi-arch build (`linux/amd64`, `linux/arm64`) with buildx; Trivy (threshold per ADR-0015: no critical, no high with a fix; `make image-scan`), run from its image pinned by digest (`TRIVY_IMAGE` in the Makefile), which is bumped only after its cosign signature verifies against aquasecurity/trivy's release workflow; size check (< 40 MB); `docker run` smoke test (`make image-smoke`), including the duplicate-writer refusal, the first note saved within 120 s of `docker run`, and adopting an existing notes folder, plain or already a git repository; **not pushed** | v0.1 |
| `image-arm64` | The same `make image-smoke` and `make image-scan` on the native arm64 image, on GitHub's hosted arm64 runner (`ubuntu-24.04-arm`, a GitHub-hosted label, free for public repositories), so arm64 is run, not only built | v0.1 |
| `perf` | Performance budgets (PRD procedure): base and head are measured **in the same job** on the same runner, interleaved, and the job fails on a > 10% regression of the median (a v0.0 spike measures runner noise first) | v0.2 |
| `backup` | `backup --verify` from v0.2; full backup → restore round-trip from v0.3 (when `restore` ships) | v0.2 |
| `helm` | `helm lint`, `kubeconform`, kind install with an RWOP PVC, N-1 upgrade | v0.3 |
| `plugins` | Sandbox negative tests, plugin-API compatibility check (`api-extractor`) | v0.4 |
| `required` | The only required status check. Defined with `if: always()` and `needs:` on every job above, so it **runs even when a dependency fails or is cancelled**. It passes only if every needed job's result is `success`, or `skipped` **and** listed in an explicit `allowed-skips` input (for example `pr-title` on `push`). Any `failure`, `cancelled` or unexpected `skipped` fails it. This follows the documented GitHub pattern for aggregating required checks. It is never made `if: !cancelled()`: a skipped required check counts as passing, so cancelling the only run would allow a merge without CI. Only a new push cancels an in-progress PR run; label and title edits queue, so a cancelled run's failing `required` never lands on the head commit. | v0.0 |

**Caching:** Go module and build cache, npm cache, buildx GHA cache. The caches are scoped per
branch, so fork PRs can't poison `main`'s cache.

## 4. Images and tags

| Tag | Where | Meaning |
|---|---|---|
| `sha-<short>`, `main` | GHCR | Candidate from `main`: for testers until the first release candidate, never for production |
| `X.Y.Z-rc.N` | GHCR | Release candidate |
| `X.Y.Z`, `X.Y` | **Docker Hub** + GHCR | Promoted release |
| `X` (major) | **Docker Hub** + GHCR | **From 1.0 only.** Before 1.0 every minor may break, so docs use `:0.Y`. |
| `latest` | — | **Not published before 1.0.** |

- **Image names:** `docker.io/joaoheytor/postelmd` (canonical)
  and `ghcr.io/joaoheytor/postelmd` (mirror).
- **What each workflow writes where.** The image is pushed and copied **by digest**, so the
  digest is identical in both registries. Signatures use `cosign sign --recursive` (the index
  and every platform manifest). Build provenance (`actions/attest-build-provenance`, a wrapper
  over `actions/attest`) is attested on the index digest, and a Syft SPDX SBOM on **each
  platform manifest's digest**. Attestations go to the registry (`push-to-registry`) and to
  GitHub's attestation API.

  | Workflow | Writes | Certificate identity |
  |---|---|---|
  | `candidate.yaml` | GHCR: signature, provenance, per-platform SBOMs; `:sha-<short>`, `:main` last | `…/candidate.yaml@refs/heads/main` |
  | `prerelease.yaml` | GHCR: the RC untagged by digest; signature, provenance, per-platform SBOMs; `:X.Y.Z-rc.N` last | `…/prerelease.yaml@refs/tags/vX.Y.Z-rc.N` |
  | `release.yaml` | Both registries: a release signature. Docker Hub: the same index, copied by digest, plus the RC's bundles **copied verbatim**. Stable tags last. | `…/release.yaml@refs/tags/vX.Y.Z` (signature only) |

- **Signatures are made at each address.** `cosign copy` doesn't carry cosign v3 bundles.
- **Provenance and SBOMs are made once, by the builder, and copied.** Build provenance must
  name the workflow that built the image. `release.yaml` builds nothing, so it never creates
  build provenance. Bundles are bound to the digest, not to an address.
- **What `release.yaml` copies to Docker Hub.** It reads the referrers of the index **and of
  every platform digest**, keeps only those with `artifactType`
  `application/vnd.dev.sigstore.bundle.v0.3+json` whose bundle verifies against the exact RC
  identity, and copies each manifest by digest with `crane`. It never copies the referrers
  index blindly. Without that copy, `gh attestation verify` would still pass: the GitHub API
  is keyed by digest alone. But cosign, `gh … --bundle-from-oci` and Kyverno read the
  registry.
- **Referrer storage.** Docker Hub serves the OCI 1.1 referrers API. GHCR uses the referrers
  **tag schema** (an index tagged `sha256-<digest>`), where every write is a read-modify-write
  of one tag. So every job that writes GHCR referrers (the `candidate`, `prerelease` and
  `release` publish jobs) shares the concurrency group `ghcr-referrers`, with
  `cancel-in-progress: false`, and signs and attests sequentially. The group is shared
  because a candidate and an RC of the same commit can share a digest, and with it the same
  referrers tag. A group keeps only one *pending* run, so a later push can cancel a queued
  `prerelease` or `release` run. A cancelled run is simply re-run, which is safe because
  every step is idempotent and a re-run redoes them all.
- **The promotion gate pins the exact RC.** The RC digest comes from the RC's GitHub
  pre-release record (written by `prerelease.yaml`), never from the mutable `-rc.N` tag. The
  `verify` job checks that digest with:
  - `--certificate-identity https://github.com/joaoheytor/PostelMD/.github/workflows/prerelease.yaml@refs/tags/vX.Y.Z-rc.N`;
  - `--certificate-github-workflow-sha <tagged commit>` (for `gh`: `--source-ref` and
    `--source-digest`).

  The RC's own gate results (smoke tests, Trivy, SBOMs) are what its pre-release record
  stands for: `prerelease.yaml` writes the record only after the gates passed and the image
  was signed and attested. `verify` then **re-runs** the vulnerability thresholds (Trivy on
  both platform digests, `govulncheck` on the tagged source). This is deliberate: a CVE
  published between the RC and the release blocks the release, and the fix is a new RC.
- **Order, re-runs and rollback.** `publish` runs these steps in order:
  1. **GHCR:** sign, then verify.
  2. **Docker Hub:** copy the index untagged and check the digest is equal; copy the
     verified bundles; sign.
  3. **Both addresses, exactly:** verify with `cosign verify` (release identity),
     `cosign verify-attestation` for provenance and per-platform SBOMs (the RC identity), and
     `gh attestation verify --bundle-from-oci`.
  4. **Tags:** apply `X.Y.Z`. Move `X.Y` only if `X.Y.Z` is the highest `X.Y.*` release.
  5. **GitHub Release:** the RC's binaries and per-platform SBOMs, downloaded from the
     pre-release and each verified (`gh attestation verify`) against the exact RC identity
     and commit; a `SHA256SUMS` of them, signed with `cosign sign-blob --bundle` under the
     release identity; notes from `CHANGELOG.md`. The binaries report the RC's version
     (`X.Y.Z-rc.N`): they are the bytes that were tested, not a rebuild.

  Every step is idempotent. A re-run redoes **every** step and never skips one because it
  "looks done": one signal, such as a tag already present, proves nothing (the same rule as
  CLAUDE.md rule 14). A failed release is re-run, never patched by hand. **Rollback:**
  re-point `X.Y` at both registries to the previous release's digest, then ship a new patch.
- **Stable aliases** (`X.Y`, and `X` from 1.0) are moved only by `release.yaml`, only for a
  stable tag, and only after both verifications pass. RC tags never move an alias.
- **User verification** and the spike's evidence: [verify-images](operations/verify-images.md).
  Running a candidate or a release (backups, logs, upgrade and rollback):
  [running v0.1](operations/running-v0.1.md).
- **Docker Hub description:** updated by hand for now. Syncing it automatically needs a token
  with *Read, Write, Delete* scope, broader than the push token; revisit later with a separate
  token.

### Candidates (`candidate.yaml`)

- `build` (also on PRs that touch the image build; no secrets) builds the binaries once,
  pushes the image by digest to a registry service on the runner (`make image-push`), and
  gates each platform digest (`make image-gate`: smoke tests and Trivy on amd64, Trivy on
  arm64, a Syft SBOM of each). `publish` (push to `main` only) builds no code: it pushes the
  same binaries to GHCR, fails unless the digest matches the gated one, signs and attests by
  digest, then tags. `:main` moves only if the commit is still `main`'s head.
- `publish` doesn't wait for `ci.yaml`; the merge was already gated, and candidates are not
  for users.
- A same-repo PR could edit the workflow, so verifiers pin the identity to `main`:
  `--certificate-identity https://github.com/joaoheytor/PostelMD/.github/workflows/candidate.yaml@refs/heads/main`.
- SBOMs are attested per **platform** digest:
  `gh attestation verify oci://ghcr.io/joaoheytor/postelmd@<amd64-digest> -R joaoheytor/PostelMD --predicate-type https://spdx.dev/Document/v2.3`.
- Untagged digests from failed runs may accumulate in the package; prune them by hand.

### Verifying (for users and for the homelab)

An image is **released** when it passes both the release signature and the provenance check.
This one-line form checks the signature only; the full commands (provenance, per-platform
SBOMs, RCs, candidates, Kyverno) are in [verify-images](operations/verify-images.md).

```sh
cosign verify docker.io/joaoheytor/postelmd@sha256:<digest> \
  --certificate-identity-regexp '^https://github\.com/joaoheytor/PostelMD/\.github/workflows/release\.yaml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

The regexp accepts any release; to check one version, use `--certificate-identity` with
`…/release.yaml@refs/tags/v0.1.0`.

## 5. Release flow

```mermaid
flowchart LR
  PR[PR] -->|ci required + DCO| M[main]
  M -->|candidate.yaml| C[GHCR :sha / :main<br/>signed candidate]
  M -->|maintainer signs tag vX.Y.Z-rc.N| RC[prerelease.yaml<br/>build once, gate, GHCR rc]
  RC -->|homelab dev check (v0.3+)| OK{OK?}
  OK -->|maintainer signs tag vX.Y.Z<br/>same commit| R[release.yaml<br/>promote rc digest + bundles<br/>re-sign at both registries<br/>environment approval]
  R --> DH[Docker Hub :X.Y.Z :X.Y]
  R --> GH[GHCR mirror]
  R --> GR[GitHub Release<br/>binaries, SBOM, checksums, chart]
```

- Tag classes:
  - `vX.Y.Z-rc.N` → `prerelease.yaml`: GHCR only, a GitHub pre-release, no aliases;
  - `vX.Y.Z` → `release.yaml`: must point at a commit that has a passing RC; the `release`
    environment approval; promotes the RC digest to Docker Hub + GHCR; aliases.

  Both workflows re-parse the tag with a SemVer parser and fail on a mismatch (defence in depth
  behind the trigger filter).
- Versioning follows ADR-0015 and GOVERNANCE.md (SemVer; candidates vs releases;
  vulnerability thresholds; a VEX file for accepted exceptions).
- **Changelog ([Keep a Changelog](https://keepachangelog.com/en/1.1.0/)):** entries are written
  in each PR under `## [Unreleased]`. A release PR moves them into `## [X.Y.Z] - YYYY-MM-DD`,
  with breaking changes and `Security` entries first. `release.yaml` **extracts that section**
  as the GitHub Release notes and fails if it's missing. The changelog is human-curated; commit
  messages help draft it (optionally with `git-cliff`) but don't replace it.

## 6. Secrets inventory

| Secret | Scope | Used by | Rotation |
|---|---|---|---|
| `GITHUB_TOKEN` | Automatic, per job | All workflows (read-only by default) | Automatic |
| GitHub OIDC token | `id-token: write` jobs only | cosign keyless, provenance | Per run |
| `DOCKERHUB_USERNAME` / `DOCKERHUB_TOKEN` | Environment `release` only | `release.yaml` push to Docker Hub | Every 6 months, or immediately if exposed. Use a Docker Hub **access token** limited to read/write on public repositories, never the account password. `DOCKERHUB_USERNAME` is a non-secret variable. |

There are no other long-lived secrets. Signing is keyless, so no signing keys exist.

## 7. Homelab hand-off (v0.3)
The CD side lives in the **`homelab` repository**, following its existing patterns:
- **Harbor:** a proxy-cache project for `docker.io` (and/or `ghcr.io`). Nodes pull
  `harbor.<domain>/dockerhub-proxy/joaoheytor/postelmd@sha256:…`. Pulling through the cache
  also avoids Docker Hub rate limits in the cluster.
- **Argo CD:**
  - an Application in the App-of-Apps; until the OCI chart exists, the in-repo chart
    pattern;
  - a sync wave after Sealed Secrets and cert-manager;
  - values pin the image **by digest**.
- **Promotion:** Renovate in the `homelab` repo opens a PR when a new release digest appears.
  The maintainer merges it → Argo CD syncs. Candidates (rc) can go to a `postelmd-dev`
  namespace the same way. There is no image-updater write-back; every change is a reviewable
  PR.
- **Optional:** a Kyverno `verifyImages` policy enforcing both identities from §4: the
  release signature and the `prerelease.yaml` provenance ([verify-images](operations/verify-images.md)).
- **The homelab ARC runners never build PostelMD.** The homelab only consumes signed releases.

## 8. Registry decisions and costs
- **Docker Hub namespace:** `joaoheytor/postelmd` (decided 2026-11-09). If an organization is
  created later, the move is a new image name plus a deprecation notice; old tags stay
  available.
- **Docker-Sponsored Open Source:** not now; revisit after the first public releases if
  anonymous users hit pull limits.
- **GHCR mirror:** kept. Public packages on GHCR have no storage or bandwidth cost.
- **Expected cost: none.**
  - GitHub-hosted runners, CodeQL, secret scanning and private vulnerability reporting are free
    for public repositories.
  - Public GHCR packages are free.
  - A personal Docker Hub account hosts public repositories on its free tier (subject to Docker's
    pull-rate limits, which Docker has changed several times; check the current pricing page
    before relying on numbers).
  - Sigstore (Fulcio/Rekor) keyless signing is a free public service.
  - The only paid items would be optional: a domain, a Docker paid plan or organization, or
    larger GitHub runners (not needed).
