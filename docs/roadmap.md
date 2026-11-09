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
- [x] **DCO enforcement:** install the DCO GitHub App, make it a required status check on
      `main`, and verify it with one unsigned test PR
- [ ] **Failure-scenario suite (executable, before feature work).** Small Go programs and
      tests that reproduce each contract on real temp directories, with fault injection
      (kill points, `ENOSPC`, concurrent writers):
      - **harness (done):** `internal/store/fs` reports every write and `fsync` to a hook;
        `internal/store/fs/faultfs` crashes at any step and models a power loss. The save's
        atomic replace is tested at every crash point, with negative controls proving a
        missing `fsync` is caught. Git refs and objects are crash-tested the same way (#54, #56);
      - **for the MVP (ADR-0020):** a move and a delete to the trash crashed at every step, the
        startup temp sweep, saves racing each other and a delete under the vault mutex (these
        are done, in `internal/vault`), a commit crash followed by a restart and the next
        commit (the git lock path), a second process on the same `/data`, and `fsync` and `EXDEV`
        errors (no fallback, no retried `fsync`). The rest of this list lands with the feature
        that needs it (v0.2 and later):
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

## v0.1: MVP, "My runbooks, safely, in one `docker run`" (ADR-0020)
One user (the maintainer), one vault, no database, no network. The goal is to find out whether
PostelMD is worth using before building the rest of the design.

**Scope:**
- single binary with the embedded SPA and the instance lock;
- one password, set on first run with the setup token; signed session cookies with a session
  epoch; `postelmd reset-password`;
- file tree: create, rename, move (no-clobber) and delete to the trash;
- CM6 editor, live preview (sanitized, with Mermaid), copy buttons;
- safe save: content-hash ETag, `If-Match`/`If-None-Match`, atomic replace, conflict view,
  IndexedDB drafts;
- local git: commit with a message (optional auto-commit), history and diff per file, restore a
  version;
- secret warning on save and paste;
- fuzzy finder and the template starter pack;
- reserved paths; `/healthz`, `/readyz`; multi-arch image.

**Acceptance criteria**
- The canonical `docker run` (ADR-0009) → setup URL in the logs → password set → first note
  saved, in < 2 minutes.
- Pre-setup routing matches the ADR-0009 table (integration test per row).
- A second container on the same `/data` refuses to start. `/data` on 9p, FUSE or NFS, or
  without `RENAME_NOREPLACE`, refuses to start.
- Two tabs edit the same note → the second save gets 412 and the conflict view; nothing is
  lost. An edit made outside PostelMD is caught the same way. Concurrent saves with the same
  `If-Match` → exactly one `200`, the rest `412` (scenario test against the vault mutex).
- An existing notes folder mounted at `/data/vaults/notes` is adopted (initialized as a git
  repository if it isn't one); a Windows-path bind mount is refused with the named-volume fix.
- Deleting a note while an autosave is pending does **not** recreate it (`404`, "this note was
  deleted"), including a save already waiting on the vault mutex when the delete runs.
- Moving onto an existing file returns 409, and the destination is unchanged. A delete moves
  the item to the trash.
- A stale save response never clears newer edits (an e2e test with injected latency).
- Killing the browser mid-edit → the draft is recovered on reopen.
- Pasting an AWS key → editor warning.
- Failure scenarios: a crash at every step of a save, a move, a delete to the trash and a git
  commit, under both the process-kill and the power-loss models; the startup temp sweep. For
  the commit: crash → restart → next commit either succeeds, or fails with the lock message
  and succeeds after "Remove the lock", with the branch at the old or the new commit and no
  change lost.
- Path lookup, create, move and upload naming `.git/config` or `.postelmd/*` return 404. Fuzz
  tests for path traversal pass. The e2e run produces no CSP violation reports.
- A session cookie fails after the password changes; a forged or expired cookie is refused;
  a password change without the current password is refused; repeated failed logins slow
  down but never lock out.
- Restarting the container keeps the session and the notes intact.
- The image runs on amd64 and arm64 and is under 40 MB. The first release is signed and
  attested at both registries (#31).
- The PRD performance budgets for a single user are met with the measurement procedure.
- **The four-week test** in ADR-0020: the maintainer uses it for real notes, nothing is lost,
  and the list of what's missing decides v0.2's order.

## v0.2: Remote git, live sync, search
The order after v0.1 is a starting point; the MVP's findings decide it.
- SQLite (ADR-0002): file IDs, generations, save and structural intents, outbox (the full
  ADR-0003).
- Push and fast-forward-only pull through the egress client (ADR-0010, ADR-0016, #40); remote
  credentials; the commit and push secret gates and findings.
- File watcher + reconciler + SSE (snapshot handoff, replay).
- SQLite FTS5 search; `reindex`. Image paste and drag-drop.

**Acceptance criteria**
- An external `echo >> note.md` shows up in an open browser within 2 s (Linux bind mount), or
  within the reconcile interval (Docker Desktop).
- An external rename keeps the file ID when the content is unchanged. Delete + recreate with
  the same content → the old autosave gets 410/412.
- Pasting an AWS key → the file is **not auto-committed**; push is blocked. A secret committed
  outside PostelMD anywhere in reachable history blocks the push, including on a first push to
  an empty remote. An allow-list change arriving through a pull is inert until approved.
- The egress policy denies every special-purpose block, and a `file://` or local-path remote is
  refused before it is stored (done in #55 and #57); an SSH host-key mismatch fails closed; a
  token in a path or branch name blocks the push.
- A `kill -9` during a git commit, pull or structural operation → repaired on startup (the
  intent tests in the failure-scenario suite).
- Search across 10k notes < 200 ms p95.

## v0.3: Multiple users and homelab dogfooding
- Invites, multiple users, personal and shared vaults with roles; the account and session
  lifecycle (ADR-0018); passkeys; the audit log (database and `file:` sink); `auth.json`
  imported into SQLite.
- `backup` and `restore` (ADR-0017), `reset-admin`, `vault lock`, key rotation; `/metrics`
  behind a token.
- Helm chart (OCI, RWOP PVC), Compose example, GoReleaser binaries.
- Argo CD Application in the `homelab` repo; Sealed Secrets; Longhorn; VictoriaMetrics.
- `docs/operations/` runbooks: backup, restore, fencing before force-delete, key compromise,
  upgrade and rollback.

**Acceptance criteria**
- A viewer can't write (API-enforced, table tests). Removing a member closes their SSE stream
  within 1 s, and their replay contains no events from that vault.
- Suspending a user kills all their sessions. A password reset revokes sessions. Removing the
  last owner returns 409. A credential change without recent re-authentication is refused.
- Passkey registration and login work over HTTPS and localhost. Plain HTTP shows the
  insecure-transport banner.
- `/metrics` returns 404 without its token; `postelmd audit verify` detects an edited, removed
  or inserted line in the `file:` sink; an editor can't write `.github/workflows/*`.
- `postelmd backup --verify` confirms an archive; a restore onto an empty volume starts in
  quarantine; the monthly restore drill has been done once and meets the RTO/RPO in
  ADR-0014 §5.
- A fresh Argo CD sync brings the app up healthy. A chart install on kind with an RWOP PVC
  passes in CI. The fencing runbook has been rehearsed.
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
| Performance budgets (no regression > 10%) | v0.2 |
| Helm install on kind, backup verification (`backup --verify`), backup → restore round-trip, N-1 upgrade test | v0.3 |
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
