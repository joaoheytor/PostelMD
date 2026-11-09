# ADR-0013: Project name: PostelMD

- **Status:** Accepted (2026-11-09, with the repository name in the first commit)
- **Date:** 2026-11-09

## Context
The working codename "MyFranMD" is too easy to confuse with FrankMD. Since this is a
clean-room project, it must have its own identity. Candidates were drawn from deceased
pioneers of computing and operations (Engelbart, Postel, Deming, Goldratt); living people
were excluded to avoid using someone's name without consent.

## Decision
- Name: **PostelMD**, honouring Jon Postel (1943–1998), long-time RFC Editor, who documented
  much of how the internet works. That fits a tool for operational documentation.
- Identifiers:
  - binary `postelmd`;
  - environment variable prefix `POSTELMD_`;
  - state directory `/data/.postelmd/`;
  - metrics prefix `postelmd_`;
  - npm scope `@postelmd`;
  - Helm chart `postelmd`.
- The local project folder and repository are named `PostelMD`.

## Consequences
- Before 1.0, check:
  - GitHub organization and repository;
  - domain;
  - GHCR and Docker Hub;
  - npm scope;
  - trademark search (USPTO, EUIPO, INPI).

  Rename if any of these conflict.
- README credits FrankMD as the inspiration, with no code copied.
