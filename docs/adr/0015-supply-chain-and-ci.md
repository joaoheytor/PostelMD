# ADR-0015: Supply chain, dependencies and CI

- **Status:** Proposed
- **Date:** 2026-11-09

## Context
The project will be public and plugin-extensible, and runs inside the maintainer's cluster.
FrankMD hand-vendored JS dependencies and ran a manual "pre-release audit" that kept finding
regressions. The maintainer's homelab has self-hosted ARC runners with cluster access.

## Decision
- **Dependencies**
  - No new dependency without an ADR line explaining why.
  - Go modules + `govulncheck`. npm with a lockfile; `npm ci --ignore-scripts`; no hand
    vendoring.
  - Renovate covers Go, npm, GitHub Actions (SHA-pinned) and base image digests. Patch updates
    auto-merge when green, after a minimum release age. Renovate commits carry a
    `Signed-off-by:` trailer (DCO), use the `build(deps)` type, and get the
    `skip-changelog` label unless they fix a vulnerability.
- **CI (GitHub Actions)**
  - Public repo: **GitHub-hosted runners only.** Never run fork PRs on the self-hosted ARC
    runners, and never use `pull_request_target` with a checkout of PR code.
  - Least-privilege `permissions:` on every workflow; the `harden-runner` action.
  - Stages:
    1. lint (golangci-lint, eslint, file-size check);
    2. unit + fuzz (short);
    3. integration;
    4. Playwright smoke tests;
    5. multi-arch build;
    6. Trivy;
    7. Syft SBOM;
    8. cosign keyless signing + build provenance;
    9. push **untagged, by digest** to GHCR;
    10. `docker run` smoke test, kind-based Helm install and upgrade test from N-1, all on
        that digest;
    11. only then apply the tag. Nothing user-visible is tagged before its gates pass.
  - CodeQL on the public repo.
- **Branching: trunk-based.** `main` is always releasable; work happens on short-lived PR
  branches, squash-merged. Releases are tags on `main`. A `release/X.Y` branch is created from
  a tag **only when** a fix must ship for an older minor (mainly security fixes after 1.0);
  fixes land on `main` first and are cherry-picked.
- **Releases:** a release is a tag. CI gates replace a manual pre-release audit. Semver for the
  app; separate semver for the plugin API. **Images** are built with buildx in
  `prerelease.yaml`; GoReleaser builds only the binaries; the Helm chart is packaged with
  `helm package` and pushed as an OCI artifact.
- **Registries:** **Docker Hub is the canonical, user-facing registry**, chosen for
  discoverability. **GHCR is an identical mirror** (same digest), giving limit-free pulls for
  CI and the homelab Harbor cache. Docker Hub credentials are an access token held only by the
  protected `release` environment. Docker-Sponsored Open Source may be requested later if
  anonymous pull limits become a problem.
- **Versioning conventions:** SemVer 2.0.0 (with the 0.x rule in GOVERNANCE.md), Conventional
  Commits 1.0.0 (PR titles, squash merges), Keep a Changelog 1.1.0 (`CHANGELOG.md`, which is
  the source of release notes). All are enforced in CI.
- **Repository settings, workflows, tags and secrets** are specified in `docs/ci-cd.md`.
- **Release promotion (build once, promote the tested digest):**
  - every `main` build publishes an internal candidate (`:sha-<short>`, GHCR);
  - the **maintainer** creates and signs `vX.Y.Z-rc.N` on a `main` commit; `prerelease.yaml`
    builds, gates and publishes that RC image (GHCR), which is what the homelab tests;
  - the maintainer then signs `vX.Y.Z` on **the same commit**. `release.yaml` does **not
    rebuild**: it finds the RC for that commit, verifies its signature and attestations, and
    promotes that exact digest to Docker Hub and GHCR stable tags.

  An RC becomes a **release** only when:
  - every gate for the current milestone passes (gates are per milestone; see the roadmap);
  - there are no **critical** vulnerabilities, and no **high** ones that have a fix available
    (Trivy and govulncheck; exceptions require a VEX statement in the repo);
  - the maintainer signs the tag.

  Releases are re-signed with cosign. Consumers verify the identity
  `https://github.com/joaoheytor/PostelMD/.github/workflows/release.yaml@refs/tags/vX.Y.Z` (exact regexp in `docs/ci-cd.md` §4) with the
  issuer `https://token.actions.githubusercontent.com`. The same applies to binaries (cosign
  blob signatures plus checksums) and the OCI Helm chart.
- **Upgrades and rollback:**
  - SQLite migrations are forward-only and numbered, and each one runs **in a single
    transaction**. The schema version is checked on startup. An older binary refuses to start
    against a newer schema (no implicit downgrade).
  - Before migrating, the app writes an automatic `VACUUM INTO` snapshot to
    `/data/.postelmd/pre-migrate-<from>-<to>.db`. An interrupted upgrade is safe, because each
    migration is atomic and re-running it resumes.
  - **Rollback has two forms, chosen by whether the new version ever accepted writes:**
    - **Database-only rollback** (stop, restore the pre-migration snapshot, start the previous
      version) is allowed **only if the upgraded instance never started post-migration
      mutation**. Eligibility is closed **durably and early**: as the last step of a
      successful migration, **before** startup repair, the reconciler, workers, the admin
      socket or HTTP start, the binary writes a `post_migration_started` marker (in the DB and
      in a sidecar file, both fsynced). Any later database-only rollback refuses. In practice,
      the database-only rollback is for **failed migrations** only. External filesystem
      changes while the app was down don't affect eligibility: the previous version reconciles
      them as it always does.
    - Otherwise, **fix forward** (preferred) or perform a **coordinated full-instance restore**
      from the pre-upgrade backup (ADR-0017), which rolls back files, database and keys
      together. The restored instance starts in **quarantine** (ADR-0017), and admins get an
      **access revalidation** report: the audit events since the backup (membership changes,
      revocations, key rotations), so they can re-apply security decisions that the restore
      undid. The report is built from the **external audit sink** (architecture §9), because
      those events postdate the backup and only survive outside the instance.
    - Both procedures are documented in `docs/operations/`.
  - **Supported upgrade paths:** before 1.0, only consecutive steps (0.x → 0.(x+1)), tested as
    a chain in CI; users pin `:0.x`, so a pull never jumps a breaking minor. From 1.0 on, any
    1.x → a later 1.y directly (skipping minor versions is supported and tested with N-2 → N). Major
    upgrades go through the last minor release of the previous major.
- **Consuming in the homelab:**
  - pin by digest;
  - Harbor proxy-cache of Docker Hub (or GHCR);
  - optionally a Kyverno policy that admits only signed images.

## Consequences
- Positive: provenance end to end; low maintenance burden; safe use of the homelab.
- Negative: more CI minutes; the release pipeline needs setup in v0.0.

## Alternatives considered
| Option | Why not |
|---|---|
| Self-hosted runners for the public repo | Fork PRs could run code inside the homelab cluster |
| Hand-vendored or importmap JS | Version drift and manual re-vendoring (a FrankMD lesson) |
