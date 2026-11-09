# PostelMD: Roadmap

Milestones are **thin vertical slices**, with one epic in flight at a time. Each milestone has
acceptance criteria that must be demonstrably true, with tests or recorded evidence, before it
closes. Dates are deliberately omitted (FrankMD lesson: every estimate was low; pace yourself).

## v0.0: Foundations
- [x] PRD, architecture, ADRs (Proposed), threat model, CLAUDE.md, governance files
- [x] Design review incorporated (save identity, git secret safety, backup, lifecycle, plugin
      isolation, single writer)
- [x] Name pre-check (2026-11-09): no conflicting GitHub org or user, npm `postelmd` /
      `@postelmd/*`, Docker Hub or Go packages; `.com/.dev/.io/.org` have no DNS records.
      Still to do, each before the first artifact that depends on it:
      - **before the first image (v0.1):** create the Docker Hub repository
        `joaoheytor/postelmd`, and decide whether the repo moves to a GitHub org (moving later
        changes the cosign identity that users pin, ADR-0015);
      - **before the first `@postelmd/plugin-api` publish (v0.4):** register the npm org;
      - **before 1.0:** reserve a domain and run a trademark search.
- [x] Repository settings per `docs/ci-cd.md` §2 (rulesets, Actions permissions, private
      vulnerability reporting, secret scanning, CODEOWNERS, templates). Required checks are
      added once CI has reported on a PR.
- [x] Repo bootstrap: Go module, Vite + TS app, Makefile (`make ci` = local parity), `ci.yaml`
      v0.0 jobs, CodeQL, Scorecard, Renovate
- [ ] **DCO enforcement:** install the DCO GitHub App, make it a required status check on
      `main`, and verify it with one unsigned test PR
