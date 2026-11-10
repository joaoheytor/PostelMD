# ADR-0015: Supply chain, dependencies and CI

- **Status:** Accepted
- **Date:** 2026-11-09

## Context
The project will be public and plugin-extensible, and runs inside the maintainer's cluster.
FrankMD hand-vendored JS dependencies and ran a manual "pre-release audit" that kept finding
regressions. The maintainer's homelab has self-hosted ARC runners with cluster access.

## Decision
- **Dependencies**
  - No new dependency without an ADR line explaining why.
  - **Development-only tooling** (not shipped, so no runtime rationale needed; see the
    dependency register below). `govulncheck` is a Go `tool` dependency in `go.mod`, so local runs and CI use the same
    version. Dev-only advisories are tracked but don't block CI; the gate is what ships
    (`npm audit --omit=dev`, `govulncheck`, Trivy).
  - Go modules + `govulncheck`. npm with a lockfile; `npm ci --ignore-scripts`; no hand
    vendoring.
  - Renovate covers Go, npm, GitHub Actions (SHA-pinned) and base image digests. Patch updates
    auto-merge when green, after a minimum release age. Renovate commits carry a
    `Signed-off-by:` trailer (DCO), use the `build(deps)` type, and get the
    `skip-changelog` label unless they fix a vulnerability.
