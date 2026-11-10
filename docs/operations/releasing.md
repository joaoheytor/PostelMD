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
  (ci-cd §2 and §6).
- The GHCR `postelmd` package is public and linked to the repository.

## 1. Release candidate

1. Pick a `main` commit whose `ci` and `candidate` runs passed.
2. Sign and push the tag:

   ```sh
   git switch main && git pull --ff-only
   git tag -s v0.1.0-rc.1 -m "v0.1.0-rc.1"
   git push origin v0.1.0-rc.1
   ```

3. Watch `prerelease` (Actions tab). When it passes, the
   [pre-release](https://github.com/joaoheytor/PostelMD/releases) `v0.1.0-rc.1` exists,
   lists the index digest (its `postelmd-image-digest:` line, which `release.yaml` reads:
   don't edit it), and carries the binaries, their SBOMs and `SHA256SUMS`.
4. Test the RC by digest (`ghcr.io/joaoheytor/postelmd@sha256:…`, or `:0.1.0-rc.1`), and
   check it as in [verify-images](verify-images.md#release-candidates-and-candidates).

Found a problem? Fix it on `main` and tag `v0.1.0-rc.2`. RC numbers only go up; a tag is
never moved or reused.

## 2. Release

1. Merge a PR that moves `## [Unreleased]` in `CHANGELOG.md` into `## [0.1.0] - YYYY-MM-DD`
   (the `changelog` skill). The release notes are that section; `release.yaml` fails without
   it. **This PR changes the commit**, so tag a new RC on it (step 1) before the release:
   the release must be on the same commit as its RC.
2. Sign and push the release tag on **the same commit** as the RC you tested:

   ```sh
   git tag -s v0.1.0 -m "v0.1.0" 'v0.1.0-rc.2^{commit}'
   git push origin v0.1.0
   ```

   `release.yaml` promotes the highest `v0.1.0-rc.N` on that commit.
3. The `verify` job runs without secrets: tag signatures, the RC's signature, provenance and
   SBOMs, Trivy and `govulncheck` again. A vulnerability published since the RC fails it
   here, on purpose: fix it and cut a new RC.
4. Approve the `release` environment when `publish` asks for it. It signs at GHCR, copies to
   Docker Hub and signs there, verifies both addresses, applies `0.1.0` and moves `0.1`,
   then creates the GitHub Release.
5. Verify it yourself as in [verify-images](verify-images.md): both registries, by digest,
   and a downloaded binary.

## When a run fails

- **Re-run the whole workflow** ("Re-run all jobs"). Every step is idempotent and a re-run
  redoes all of them; a cancelled run (another `ghcr-referrers` job can cancel a queued one)
  is re-run the same way. Never finish a release by hand: no manual `crane tag`, `cosign
  sign` or release edits.
- **A failure a re-run can't fix** (a workflow bug, an endpoint missing from harden-runner's
  `allowed-endpoints`, a new vulnerability): fix it in a PR on `main`, then tag a new RC on
  the new commit and release that. This is what RCs are for: the workflow that publishes a
  release is always one that already built its RC.
- **`prerelease` refuses because the pre-release already records another digest:** the
  same tag built different bytes. Tag the next RC instead.
- **Docker Hub login fails:** check the `release` environment's `DOCKERHUB_TOKEN` (an access
  token with read/write on public repositories) and `DOCKERHUB_USERNAME`, then re-run.

## Rollback

Re-point `0.1` at both registries to the previous release's digest, then ship a fixed patch
release (ci-cd §4). Users pinned by digest are unaffected; the
[runbook](running-v0.1.md#upgrade-and-rollback) shows them how to go back.
