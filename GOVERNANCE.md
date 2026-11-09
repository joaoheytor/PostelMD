# Governance

## Model

PostelMD is maintained by its founder, acting as **BDFL** (benevolent dictator for life).
The maintainer owns the roadmap, accepts or rejects ADRs, and cuts releases. Additional
maintainers may be added as the project grows; this document will then define how they vote.

## How decisions are made

- **Design decisions** are made through ADRs in [`docs/adr/`](docs/adr/), proposed by PR and
  accepted by the maintainer.
- **Scope:** the PRD's [Non-Goals](docs/PRD.md#4-non-goals) are binding. Changing one requires
  an ADR. The maintainer reviews scope quarterly against the Non-Goals.
- **Extras belong in plugins.** Features serving a narrow audience should be plugins rather
  than core.

## Versioning and stability

- The application follows [SemVer 2.0.0](https://semver.org/). Before 1.0, a **breaking
  change bumps the minor version** (0.3 → 0.4), and features and fixes bump the patch
  version. From 1.0, standard SemVer applies: breaking → major, feature → minor, fix → patch.
  Upgrades between consecutive minor versions are tested.
- Commits on `main` follow [Conventional Commits 1.0.0](https://www.conventionalcommits.org/)
  (squash merges; the PR title is checked in CI). The type of each change decides the version
  bump above.
- [`CHANGELOG.md`](CHANGELOG.md) follows [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/).
  It is the source of release notes.
- `@postelmd/plugin-api` has its **own** SemVer:
  - breaking changes only in a major version;
  - deprecations are announced with runtime warnings at least 2 minor releases or 6 months
    ahead;
  - plugins declare `apiVersion` and `minAppVersion`.

## Releases

A release is a signed tag promoted from a candidate build that passed **the gates defined for
its milestone** in [`docs/roadmap.md`](docs/roadmap.md#release-gates-by-milestone). A gate for
an artifact that doesn't exist yet (for example the Helm chart before v0.3) does not apply.
Once a gate applies, it never becomes optional again. Promotion rules are in ADR-0015. Release
notes list breaking changes, security fixes and upgrade steps.

## Code of Conduct enforcement

Conduct reports go to the conduct contact named in
[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md), not to the security channel. If a report involves
the maintainer, it goes to GitHub's abuse reporting until a second maintainer can act as the
alternate contact.
