# PostelMD: Product Requirements Document

- **Status:** Draft v0.1
- **Owner:** project maintainer
- **Last updated:** 2026-11-09

## 1. Vision

A **self-hosted, git-native Markdown workspace for operational knowledge**. DevOps, Platform
and SRE engineers write and maintain their runbooks, ADRs, postmortems and on-call notes in a
fast, safe web editor. The content stays plain Markdown files in git, which their existing
docs-as-code tooling (MkDocs, Backstage TechDocs, Hugo) can publish.

### Positioning

| Product | What it lacks for this audience |
|---|---|
| Obsidian | Single-user and local-first, closed source, no team server |
| Outline / Confluence | Content lives in a database, not plain files in git |
| Backstage TechDocs / MkDocs | Publish-only; no live multi-user editing experience |
| FrankMD | Single-user blogging tool with no git, sync or plugin model (our inspiration) |

## 2. Personas

| Persona | Needs |
|---|---|
| **Platform/SRE engineer (primary)** | Write runbooks and postmortems quickly. Commit to git. Never leak a secret. Link to dashboards. |
| **Team lead / incident commander** | Templates (postmortem, ADR). Find past incidents quickly. Share a vault with the team. |
| **Self-hoster with little infrastructure** | Run with one `docker run`. Secure by default with no IdP or reverse proxy. |
| **Homelab operator (the maintainer)** | Deploy via Argo CD + Helm on K8s. Observability and backups. |
| **Family member or friend** | A simple personal vault. Log in with a passkey. |
| **Plugin author** | A stable, documented, framework-agnostic API with clear permissions. |

## 3. Scope

### MVP (v0.1–v0.3; see `roadmap.md`)

**Editor and workspace**
- File tree: create, rename, move and delete files and folders; nested folders.
- CodeMirror 6 editor with Markdown highlighting, code-fence language highlighting and
  keyboard shortcuts.
- Live preview (split view), sanitized, with Mermaid (lazy-loaded) and copy buttons on code
  blocks.
- **Safe autosave** with conflict detection and a conflict-resolution UI (ADR-0003).
- Local draft recovery (IndexedDB) after a crash or going offline.
- Fuzzy file finder; full-text search (SQLite FTS5).
- Templates: runbook, postmortem, ADR and on-call handoff starter pack, with `{{var}}`
  placeholders.

**Git and safety**
- Each vault is a git repository: batched auto-commits, manual commit message, push/pull to a
  remote, and file history with diffs.
- Secret detection (gitleaks rules) on save and paste. It warns in the editor and **blocks push**
  until the finding is resolved or acknowledged.

**Sync**
- External changes (git pull, vim, Syncthing) show up live: file watcher + reconciler + SSE.

**Media**
- Paste or drag-drop images into the vault (`_assets/`). Local storage first; S3/MinIO later.

**Users and auth**
- Local accounts (argon2id, passkeys, admin invite links). First-run setup token.
- Personal vault per user; shared vaults with owner, editor and viewer roles.

**Operations**
- One image used by `docker run`, Compose and the Helm chart. `/healthz`, `/readyz` and
  `/metrics`. Subcommands (ADR-0009 says which run online, offline or stateless): `serve`,
  `healthcheck`, `version`, `backup`, `restore`, `reindex`, `reset-admin`, `keys rotate` and
  `keys retire`, `vault lock` and `vault unlock`, `plugin disable` and
  `plugin import-revocations`, `db rollback`, `audit verify`.

### Post-MVP (v0.4 → 1.0)
- Plugin API beta (trusted and sandboxed tiers), plugin registry, reference plugins (K8s
  manifest lint, Grafana/Prometheus embeds).
- OIDC (Pocket ID, Authentik, Keycloak, tsidp) and trusted-header auth.
- Signed outbound webhooks (`note.saved`, `note.deleted`).
- Backlinks and `[[wikilinks]]`; YAML/K8s linting in code blocks.
- S3/MinIO media backend; i18n framework (English first).

## 4. Non-Goals

Changing these requires an ADR.

- ❌ Executing code blocks or runbook commands on the server (ADR-0011)
- ❌ Real-time collaborative co-editing (CRDT/OT). Conflicts are detected and resolved instead.
- ❌ WYSIWYG editing. The editor is Markdown source with a live preview.
- ❌ AI image generation, web image scraping (Google, Pinterest), YouTube search
- ❌ More than 2 built-in themes (light and dark) in core; extra themes come from plugins
- ❌ More than 1 UI language before 1.0 (strings stay extractable)
- ❌ Postgres or any external database in v1 (SQLite only)
- ❌ Horizontal scaling or more than 1 replica
- ❌ A native desktop or mobile app before 1.0 (the web UI must work on mobile browsers)

## 5. Non-Functional Requirements

