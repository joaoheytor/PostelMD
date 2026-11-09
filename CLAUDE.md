# CLAUDE.md

Conventions for anyone, human or AI agent, working in this repository. These rules exist
because the project this one learned from drifted badly without them (see
`docs/lessons-from-frankmd.md`). Read `docs/architecture.md` and the ADRs before
changing the design.

## Project in one paragraph

PostelMD is a self-hosted, multi-user, git-native Markdown workspace for DevOps/SRE teams.
The backend is a Go single static binary (`CGO_ENABLED=0`) that embeds the frontend, which is
TypeScript + Vite + CodeMirror 6. Plain `.md` files on disk are the **source of truth**. SQLite
holds identity, ACLs and a **rebuildable** index. One image serves `docker run`, Compose and Helm.

## Repository layout (target)

```
cmd/postelmd/          main + subcommands (serve, healthcheck, backup, restore, reindex,
                       reset-admin, keys, vault, plugin, db, audit); online ones via admin.sock
internal/
  http/                handlers + middleware ONLY (no filesystem, no SQL)
  service/             business logic, authorization checks
  store/{fs,watch,git,sql,lock,auditfile}/   the ONLY packages that do filesystem/inotify/
                       git/SQLite I/O
  audit/               security-relevant events: DB + external sink (rule 15)
  egress/              the single outbound HTTP client (rule 13)
  vault/ sync/ gitvault/ secrets/ index/ auth/ keys/ plugins/ config/ setup/
web/
  src/core/            framework-free: doc store, save queue, sync engine, plugin host
  src/editor/          CodeMirror 6 setup and extensions
  src/ui/              UI shell: Lit, light DOM (ADR-0019); never imported by core/ or plugin-api
packages/plugin-api/   @postelmd/plugin-api (types + runtime contract, Apache-2.0)
test/scenarios/        failure-scenario suite (fault injection; one test per recovery contract)
deploy/helm/postelmd/  Helm chart
docs/                  PRD, architecture, ADRs, threat model, roadmap, ci-cd
docs/operations/       runbooks (backup, restore, fencing, key compromise, upgrade/rollback)
CHANGELOG.md           Keep a Changelog; every user-visible PR adds to [Unreleased]
.githooks/             commit-msg + pre-commit (enable: git config core.hooksPath .githooks)
.claude/               shared agent config: settings.json, hooks/postelmd_guard.py, skills/
                       (new-adr, changelog, design-review); *.local.* files are git-ignored
```

## Hard rules

1. **Layering:** `http → service → (vault, sync, gitvault, index, …) → store/*`. Only
   `internal/store/...` does filesystem, inotify, go-git or SQLite I/O. Every vault path goes
   through the `os.Root`-based API and `store.IsReserved`; never use raw `os.Open` on user
   paths. `internal/store/fs` may use `x/sys/unix` `*at` syscalls on a root's directory fd
   (ADR-0006). The single exception outside `store`: `config` reads `config.yaml` and
   `_FILE` secrets during bootstrap.
2. **File size:** 300 lines soft limit, **500 hard limit** (CI-enforced, excluding generated
   files and test fixtures). Functions over ~60 lines need a reason.
3. **No god objects.** On the frontend, state lives in `web/src/core` stores; UI components
   subscribe. Components never talk to each other through ad-hoc DOM events.
4. **The save protocol is sacred** (ADR-0003). Every write uses `If-Match` / `If-None-Match`
   with the ID+generation+hash ETag (the MVP uses a content-hash ETag; ADR-0020). Autosave
   never creates a file. Moves never overwrite the destination. **Deletes never unlink in place**: they move the item into the vault trash,
   and a resumable purge removes it later. A save response clears dirty state only for its
   own edit generation and editing session (`esid`). Any change to save, sync, git or
   conflict code needs tests first.
5. **No new dependency without an ADR** (or a line in an existing one) explaining why the stdlib
   or an existing dependency is not enough. No hand-vendored JS; npm + lockfile only.
6. **Secure defaults are not optional.** Never add a "disable auth", open CORS, `unsafe-inline`
   CSP, debug or log endpoint, or a wildcard host without an ADR.
7. **Config:** read settings only through `internal/config`; never `os.Getenv` elsewhere.
   Secrets support `_FILE` variants. Precedence: flags > env > config.yaml > defaults.
8. **Timers and listeners:** every `setTimeout`/`setInterval`/`addEventListener` has a
   matching cleanup (a `Disposable`). Go goroutines take a `context.Context` and exit on cancel.
9. **Performance is a requirement** (see the PRD NFRs): debounce autosave, compute stats lazily,
   and do not render the preview when it is hidden.
10. **Plugin API stability:** never import UI-framework types into `packages/plugin-api`. Any
    breaking change needs a major `apiVersion` bump and a deprecation period (ADR-0008).
