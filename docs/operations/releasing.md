# Releasing PostelMD

The maintainer's steps for a release candidate and a release. The design is in
[ci-cd §4-§5](../ci-cd.md#4-images-and-tags) and ADR-0015; the workflows are
`.github/workflows/prerelease.yaml` and `release.yaml`. Only the maintainer creates `v*` tags
(tag ruleset); agents never do.

## Before the first release (once)

- `.github/allowed_signers` on `main` holds your SSH signing key; git signs tags with it
  (`git config gpg.format ssh`, `user.signingkey`).
- The `release` environment exists with you as required reviewer, a deployment tag policy
  `v*.*.*`, the `DOCKERHUB_TOKEN` secret and the `DOCKERHUB_USERNAME` variable
  (ci-cd §2 and §6). Leave "Prevent self-review" off: you push the tag and approve the run.
- **The Docker Hub token:** prefer an *organization access token* scoped to the one
  repository `joaoheytor/postelmd` (read/write). A personal account can't scope one: its
  access token with *Read & Write* can push to **every** public repository of the account,
  so create it just before a release and revoke or rotate it after.
- The Docker Hub repository `joaoheytor/postelmd` exists and is public (created by hand, so
  the first push never creates it private).
- The GHCR `postelmd` package is public and linked to the repository.

## 1. Release candidate

1. Pick a `main` commit whose `ci` and `candidate` runs passed. `prerelease.yaml` checks the
   `ci` run itself and fails with "no ci.yaml run … yet" or "still in_progress" if you were
   early: wait for `main`'s CI, then re-run.
2. Sign and push the tag:

   ```sh
   git switch main && git pull --ff-only
   git tag -s v0.1.0-rc.1 -m "v0.1.0-rc.1"
   git push origin v0.1.0-rc.1
   ```

3. Watch `prerelease` (Actions tab). Before it pushes anything, it claims the digest it built
   in a **draft** pre-release. When it passes, the
   [pre-release](https://github.com/joaoheytor/PostelMD/releases) `v0.1.0-rc.1` is published,
   lists the index digest (its `postelmd-image-digest:` line, which `release.yaml` reads:
   don't edit it), and carries the binaries, their SBOMs and `SHA256SUMS`.
4. Test the RC by digest (`ghcr.io/joaoheytor/postelmd@sha256:…`, or `:0.1.0-rc.1`), and
   check it as in [verify-images](verify-images.md#release-candidates-and-candidates).
5. Point the README's `docker run` and the runbook's `IMAGE=`
   ([running-v0.1](running-v0.1.md)) at the new `:0.1.0-rc.N`, in a docs PR.

**Don't merge to `main` while `prerelease` runs.** Its `publish` job shares the
`ghcr-referrers` group with `candidate`: a `candidate` publish queued behind it can cancel the
pending RC publish. Merge again once the pre-release is published.

Found a problem? Fix it on `main` and tag `v0.1.0-rc.2`. RC numbers only go up; a tag is
never moved or reused.

**`v0.1.0-rc.1` is also a shakedown** of the pipeline: it is the first run with egress
blocked and the first `prerelease.yaml` run at all. Expect to fix a missing endpoint or two
and tag `rc.2`. The Docker Hub steps run for the first time only at the final release.

## 2. Release

The order is: **RC → test → CHANGELOG PR → tag `vX.Y.Z` on the tested RC's commit.**

1. Merge a PR that moves the entries under `## [Unreleased]` in `CHANGELOG.md` into
   `## [0.1.0] - YYYY-MM-DD` (the `changelog` skill), keeping an empty `## [Unreleased]`
   heading (`ci.yaml`'s `changelog` check needs it). `release.yaml` reads the release notes
   from `CHANGELOG.md` **on `main`**, so this PR can merge after the RC you tested; no new RC
   is needed.
2. Sign and push the release tag on **the commit of the RC you tested** (not `main`'s head):

   ```sh
   git fetch origin --tags
   git tag -s v0.1.0 -m "v0.1.0" 'v0.1.0-rc.2^{commit}'
   git push origin v0.1.0
   ```

   `release.yaml` promotes the highest `v0.1.0-rc.N` on that commit.
3. The `verify` job runs without secrets: tag signatures, the RC's signature, provenance and
   SBOMs, Trivy and `govulncheck` again, and the CHANGELOG section. A vulnerability published
   since the RC fails it here, on purpose: fix it and cut a new RC (see below: this version is
   burnt).
4. Approve the `release` environment when `publish` asks for it. It signs at GHCR, copies to
   Docker Hub and signs there, verifies both addresses, applies `0.1.0` and moves `0.1` (if
   it is the highest `0.1.*`), then creates the GitHub Release. The release notes say the
   binaries were built and tested as the RC and report the RC's version.
5. Verify it yourself as in [verify-images](verify-images.md): both registries, by digest,
   and a downloaded binary.

**Don't merge to `main` while a release runs.** The `ghcr-referrers` group holds one pending
job: a `candidate` publish queued behind `release` can cancel a pending `release` publish
(and the reverse). A cancelled run is re-run, but it's simpler not to race.

## When a run fails

- **Re-run.** Every step is idempotent and a re-run redoes all of the job's steps; a
  cancelled run is re-run the same way. Prefer **"Re-run failed jobs"** within the 3-day
  artifact retention: `prerelease`'s `publish` then uses the binaries `build` already gated.
  A full re-run rebuilds; it must reach the digest the draft pre-release claims, or it stops
  before pushing or signing anything. Never finish a release by hand: no manual
  `crane tag`, `cosign sign` or release edits.
- **`prerelease` stops because the tag is claimed by another digest:** the same tag built
  different bytes. Tag the next RC instead; leave the claimed draft as the record.
- **A failure a re-run can't fix** (a workflow bug, an endpoint missing from harden-runner's
  `allowed-endpoints`, a new vulnerability): fix it in a PR on `main`, then tag a new RC on
  the new commit. This is what RCs are for: the workflow that publishes a release is always
  one that already built its RC.
- **A failed release burns its version.** The fix is on a new commit, and `vX.Y.Z` can't move:
  - if `publish` never reached step 4 (tags), release `X.Y.(Z+1)` from a new
    `vX.Y.(Z+1)-rc.1`. Deleting the pushed `vX.Y.Z` tag and reusing the number is possible
    (a tag-ruleset bypass), but it is not the default;
  - if tags were set, always move on to the next patch. `X.Y.Z` stays where it was promoted;
    the next release re-points `:X.Y` at both registries.
- **Docker Hub login fails:** check the `release` environment's `DOCKERHUB_TOKEN` and
  `DOCKERHUB_USERNAME`, then re-run.

## Rollback

Re-point `0.1` at both registries to the previous release's digest, then ship a fixed patch
release (ci-cd §4). Users pinned by digest are unaffected; the
[runbook](running-v0.1.md#upgrade-and-rollback) shows them how to go back.