- [ ] **Failure-scenario suite (executable, before feature work).** Small Go programs and
      tests that reproduce each contract on real temp directories, with fault injection
      (kill points, `ENOSPC`, concurrent writers):
      - **structure:** crash after a folder rename and before the DB commit, with two identical
        notes inside (IDs must be preserved by the structural intent); crashes at every step of
        create, delete, atomic rename and `link`/`unlink`, each reaching its table's outcome;
        a crash at each phase of a **folder delete** (`moving`, after the `mkdir`, `trashed`, a
        half-done purge); every row of every table, including "appeared externally", and a
        state outside the table landing in interference; `rename-checked` and the delete
        fallback on a filesystem without `RENAME_NOREPLACE`; a crash at each step of a
        **restore from trash**, and a restore racing the purge (exactly one wins);
        a commit message containing a token is rejected before the commit is written;
      - **reconcile:** a file replaced by rename between two scans keeps its ID (absence needs
        two passes); a file really deleted is tombstoned on the second pass;
      - **reserved paths:** a reserved path is never watched, staged or committed, and a pulled
        commit containing one is refused;
      - **startup:** recovery runs before the reconciler, workers and HTTP; the reconciler never
        tombstones or re-IDs a path named by an open or flagged intent, and applies only under
        the vault lock;
      - **drafts:** orphaned drafts (closed tab, tombstoned file) are listed on vault load and
        never deleted without the user's choice;
      - **save:** a crash at every step of a save (each reaching its recovery-table row), a
        lost response retried with the old `If-Match` (`200`, no new generation), the startup
        temp sweep, `EXDEV` and `fsync` errors (no fallback, no retried `fsync`), ABA
        delete+recreate, a stale response vs a newer edit, two offline tab drafts,
        an ambiguous external rename, an external write in the rename window (shows what is
        and isn't detected);
      - **git:** a pull where a blocked file was also changed remotely (kept, copied to
        recovery, listed as a conflict; the same outcome after a crash mid-pull); a crash
        between two per-author commits of one batch (attribution kept); a crash at each step
        of commit and **fast-forward pull** (finish and abort both
        reach a verified state, and bytes matching neither blob go to recovery), plus merge after
        the spike (the intent recovery must never discard), and commit-gate byte binding with a writer racing the stage;
      - **secrets:** first push to an empty remote, a push to a changed remote, an imported
        allow-list that must stay inert;
      - **backup:** a key rotation racing the backup (the barrier must hold), recovery files of
        an open intent included, a restore onto an empty volume that starts in **quarantine**
        (a user **or admin** suspended after the backup can't log in; only the recovery
        identity can); snapshot-only backup refused on a multi-volume layout;
      - **keys:** a crash at each step of rotation and retirement; a key is never deleted
        while a record references it;
      - **upgrade:** a crash at each step of the pre-migrate snapshot and of
        `postelmd db rollback` (a stale WAL is never replayed into the restored file);
        startup repair or a worker running before any request still closes
        database-only rollback eligibility;
      - **outbox:** overflow by kind (commit obligations survive, clients get `resync`);
      - **single writer:** a second process on the same `/data` refuses to start;
      - **power loss:** every save, structural and git crash point above also runs under the
        power-loss model (unsynced data and directory entries dropped; ADR-0003 §5);
      - **release:** an `-rc` tag never publishes to Docker Hub or moves an alias; signatures
        verify from both registries; the `required` check fails when a needed job is
        cancelled or skipped unexpectedly.

      These become permanent regression tests.
- [x] **Spike: go-git capabilities** (results in ADR-0016). Pinned v5.19.3; commits from bytes,
      compare-and-swap refs, fast-forward detection and push-object listing in
      `internal/store/git`; v6 beta has no three-way merge, so v0.1 stays fast-forward only.
- [x] **Spike: confinement primitives** (ADR-0006, results recorded there). `openat2` +
      `renameat2(RENAME_NOREPLACE)` in `internal/store/fs`; 0 escapes under a symlink-swap race
      (with a negative control); fuzzed; 9p lacks `RENAME_NOREPLACE`.
- [x] **Spike: UI shell framework** (ADR-0019): Lit, light DOM; CodeMirror mounted in a shadow root
      so its styles comply with `style-src 'self'` (found in the spike: it breaks otherwise)
- [x] **Spike: CodeMirror 6 + markdown-it in a Worker** (ADR-0007): DOMPurify stays on the main
      thread; changed-block patching, batched initial render and `content-visibility` make typing
      with the preview open cost the same as with it hidden; editor budget made relative to a
      `<textarea>` control

**Acceptance:** CI is green on an empty skeleton; the file-size check fails on a 501-line
fixture; DCO blocks an unsigned commit.

## v0.1: "My runbooks, safely, in one `docker run`"
**Scope:**
- single binary with the embedded SPA and the instance lock;
- local auth (passwords) with first-run setup;
- personal vault;
- file tree CRUD with no-clobber moves;
- CM6 editor;
- live preview (sanitized, with Mermaid);
- the full save protocol (ADR-0003), with conflict UI and drafts;
- fuzzy finder and template starter pack;
- **git-backed vault** (ADR-0010 and ADR-0016: auto-commit, history, push, **fast-forward-only
  pull**);
- **secret detection** with the commit and push gates;
- reserved paths;
- `/healthz`, `/readyz`, `/metrics` (behind a token or on a separate listener; ADR-0009);
- multi-arch image.

**Acceptance criteria**
- The canonical `docker run` (ADR-0009) → setup URL in the logs → admin created → first note
  saved, in < 2 minutes.
- Pre-setup routing matches the ADR-0009 table exactly (integration test per row).
- A second container on the same `/data` refuses to start.
- `/data` on 9p, FUSE or NFS refuses to start (by `statfs` magic); a vault on a filesystem
  without `RENAME_NOREPLACE` works through the fallbacks (CI test with a seccomp profile that
  returns `EINVAL` for it); the Docker Desktop commands in ADR-0009 are checked by hand on
  Windows before each minor release.
- Two tabs edit the same note → the second save gets 412 and the conflict view; nothing is
  lost.
- Deleting a note while an autosave is pending does **not** recreate it. Delete + recreate with
  the same content → the old autosave gets 410/412.
- Moving onto an existing file returns 409, and the destination is unchanged.
- A stale save response never clears newer edits (an e2e test with injected latency).
- Killing the browser mid-edit → the draft is recovered on reopen.
- Pasting an AWS key → editor warning; the file is **not auto-committed**; push is blocked.
  A secret committed outside PostelMD anywhere in reachable history blocks the push,
  including on a first push to an empty remote.
- An allow-list change arriving through a pull is inert until an owner approves it.
- The whole failure-scenario suite from v0.0 passes in CI.
- Path lookup, create, move and upload naming `.git/config` or `.postelmd/*` return 404.
- Security tests: the egress policy denies every special-purpose block (table test, including
  IPv4-mapped, NAT64 and 6to4 forms) and a `file://` or local-path remote is refused before it
  is stored; an SSH host-key mismatch fails closed; an editor can't write
  `.github/workflows/*`; a new secret under an approved path glob is still an open finding; a
  token in a path or branch name blocks the push; repeated failed logins slow down but never
  lock out a passkey login; a credential change without recent re-authentication is refused;
  `/metrics` returns 404 without its token; `postelmd audit verify` detects an edited, removed
  or inserted line in the `file:` sink.
- Restarting the container keeps the sessions (persisted keys) and the notes intact. A
  `kill -9` during a git commit → repaired on startup (intent log test).
- Fuzz tests for path traversal pass. The e2e run produces no CSP violation reports.
- The image runs on amd64 and arm64 and is under 40 MB.
- The first release promotion signs and attests at Docker Hub and GHCR separately, and
  `cosign verify` plus `cosign verify-attestation` pass at **both** addresses by digest before
  any tag moves (the #31 spike showed copies don't carry cosign v3 signatures).
- The PRD performance budgets for a single user are met with the measurement procedure.

## v0.2: Live sync, search, media, sharing, backup
- File watcher + reconciler + SSE (snapshot handoff, replay, per-event authorization).
- SQLite FTS5 search; `reindex`.
- Image paste and drag-drop.
- Invites, multiple users, shared vaults; the account and session lifecycle (ADR-0018); the
  audit log.
- Passkeys.
- `backup` (ADR-0017), `reset-admin`, `vault lock`.

**Acceptance criteria**
- An external `echo >> note.md` shows up in an open browser within 2 s (Linux bind mount), or
  within the reconcile interval (Docker Desktop).
- An external rename keeps the file ID when the content is unchanged.
- A viewer can't write (API-enforced, table tests).
- Removing a member closes their SSE stream within 1 s, and their replay contains no events
  from that vault.
- Suspending a user kills all their sessions. A password reset revokes sessions. Removing the
  last owner returns 409.
- Search across 10k notes < 200 ms p95.
- `postelmd backup --verify` confirms an archive (decrypts it, checks the manifest hashes and
  runs `integrity_check` on the database copy). The full restore ships in v0.3.
- Passkey registration and login work over HTTPS and localhost. Plain HTTP shows the
  insecure-transport banner.

## v0.3: Homelab dogfooding
- Helm chart (OCI, RWOP PVC), Compose example, GoReleaser binaries.
- `restore` command and a CI restore test.
- Argo CD Application in the `homelab` repo; Sealed Secrets; Longhorn; VictoriaMetrics.
- `docs/operations/` runbooks: backup, restore, fencing before force-delete, key compromise,
  upgrade and rollback.

**Acceptance criteria**
- A fresh Argo CD sync brings the app up healthy.
- The monthly restore drill has been done once, recorded, and meets the RTO/RPO in ADR-0014 §5.
- A chart install on kind with an RWOP PVC passes in CI.
- The fencing runbook has been rehearsed: a node is drained and the volume detached before the
  force-delete.
- One week of daily use with zero save errors and zero split-brain in the metrics.
- An N-1 → N upgrade on the same volume is tested in CI, and a rollback via the pre-migrate
  snapshot has been rehearsed.

## v0.4: Plugin API beta
- `@postelmd/plugin-api` 0.x (`App` / `TrustedApp` / `SandboxApp`), plugin host with one
  iframe per plugin, manifests and capabilities, admin approval bound to capabilities, safe
  mode, emergency disable, offline revocation import.
- Mermaid moves to a **first-party bundled plugin, enabled by default**.
- Reference plugins: K8s manifest lint, Grafana/Prometheus embed (an egress exception).

**Acceptance criteria**
- The sandbox negative-test suite passes (ADR-0008):
  - an undeclared `fetch` is blocked;
  - `/api` with cookies fails;
  - foreign IndexedDB is unreachable;
  - an undeclared capability returns `EPERM`.
- A capability change forces re-approval.
- A plugin throwing in `onload` doesn't break boot. `POSTELMD_PLUGINS=off` works.
- The Mermaid preview is identical before and after the move to a plugin (golden test).

## v0.5+: Toward 1.0
- OIDC (Pocket ID, Authentik, Keycloak, tsidp) with explicit identity linking; trusted-header auth (contract per
  ADR-0005).
- Signed outbound webhooks through the egress client.
- Backlinks and wikilinks; S3/MinIO media; i18n framework; plugin registry with cosign
  verification and an online revocation list.
- Accessibility pass.

**Acceptance criteria**
- OIDC login never auto-links by email (test).
- A spoofed identity header from an untrusted peer is rejected (test).
- A webhook to a private IP is blocked unless an exception exists (test).
- axe-core reports no serious or critical violations on the main views, and both themes meet
  WCAG 2.1 AA contrast.

## Release gates by milestone
A gate applies from the milestone where its artifact first exists, and never stops applying
afterwards (see GOVERNANCE.md).

| Gate | From |
|---|---|
| Lint, unit, fuzz (short), file-size, DCO | v0.0 |
| Integration, Playwright smoke, multi-arch build, Trivy/govulncheck thresholds, SBOM, cosign, `docker run` smoke (including the duplicate-writer check) | v0.1 |
| Performance budgets (no regression > 10%), backup verification (`backup --verify`: decrypt, manifest hashes, `integrity_check`) | v0.2 |
| Helm install on kind, backup → restore round-trip, N-1 upgrade test | v0.3 |
| Plugin sandbox negative tests, plugin API compatibility check | v0.4 |
| Upgrade chain test (every consecutive 0.x step up to N), accessibility check | v0.5 |

## 1.0: Public release gates
- [ ] All ADRs that v1 depends on are **Accepted**
- [ ] Plugin API frozen at 1.0, with a deprecation policy published
- [ ] Threat model reviewed; an external (even informal) security review done
- [ ] Upgrade from the latest 0.x tested on the same volume; the rollback procedure tested
- [ ] Signed releases with SBOM and provenance; the SECURITY.md process live (private reporting
      enabled)
- [ ] Docs for all install paths; backup, restore and fencing runbooks in `docs/operations/`
- [ ] A fresh install has no credentials, no open signup and no third-party plugins
      (first-party bundled plugins such as Mermaid are enabled)
- [ ] The conduct contact is a project address (replacing the interim personal email); an
      alternate contact is set in CODE_OF_CONDUCT.md once a second maintainer exists
- [ ] Trademark search done; domain registered (GitHub org and npm org decided earlier, see v0.0)