### Performance budgets
| Metric | Budget |
|---|---|
| Typing in the editor (10k-line note, preview open) | Keydown JS processing < 4 ms, **and** keystroke-to-paint no more than 10% above a plain `<textarea>` control on the same runner (editor/control ratio ≤ 1.10). Both per-session p95, aggregated as below. Measured on 10 runners (#33): 0.7–1.0 ms, ratio 0.78–0.90 |
| Autosave | Debounced 1–2 s after the last keystroke, plus on blur/navigation. **Never per keystroke.** |
| Preview render | Off the main thread (Worker), debounced; **skipped when the preview is hidden** |
| Word and character stats | Computed lazily or idle, not on every change |
| Search (10k notes) | < 200 ms p95 |
| Cold start to ready (10k notes, warm index) | < 5 s |
| Idle memory | < 80 MB RSS |
| Image size | < 40 MB compressed |

**Measurement procedure.** Budgets are measured in CI with fixed seeds:
- reference machine: GitHub-hosted `ubuntu-latest` (4 vCPU) for the server; Chromium (the
  Playwright-pinned version) with 4× CPU throttling for the client;
- data: synthetic vault generator, 10k notes, median 4 KB, p99 200 KB, one 10k-line note,
  5 vaults, 5 concurrent users (k6);
- a **sample** is one fresh session; its figure is the p95 over that session's events. Each
  reported figure is the **median over ≥ 30 sessions**, after 2 warm-up rounds, with the
  compared modes alternating. (The p95 of the session p95s was dominated by a few slow
  sessions: the editor/control ratio then varied 0.73–1.67 between runners, against 0.78–0.90
  for the median.);
- browser timings use the Event Timing API. Absolute paint latency in headless CI reflects the
  runner (software rendering), so editor budgets are relative to a control measured in the
  same run.

**Regression gate (from v0.2).** Absolute budgets fail the PR as stated above. Regressions
are measured **paired**: the base commit and the PR are built and benchmarked on the same
runner, alternating sessions, so differences between runners cancel out. The PR fails if its
keydown processing exceeds the base by more than max(15%, 0.3 ms), or its editor/control ratio
exceeds the base by more than 0.10. Server budgets get the same paired rule when the k6
benchmark exists (v0.1).

Why paired, with these tolerances (runner-noise spike, #33, `perf-noise.yaml`): one run landed
on four CPU models (AMD EPYC 9V74, 7763 and 9V45, Intel Xeon 8370C). Between runners, keydown
processing varied with a coefficient of variation (CV) of 10% and the ratio of medians about
5%; within one runner, sessions varied 5–14%. Event Timing reports durations in 8 ms steps,
so a single step is a 12–16% change at these values. A flat 10% rule across runners would
flake.

### Limits
| Limit | Default |
|---|---|
| Note size | 5 MB |
| Request body (notes) | 6 MB |
| Upload size | 25 MB |
| SSE streams per user | 20 |
| Login attempts | 10 per 15 min per IP and per account |
| Per-vault queued work | Bounded per kind (ADR-0003 §6); commit obligations and intents are never dropped |

### Reliability and data safety
- **No lost updates between PostelMD clients.** Every save is atomic and conflict-checked. An
  autosave never recreates a deleted or renamed file.
- **External writers:** safe when coordinated through maintenance mode. **Uncoordinated**
  writes that race with a save can be lost, possibly without detection; ADR-0003 §1 documents
  this, and so do the user docs.
- Recovery point: the last successful save or local draft. The local draft survives a browser
  crash.
- The index is derived data: deleting `app.db`'s index tables and running `reindex` restores it.

### Security baseline
- OWASP ASVS **Level 1** overall, **Level 2** for authentication, session management and access
  control.
- Auth always on: there is no option to disable it (ADR-0005). Strict CSP (`script-src 'self'`). Sanitized preview. Cookies are `HttpOnly`,
  `SameSite=Lax`, and `Secure` on TLS. CSRF protection on. No debug or log endpoints.
- Container runs non-root with a read-only root filesystem. Distroless base image. Signed
  images with an SBOM.
- Threat model maintained in `threat-model.md`.

### Operability
- Structured JSON logs (slog) to stdout. Prometheus metrics. Graceful shutdown that flushes
  pending writes.
- Works on amd64 and arm64.
- Configured by environment variables, with `_FILE` variants for secrets.

### Accessibility and UX
- Keyboard-first: command palette and documented shortcuts. WCAG 2.1 AA contrast in both themes.
- Usable on a phone browser (read, light edits).

## 6. Success metrics (for the maintainer)
- The maintainer's own runbooks and notes live in PostelMD on the homelab by v0.3.
- No data-loss bugs reported. Save conflict and error metrics stay at 0 in normal use.
- A new user goes from `docker run` to their first saved note in under 2 minutes.
- At least 2 reference plugins built only against the public plugin API before 1.0.

## 7. Open questions
- UI shell framework: decided by the v0.0 spike (ADR-0019, Lit).
- Git policy is decided in ADR-0010 and ADR-0016: one repository per vault, a 30 s quiet-window
  auto-commit, and per-author commits.