- **Dependency register** (CLAUDE.md rule 5). Every dependency the design names, with the
  reason the standard library or an existing dependency isn't enough. A PR that adds a
  dependency adds or updates its row; *planned* rows become *in use* in the PR that adds them
  to `go.mod` or `web/package.json`. Transitive dependencies are covered by `govulncheck`,
  `npm audit --omit=dev`, Trivy and Renovate rather than listed.

  | Dependency | Used by | Ships? | Why not the stdlib or an existing dependency | Status |
  |---|---|---|---|---|
  | `golang.org/x/sys` | `store/fs`, `store/lock` | server | `openat2`, `renameat2(RENAME_NOREPLACE)` and `flock` aren't in `syscall`/`os` (ADR-0006 spike) | in use |
  | `go-git/go-git/v5` (+ `go-billy/v5`) | `store/git` | server | Pure-Go git: the distroless image has no `git` binary, and commits must be built from scanned bytes (ADR-0016 spike) | in use |
  | `modernc.org/sqlite` | `store/sql` | server | Pure-Go SQLite with FTS5, keeping `CGO_ENABLED=0` (ADR-0002) | planned v0.1 |
  | `sqlc` | `store/sql` | no (code generator) | Type-checked queries without a runtime ORM | planned v0.1 |
  | gitleaks (`zricethezav/gitleaks/v8`) | `secrets` | server | A maintained, widely used rule set; writing our own detectors would lag it (ADR-0010). Footprint is checked when it is added | planned v0.1 |
  | `go.yaml.in/yaml/v3` | `config` | server | Reads `config.yaml` (ADR-0009); the stdlib has no YAML parser. The YAML organisation's maintained continuation of `gopkg.in/yaml.v3`; strict decoding (`KnownFields`) rejects unknown keys | in use |
  | `golang.org/x/crypto` | `auth` | server | argon2id isn't in the stdlib (ADR-0005). Already a transitive dependency of go-git | planned v0.1 |
  | `go-webauthn/webauthn` | `auth` | server | WebAuthn attestation and assertion parsing is large and security-critical (ADR-0005) | planned v0.1 |
  | `alexedwards/scs` | `auth` | server | Server-side sessions with rotation, idle and absolute timeouts, SQLite store (ADR-0005, ADR-0018) | planned v0.1 |
  | `fsnotify/fsnotify` | `store/watch` | server | The stdlib has no file watching (ADR-0004) | planned v0.2 |
  | `yuin/goldmark` | `index` | server | CommonMark + GFM parsing for link and heading extraction (ADR-0007) | planned v0.2 |
  | `prometheus/client_golang` | metrics | server | The exposition format VictoriaMetrics and Prometheus scrape; histograms done right | planned v0.2 |
  | `coreos/go-oidc` | `auth` (`oidc`) | server | ID-token verification and discovery; hand-rolled JOSE is a known foot-gun (ADR-0005) | planned (OIDC milestone) |
  | `microcosm-cc/bluemonday` | `http` | server | Only if the server ever returns rendered HTML (ADR-0007) | conditional |
  | Extism on `tetratelabs/wazero` | `plugins` | server | Sandboxed server plugins (ADR-0008) | not before v1.x |
  | CodeMirror 6 (`@codemirror/*`) | `web/src/editor` | web | The editor (ADR-0001); FrankMD's hand-built editor was replaced at great cost (lesson 2) | in use |
  | `@lezer/highlight` | `web/src/editor` | web | CodeMirror's own highlighting tags, used to style Markdown tokens; already a dependency of `@codemirror/language` | in use |
  | `markdown-it` | `web/src/preview` (Worker) | web | CommonMark + GFM with source maps for scroll sync (ADR-0007 spike) | in use |
  | `dompurify` | `web/src/preview` | web | Proven HTML sanitizer; runs inside the only Trusted Types policy (ADR-0007) | in use |
  | `lit` | `web/src/ui` | web | Reactive custom elements with no compiler (ADR-0019 spike) | in use |
  | `mermaid` | `web/src/diagram` (sandboxed frame) | web | Diagrams; loaded only when a `mermaid` block exists, in its own frame (ADR-0021) | in use |
  | `gcr.io/distroless/static:nonroot` | image | base image | No shell or package manager in the runtime image (ADR-0009) | planned v0.1 |

  Development, test and release tooling is not shipped and needs no row beyond this list:
  TypeScript, Vite, Vitest, ESLint with `typescript-eslint`, Playwright (and the a11y scanner
  chosen under ADR-0019), k6, golangci-lint, `govulncheck`, actionlint, shellcheck, yamllint,
  the gitleaks CLI (pre-commit), Helm, kind, kubeconform, GoReleaser, buildx, cosign, Syft,
  Trivy, Renovate and `crane` (go-containerregistry). `release.yaml` needs a byte-exact
  copy by digest of the image and of its Sigstore referrer manifests. Neither `cosign copy`
  (it drops v3 bundles) nor buildx provides one (#31).
  - **How crane is pinned:** its image isn't cosign-signed (checked on v0.22.1), so
    workflows use the **release tarball, pinned by SHA-256**. A bump is accepted only after:
    - the tarball matches the release's `checksums.txt`;
    - its SLSA provenance (`multiple.intoto.jsonl`, a Sigstore bundle) verifies with
      `cosign verify-blob-attestation --type slsaprovenance02` against the
      slsa-github-generator `generator_generic_slsa3.yml` identity, with
      `--certificate-github-workflow-repository google/go-containerregistry`.

    v0.22.1 passed both checks on 2026-11-10.
  - **Docker Hub credentials** reach crane only through `crane auth login
    --password-stdin`, never as arguments or in the environment of other steps.
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
    6. Trivy (on the local build);
    7. push **untagged, by digest** to GHCR;
    8. gate on that digest: `docker run` smoke test, Trivy, kind-based Helm install and
       upgrade test from N-1;
    9. Syft SBOM of each platform manifest;
    10. sign (cosign keyless, `--recursive`) and attest (build provenance on the index, an
        SBOM per platform digest), by digest;
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

  **Signatures and attestations at both registries** (spike #31; `docs/ci-cd.md` §4,
  evidence and user commands in `docs/operations/verify-images.md`):
  - **Provenance and SBOMs are attested once, by the builder.** `prerelease.yaml` attests
    them with `actions/attest-build-provenance` / `actions/attest` and `push-to-registry`:
    build provenance on the index digest, a Syft SPDX SBOM on **each platform digest**. They
    go to GHCR and to GitHub's attestation API.
  - **`release.yaml` builds nothing and never creates build provenance.** It copies the RC's
    Sigstore bundles to Docker Hub **verbatim**: the referrers of the index and of every
    platform digest, filtered by `artifactType`, and only those whose bundle verifies
    against the exact RC identity.
  - **The release signature is made at each address**: `cosign sign --recursive` on
    `ghcr.io/…@<digest>`, then on `docker.io/…@<digest>` after the copy.
  - **The gate pins the exact RC.** The RC digest comes from the RC's GitHub pre-release
    record, not the mutable tag. It is checked against the identity
    `…/prerelease.yaml@refs/tags/vX.Y.Z-rc.N` and the tagged commit's workflow SHA. Both
    exact addresses are verified before any stable tag moves; `X.Y` moves only if `X.Y.Z`
    is the highest `X.Y.*`.
  - **GHCR referrer writes are serialized.** GHCR uses the OCI referrers tag schema, so the
    `candidate`, `prerelease` and `release` publish jobs share the concurrency group
    `ghcr-referrers`.
  - **Recovery of a promotion: an idempotent full re-run**, in the order GHCR → Docker Hub →
    GitHub Release. The registries themselves are the record; no intent file is needed.
    Every step is safe to repeat (same digest, verbatim bundles, an extra signature is
    harmless), and a re-run redoes every step instead of inferring progress from one signal.
    CLAUDE.md rule 14's durable-intent requirement covers application mutations, not CI.
    Rollback re-points `X.Y` to the previous release's digest and ships a new patch.
  - **Rejected:**
    - `cosign copy`: it drops v3 bundles and carries only the legacy `.sig`/`.att` tags.
    - Attesting again in `release.yaml`: the provenance would name a workflow that didn't
      build the image.
    - Legacy tag-based signatures for Docker Hub: Docker Hub serves the OCI referrers API.
    - GitHub's attestation API alone: offline and admission-controller verification read
      the registry.
- **Upgrades and rollback:**
  - SQLite migrations are forward-only and numbered, and each one runs **in a single
    transaction**. The schema version is checked on startup. An older binary refuses to start
    against a newer schema (no implicit downgrade).
  - Before migrating, the app writes an automatic `VACUUM INTO` snapshot to
    `/data/.postelmd/pre-migrate-<from>-<to>.db`. An interrupted upgrade is safe, because each
    migration is atomic and re-running it resumes.
  - **Snapshot crash safety.** The migration intent can't live in the database being migrated,
    so it is a sidecar file, `/data/.postelmd/migrate.intent` (`from`, `to`, snapshot path),
    written and `fsync`ed first. The snapshot goes to `<name>.tmp`, is `fsync`ed and verified
    (`PRAGMA integrity_check`, schema version = `from`), then renamed into place and the
    directory `fsync`ed. Migrations start only after that. On startup, with the intent present:

    | Schema version | Snapshot | Action |
    |---|---|---|
    | `from` | Absent (a temp at most) | Not applied: remove the temp, take the snapshot again |
    | `from` | Present | Snapshot done: verify it again, then migrate |
    | Between `from` and `to` | Present | Resume the remaining migrations |
    | `to` | Present | Done: write the `post_migration_started` marker if missing, remove the intent |
    | Anything else | | Interference: refuse to start; the admin uses the runbook |
  - **Database-only rollback** is an offline command (`postelmd db rollback`), run under the
    instance lock, never by hand-copying files:
    1. refuse unless the snapshot passes `PRAGMA integrity_check` and has schema version
       `from`, and **neither** `post_migration_started` marker exists;
    2. write a rollback intent (sidecar, `fsync`ed);
    3. **move** `app.db`, `app.db-wal` and `app.db-shm` into `rollback-<timestamp>/` (never
       delete them). A stale WAL left next to the restored file would be replayed into it and
       corrupt it;
    4. copy the snapshot to `app.db.tmp`, `fsync`, rename it to `app.db`, `fsync` the directory;
    5. remove the intent.

    Re-running the command after a crash continues from the intent: if `app.db` is absent and
    `rollback-<timestamp>/` holds the moved files, it continues at step 4; if `app.db` already
    matches the snapshot's hash, it removes the intent.
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
  - optionally a Kyverno policy that admits only images with both the release signature
    (`release.yaml` identity) and build provenance (`prerelease.yaml` identity).

## Consequences
- Positive: provenance end to end; low maintenance burden; safe use of the homelab.
- Negative: more CI minutes; the release pipeline needs setup in v0.0.

## Alternatives considered
| Option | Why not |
|---|---|
| Self-hosted runners for the public repo | Fork PRs could run code inside the homelab cluster |
| Hand-vendored or importmap JS | Version drift and manual re-vendoring (a FrankMD lesson) |

## Clarifications
- **2026-11-10, npm `overrides`.** An `overrides` entry in `web/package.json` is allowed only
  to lift a vulnerable transitive dependency when no release of its parents accepts the fixed
  version; it pins an exact version and is removed once the parents catch up. The first one is
  `minimatch` 11.0.1, for GHSA-g3hv-75wg-m249 (fixed in 11.0.1): it reaches `web` only through
  ESLint and `typescript-eslint` (dev tooling, never shipped), whose latest releases (eslint
  10.14.0, `@eslint/config-array` 0.23.5, `typescript-eslint` 8.72.2) still require `^10`.
  11.0.1 is the first fixed release and the oldest 11.x apart from 11.0.0 (published
  2026-10-27, two weeks old on 2026-11-10); every later 11.x is younger than the two-week
  minimum release age. `npm run lint` passes with it. `test/perf/app` and `test/perf/editor`
  don't depend on `minimatch`. The override goes when ESLint and `typescript-eslint` accept
  minimatch 11 (#101).
- **2026-11-10, actionlint and shellcheck images.** `make lint` and CI's `lint` job run
  actionlint and shellcheck from their upstream images on Docker Hub, pinned by index digest
  in the Makefile (`ACTIONLINT_IMAGE` = `rhysd/actionlint` 1.7.12, published 2026-03-30;
  `SHELLCHECK_IMAGE` = `koalaman/shellcheck` v0.11.0, published 2025-08-04). CI used to
  install actionlint with `go install` and take the runner's preinstalled, unpinned
  shellcheck, which is older than 0.11.0 and reported an SC2015 that local runs didn't. The
  actionlint image bundles shellcheck 0.11.0, the same as `SHELLCHECK_IMAGE`. Neither image is signed
  upstream (no cosign signature for either digest, checked 2026-11-10), so the controls are
  the digest pin, `--network none` and a read-only mount. A bump moves both together, to releases at least two weeks old.
- **2026-11-10, the release workflows** (`prerelease.yaml`, `release.yaml`). No new
  dependency: they reuse `candidate.yaml`'s actions and tools, plus crane v0.22.1 pinned as
  above.
  - **Binaries follow the image:** built once by `prerelease.yaml`, gated with it, attested
    there (build provenance on the binaries and SBOM files) and attached to the pre-release.
    `release.yaml` verifies each against the exact RC identity and commit, attaches them to
    the release, and signs a `SHA256SUMS` with `cosign sign-blob` under the release identity
    (the "blob signatures plus checksums" above). No GoReleaser until there are archives
    for more platforms.
  - **A release reports its RC's version** (`X.Y.Z-rc.N`), in the binaries and in the SBOMs:
    promotion keeps the tested bytes, so nothing is rebuilt to change the string. The release
    notes and `verify-images.md` say so. SemVer orders `X.Y.Z-rc.N` before `X.Y.Z`, so a
    scanner may treat a release as older than it is; security advisories name the RC
    version too.
  - **One RC tag, one digest:** `prerelease.yaml` claims the digest in a draft pre-release
    before its first push or signature, and a run that builds another digest stops there.
  - **The release re-checks the vulnerability thresholds** (Trivy on both platform digests,
    `govulncheck` on the tagged source) instead of trusting the RC's results alone: a CVE
    published since the RC blocks the release.
  - **No caches in release builds**, and harden-runner **blocks** egress in both workflows,
    with per-job allow-lists (`docs/ci-cd.md` §3).
