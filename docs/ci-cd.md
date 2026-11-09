# PostelMD: CI/CD

- **Status:** Draft v0.1
- **Last updated:** 2026-11-09
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
| Ruleset on `main` | PRs required (no direct pushes, including the maintainer); required checks: `required` (from the GitHub Actions app; it's the **job** name) and `DCO`; linear history; no force pushes or deletions; conversation resolution required |
| Ruleset on tags `v*` | Only the maintainer may create, update or delete them. GitHub rulesets can't require a *signed tag object* (and tag-name patterns may not be available on a personal repo), so the workflows enforce it: they run `git tag -v` against an allowed-signers file in the repo, check the tag name is SemVer (`vX.Y.Z` or `vX.Y.Z-rc.N`), and check the tagged commit is on `main` (`git merge-base --is-ancestor`). |
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
| `candidate.yaml` (from v0.1) | `push` to `main` | Builds and publishes **candidate** images (`:sha-<short>`, `:main`), signed, to GHCR only | `contents: read`, `packages: write`, `id-token: write` |
| `prerelease.yaml` (from v0.1) | `push` of tags matching `v[0-9]+.[0-9]+.[0-9]+-rc.[0-9]+` | **The only workflow that builds a release image.** Test jobs (no secrets, `contents: read`) build and gate; a publish job pushes the digest **untagged** to GHCR, signs and attests it, then applies `:X.Y.Z-rc.N` and creates a GitHub *pre-release*. Never touches Docker Hub or the stable tags; no `release` environment. | test jobs: `contents: read`; publish job: `contents: write`, `packages: write`, `id-token: write`, `attestations: write` |
| `release.yaml` (from v0.1) | `push` of tags matching `v[0-9]+.[0-9]+.[0-9]+` (stable only; RC tags don't match) | **Promotes, never rebuilds.** A `verify` job (no secrets) finds the latest `vX.Y.Z-rc.N` on the same commit, checks the RC's signature, attestations and gate results, and checks the vulnerability thresholds again. A `publish` job (environment `release`) then signs the same digest with the release identity, publishes it to Docker Hub and GHCR, verifies both addresses, moves the stable tags, and creates the GitHub Release (binaries, SBOM, checksums; chart from v0.3). | `verify`: `contents: read`; `publish`: `contents: write`, `packages: write`, `id-token: write`, `attestations: write` |

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
| `image` | Multi-arch build (`linux/amd64`, `linux/arm64`) with buildx; Trivy (threshold per ADR-0015); size check (< 40 MB); `docker run` smoke test, including the duplicate-writer refusal; **not pushed** | v0.1 |
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
| `sha-<short>`, `main` | GHCR | Candidate from `main` (not for users) |
| `X.Y.Z-rc.N` | GHCR | Release candidate |
| `X.Y.Z`, `X.Y` | **Docker Hub** + GHCR | Promoted release |
| `X` (major) | **Docker Hub** + GHCR | **From 1.0 only.** Before 1.0 every minor may break, so docs use `:0.Y`. |
| `latest` | — | **Not published before 1.0.** |

- **Image names:** `docker.io/joaoheytor/postelmd` (canonical)
  and `ghcr.io/joaoheytor/postelmd` (mirror).
- **Push by digest, sign each address:** the image is pushed by digest to GHCR (RC) and later
  copied by digest to Docker Hub (release). Signatures and attestations live next to the image
  **in each registry**, and copy tools may not carry Sigstore bundles stored as OCI referrers.
  So `release.yaml` **signs and attests at each registry address** (cosign `sign` and
  `attest-build-provenance` with `push-to-registry` for both). Whether `cosign copy` can
  replace that is a v0.1 spike.
- **Verify both addresses before the release finishes:** the job runs `cosign verify` and
  `cosign verify-attestation` against **both** `docker.io/joaoheytor/postelmd@<digest>` and
  `ghcr.io/joaoheytor/postelmd@<digest>`. Either failure fails the release, before the stable
  tags are moved.
- **Stable aliases** (`X.Y`, and `X` from 1.0) are moved only by `release.yaml`, only for a stable tag, and
  only after both verifications pass. RC tags never move an alias.
- **Signing and attestations** (keyless cosign via GitHub OIDC) are attached by digest, so they
  cover both registries: a cosign signature, a Syft SBOM (SPDX) attestation and SLSA build
  provenance (`actions/attest-build-provenance`).
- **Docker Hub description:** updated by hand for now. Syncing it automatically needs a token
  with *Read, Write, Delete* scope, broader than the push token; revisit later with a separate
  token.

### Verifying (for users and for the homelab)

```sh
cosign verify docker.io/joaoheytor/postelmd:0.1.0 \
  --certificate-identity-regexp '^https://github\.com/joaoheytor/PostelMD/\.github/workflows/release\.yaml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

## 5. Release flow

```mermaid
flowchart LR
  PR[PR] -->|ci required + DCO| M[main]
  M -->|candidate.yaml| C[GHCR :sha / :main<br/>signed candidate]
  M -->|maintainer signs tag vX.Y.Z-rc.N| RC[prerelease.yaml<br/>build once, gate, GHCR rc]
  RC -->|homelab dev check (v0.3+)| OK{OK?}
  OK -->|maintainer signs tag vX.Y.Z<br/>same commit| R[release.yaml<br/>promote rc digest<br/>environment approval]
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
- **Optional:** a Kyverno `verifyImages` policy enforcing the cosign identity from §4.
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
