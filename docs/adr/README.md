# Architecture Decision Records

Format: a lightweight [MADR](https://adr.github.io/madr/). Copy `0000-template.md`, take the
next number, and open a PR. Statuses: `Proposed` → `Accepted` → (`Deprecated` |
`Superseded by ADR-XXXX`).

**Editing rules**
- **Proposed** ADRs may be edited freely.
- An ADR becomes **Accepted** when the first code that depends on it is merged, and the
  maintainer flips the status in that PR.
- An **Accepted** ADR's *Decision* section is immutable; change the decision by superseding it
  with a new ADR.
- Accepted ADRs may still receive **append-only** entries in a dated `## Clarifications`
  section: dependency rationale (see CLAUDE.md rule 5), links and typo fixes. A clarification
  must never change the decision.

ADR-0012 (license) and ADR-0013 (name) are **Accepted**: the first commit depends on them.
All others are **Proposed** until the code that depends on them is merged.

**MVP:** ADR-0020 defines v0.1 as a subset of this design. Each ADR it narrows has a
"MVP scope" note under its status line.

| ADR | Title | Status |
|---|---|---|
| [0001](0001-backend-language.md) | Go backend, TypeScript + CodeMirror 6 frontend | Proposed |
| [0002](0002-storage-files-as-source-of-truth.md) | Files are the source of truth; SQLite for derived data and identity | Proposed |
| [0003](0003-save-protocol.md) | Save protocol: identity, conditional writes, atomic rename | Proposed |
| [0004](0004-change-propagation.md) | Change propagation: watcher + reconciler + SSE | Proposed |
| [0005](0005-auth-providers.md) | Pluggable auth providers, local accounts by default | Proposed |
| [0006](0006-authorization-and-confinement.md) | Vaults, memberships, confinement and reserved paths | Proposed |
| [0007](0007-markdown-and-rendering.md) | Markdown dialect and client-side rendering | Accepted (Mermaid: see 0021) |
| [0008](0008-plugin-architecture.md) | Plugin architecture: two trust tiers, per-plugin isolation, versioned API | Proposed |
| [0009](0009-zero-infra-distribution.md) | Zero-infra contract: one image, one volume, secure first run | Accepted |
| [0010](0010-git-vaults-and-secret-detection.md) | Git-backed vaults and secret detection in core | Proposed |
| [0011](0011-no-executable-blocks.md) | No server-side execution of note content | Proposed |
| [0012](0012-license-apache-2.md) | License: Apache-2.0 with DCO | Accepted |
| [0013](0013-project-name.md) | Project name: PostelMD | Accepted |
| [0014](0014-kubernetes-topology.md) | Single writer: Kubernetes topology and instance lock | Proposed |
| [0015](0015-supply-chain-and-ci.md) | Supply chain, dependencies and CI | Accepted |
| [0016](0016-git-repository-state-and-recovery.md) | Git repository state model and recovery | Proposed |
| [0017](0017-backup-and-restore.md) | Backup and restore consistency | Proposed |
| [0018](0018-account-and-session-lifecycle.md) | Account, session and permission lifecycle | Proposed |
| [0019](0019-ui-shell-framework.md) | UI shell framework: Lit, light DOM, editor in a shadow root | Accepted |
| [0020](0020-mvp-scope.md) | MVP scope: one user, one vault, no database | Accepted (trash: see 0022) |
| [0021](0021-mermaid-in-a-sandboxed-frame.md) | Mermaid renders in a sandboxed frame, shown as images | Accepted |
| [0022](0022-mvp-trash-layout.md) | The MVP trash keeps each item's name in a folder of its own | Accepted |