11. **Never execute user content** (code blocks, templates) on the server (ADR-0011).
12. **One writer.** Never add code paths that open `/data` without holding the instance lock
    (ADR-0014). All app-managed git mutations go through `gitvault` under the vault lock
    (ADR-0016).
13. **Outbound requests** only through the egress client (architecture §11).
14. **Crash-safe mutations.** Any operation with more than one side effect (filesystem + DB,
    git, structural ops, backup, restore, migration) writes a **durable intent before its
    first side effect**. It also needs an operation-specific recovery table in its ADR and a
    test in `test/scenarios/` for every crash point. Never infer what happened from a single
    signal (HEAD alone, a hash match alone, a timeout).
15. **Secrets and logs:** never log note content, secret values, tokens or passwords. Secret
    scanning runs on the exact bytes and metadata being committed or pushed, and cached
    detections never stand in for authorization decisions (ADR-0010). Security-relevant
    actions go through the audit package, which writes to the DB **and** the external sink
    (the MVP has no DB: stdout only; ADR-0020).
16. **Authorization per request:** check membership and role on every request and every SSE
    event delivery. Never cache roles in sessions (ADR-0018). In the MVP (one user), check the
    session's signature, expiry and epoch on every request (ADR-0020).
17. **Plugins:** sandboxed plugins are headless by default, with code in a Worker, identity
    bound to a `MessageChannel` port, and any extra capability needing the admin's approval
    again (ADR-0008).

## Workflow

- Small vertical slices: one feature per PR, with tests, docs and, if needed, an ADR.
- **PR title = Conventional Commit** (`feat(editor): …`, `fix!: …`). PRs are squash-merged,
  and every commit is signed off (`git commit -s`, DCO).
- **Versioning:** SemVer. Before 1.0 a breaking change bumps the minor version (GOVERNANCE.md).
- **ADRs are Proposed** until the first code that depends on one is merged; that PR flips it to
  Accepted. Accepted decisions only change by being superseded; clarifications are
  append-only (`docs/adr/README.md`).
- **Agents never** push to `main`, create tags or releases, or change repository settings or
  secrets without the maintainer's explicit go-ahead in the conversation.
- **GitHub Actions:** pin actions by commit SHA, set least-privilege `permissions:`, never use
  `pull_request_target` with a PR checkout, and use GitHub-hosted runners only
  (`docs/ci-cd.md`).
- **Bug fixes start with a failing test.**
- After a refactor, delete the dead code in the same PR.
- `.gitignore` patterns must be anchored (`/notes/`, not `notes/`).
- Bug reports and agent prompts must say what broke, the exact error, steps to reproduce,
  what changed, and include logs. "It stopped working" is not a report.
- Humans review every diff an agent produces. No unattended agent runs on the main branch.
- **This repository is public.** Never write private hostnames, IPs, home paths, tokens or
  work references into it. `.claude/hooks/postelmd_guard.py` blocks secrets, asks about
  anything that looks private, and turns every outward-facing command (push, tag, release,
  PR, issue, repo settings) into a permission prompt for the maintainer.

## Definition of Done

- [ ] Tests added and passing: `make ci` (Go, web, failure scenarios), plus Playwright smoke
      tests if the UI changed
- [ ] New recovery paths have a scenario test for each crash point
- [ ] Lint clean: `golangci-lint`, `eslint`, file-size check
- [ ] ADR added or updated if the design changed. Threat model updated if a trust boundary
      changed.
- [ ] Docs updated; `CHANGELOG.md` `[Unreleased]` entry added for user-visible changes
- [ ] No dead code, no TODO without an issue link

## Commands

```sh
make ci         # everything the CI `required` check runs, locally (no signing or publishing)
make test       # go test -race ./... and the web tests (Vitest)
make lint       # golangci-lint, tsc + eslint, file size, actionlint, shellcheck, yamllint
make vuln       # govulncheck + npm audit --omit=dev
make build      # bin/postelmd
```

Planned, with the code that needs them: `make dev` (go run + Vite dev server),
`make scenarios` (test/scenarios), `make e2e` (Playwright), `make image` (multi-arch).

## Testing conventions

- Go: table-driven tests on real `t.TempDir()` vaults. Fuzz path confinement and the save
  protocol (`go test -fuzz`).
- Failure scenarios (`test/scenarios/`): inject a crash at each recorded step (a kill-point
  hook in the store layer), simulate `ENOSPC`, external writers and duplicate processes, then
  assert the recovery table's outcome. These are permanent regression tests.
- Frontend: Vitest for `core/` (no DOM needed). Playwright uses role- and label-based
  locators only; no CSS or structure selectors.
- Never quarantine a flaky test silently: fix it or delete it with an issue link.
