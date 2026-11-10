# ADR-0020: MVP scope: one user, one vault, no database

- **Status:** Proposed
- **Date:** 2026-11-09

## Context
The design so far (ADR-0001 to ADR-0019) is for a public, multi-user product. It covers
shared vaults with roles, external writers racing the server, git pull, plugins, encrypted
backups and audit chains. Every requirement multiplies the others, and the result is crash
recovery tables for a dozen operations before the first note can be saved.

None of that tells us whether PostelMD is worth using. The maintainer's goal for the first
release is narrower: **prove that PostelMD makes sense, with the maintainer as its only
user**, using the smallest version that is still safe. FrankMD's real failures were ordinary
ones, and they stay in scope ([lessons](../lessons-from-frankmd.md)): autosave recreated
deleted files, the conflict check silently did nothing, auth was off by default, and there
were no conventions.

## Decision
The **MVP (v0.1)** is the subset below. Everything else in the earlier ADRs stays as the
design for later milestones (see `roadmap.md`). Those ADRs stay **Proposed**; each one now
says which of its parts the MVP uses.

### One user, one vault
- **One account**, the owner of the instance. There are no other accounts, roles,
  memberships, invites or passkeys (ADR-0005's local provider with passwords only; ADR-0006's
  model and ADR-0018 are deferred).
- **One vault** at `/data/vaults/notes/`, with app state in `/data/.postelmd/`: ADR-0009's
  volume layout (architecture §3), with a fixed slug. App state never lives inside the vault.
  The vault's reserved paths are `.git/`, `.postelmd-tmp/` and `.postelmd-trash/`.

### Installation
ADR-0009's canonical commands apply, narrowed to filesystems with `RENAME_NOREPLACE`:
- **Linux, or Docker Desktop from a WSL shell with a WSL-side path:**
  `-v ~/postelmd:/data` (with `--user`, as in ADR-0009). Setup creates an empty vault.
- **An existing notes folder:** add `-v ~/notes:/data/vaults/notes`. Setup adopts it. If it
  isn't a git repository, setup initializes one and commits the existing files as the first
  commit, after showing the file count. If it is one, it's used as it is, and a configured
  remote is ignored (no push or pull in the MVP). Adoption refuses a folder that contains
  `.postelmd-tmp` or `.postelmd-trash` entries it didn't create, or a symlink that leaves it
  (ADR-0006). `.md` files at the root of `/data` stop setup with the fix, as in ADR-0009.
- **A named volume** (`-v postelmd-data:/data`) works everywhere, including Docker Desktop on
  Windows and macOS.
- **Not supported in the MVP:** a vault on a filesystem without `RENAME_NOREPLACE`. Startup
  probes the capability and refuses with a message that points to the named-volume command.
  Measured: a Windows-path (`C:\…`) bind in Docker Desktop is 9p and is refused. macOS binds
  haven't been measured; the probe decides. ADR-0009's fallback instructions return with
  ADR-0006's fallbacks.

### No database
The MVP has **no SQLite**. ADR-0002's database arrives with the features that need it: file
identity, the watcher, search and multiple users.
- **The credential** lives in `/data/.postelmd/auth.json` (mode `0600`): the argon2id
  password hash and a **session epoch**. It is replaced with the atomic write path (temp,
  `fsync`, rename, `fsync` the directory): one side effect, so no intent.
- **First run** keeps ADR-0009's one-time setup token in the logs: the setup page sets the
  password, plus the name and email used as the git author. A forgotten password is reset
  with `postelmd reset-password`, run with the server stopped (it takes the instance lock).
- **Sessions** are cookies signed (HMAC-SHA256) with `/data/.postelmd/secret.key`, created
  `0600` on first run and never regenerated. A cookie holds the epoch and an absolute expiry
  (7 days), and is `HttpOnly`, `SameSite=Lax`, and `Secure` on TLS. **Every request** checks
  the signature, the expiry and the epoch (CLAUDE.md rule 16). Changing the password or
  "log out everywhere" bumps the epoch, which revokes every session. Logging out clears that
  cookie only.
- **Changing the password requires the current password** in the same request (ADR-0018's
  re-authentication rule, kept). A stolen cookie alone can't lock the owner out.
- Login failures are slowed down per source IP (after 5 failures, 1 s doubling up to 15 min;
  a success resets it). There's no account-wide backoff: with one account and no passkeys,
  it would let anyone delay the owner's login (ADR-0005's account rule assumes passkeys).
  Password checks run one at a time, because each argon2id takes 64 MB.
- argon2id comes from `golang.org/x/crypto`, already in the module graph through go-git; the
  MVP makes it a direct dependency (CLAUDE.md rule 5). `postelmd reset-password` reads the
  password without echo through `golang.org/x/term`, also already in the graph (go-git's
  SSH transport); it becomes direct too.

### Saving
ADR-0003's client rules stay. Its server side is replaced, for the MVP, by single-rename
operations that need no intents (CLAUDE.md rule 14 applies to operations with more than one
side effect):
- **The ETag is the SHA-256 of the content.** Update requires `If-Match`; create requires
  `If-None-Match: *`. A mismatch returns `412` with the current content for the conflict view
  (two tabs, or an edit made outside PostelMD). **Autosave never creates a file**: a save to a
  missing file returns `404`, and the client shows "this note was deleted" instead of
  recreating it.
- A file deleted and recreated with identical bytes matches an old `If-Match`. That's
  accepted: the edit was based on exactly those bytes, so nothing is lost.
- **Atomic replace** as implemented and crash-tested in `internal/store/fs` (#53): temp in
  `.postelmd-tmp/`, `fsync`, `rename`, `fsync` both directories. A crash leaves the old or the
  new note, never a torn one. At startup **every** entry in `.postelmd-tmp/` is removed: a save
  that didn't finish never returned `200`, so the client still has the edit as a draft.
- **Moves** use `RENAME_NOREPLACE` and return `409` on an existing destination. **Deletes**
  move the file or folder into the vault's `.postelmd-trash/<timestamp>-<name>` with one
  `RENAME_NOREPLACE`. The trash lives inside the vault, so the rename never crosses a
  filesystem, and nothing is unlinked in place (CLAUDE.md rule 4). The trash is emptied by
  hand from the UI.
- After a move or a delete, the destination folder is synced before the source. So a power
  loss in between can leave the item in **both** places, a duplicate to clean up, but never in
  neither. The crash tests check this, under both the process-kill and power-loss models.
- A filesystem without `RENAME_NOREPLACE` is refused at startup (ADR-0006's fallbacks are
  deferred), as are 9p, FUSE and NFS (ADR-0014).
- **One vault mutex** serializes every mutation (ADR-0003 §4, simplified: no per-file
  locks). It is held from the precondition check through durable completion (the last
  `fsync`), for saves, creates, moves, deletes, restores from history, trash purges and git
  commits. A commit reads the files it commits under the same mutex. So two saves with the
  same `If-Match` can't both pass: the second sees the new hash and gets `412`. A save that
  waited behind a delete finds the file gone and gets `404`; it never recreates it. A
  request waits at most 5 s for the mutex, then fails with `503 Retry-After`; the client
  retries, and the draft keeps the edit. Startup removes temp files before HTTP starts.
- **Client:** the save queue with debounced autosave, the per-edit `esid`/generation rule (a
  stale response never clears newer edits), and IndexedDB drafts all stay (ADR-0003 §7).
  In the MVP the drafts are simplified as follows:
  - **Key:** `esid` + path. One origin is one instance, one user and one vault, so the origin
    is the namespace ADR-0003 keys by `instanceID/userID/vaultID`, and paths stand in for file
    IDs.
  - **`esid`:** 128 random bits from `crypto.getRandomValues`, which, unlike `randomUUID` and
    Web Locks, also exists on plain http off localhost. There, without Web Locks, every load
    mints a new `esid`, as ADR-0003 §7 requires.
  - **A reload** keeps the `esid` but restarts edit generations, so drafts already under the
    tab's `esid` are moved to a fresh one at load, before any write, and are offered like a
    closed tab's.
  - **Timing:** a draft is written 300 ms after the last edit, never more than 2 s after the
    first edit not yet in one, and at once when the tab is hidden or the note closes.
  - **Without IndexedDB** (some private modes), drafts live in memory: they survive a closed
    note, not a closed tab.
  - **Not in the first drafts change:** one-tab-at-a-time editing over `BroadcastChannel`
    ([#76](https://github.com/joaoheytor/PostelMD/issues/76); until then, two tabs on one note
    still get `412` and the conflict view), and ADR-0018's logout prompt and shared-computer
    mode ([#77](https://github.com/joaoheytor/PostelMD/issues/77)).
- **External writers:** no watcher yet. A change made outside PostelMD shows when the note is
  reopened, and a save over it gets `412` because the hash moved. The uncoordinated race in
  ADR-0003 §1 is documented as before.

### API
Paths, not file IDs (ADR-0003 §3's IDs arrive with SQLite):

| Request | Result |
|---|---|
| `GET /api/v1/notes/{path}` | the note's bytes and its `ETag` |
| `PUT /api/v1/notes/{path}` with `If-Match` | save: `204`, or `412` with the current note and its `ETag`; `404` if the note is gone |
| `PUT /api/v1/notes/{path}` with `If-None-Match: *` | create: `201`, or `409` |
| `PUT` with neither or both | `428` |
| `DELETE /api/v1/notes/{path}` or `/api/v1/folders/{path}` | move to the trash |
| `GET` / `POST /api/v1/folders/{path}` | list (reserved entries hidden) / create a folder |
| `POST /api/v1/move` (`application/json`) | move, `409` on an existing destination |
| `GET /api/v1/files` | every file's path, sorted, for the finder (reserved paths and symlinks never listed; at most 50,000, with `truncated`) |

A held vault mutex gives `503` with `Retry-After`; a note over 5 MB gives `413`. Reserved and
malformed paths, and paths through a symlink, give `404`.

Authentication:

| Request | Result |
|---|---|
| `POST /api/v1/setup` `{token, password, name, email}` | first run: `201` + session; wrong token `403`; after setup `409` |
| `POST /api/v1/session` `{password}` | log in: `204` + session; `401`; `429` with `Retry-After` while throttled |
| `GET /api/v1/session` | `204` with a valid session, else `401` |
| `DELETE /api/v1/session` | log out this browser |
| `POST /api/v1/session/revoke` | log out everywhere (bumps the epoch) |
| `PUT /api/v1/password` `{current, next}` | `204` + a new session; every other session is revoked |
| `POST /api/v1/setup/check` `{token}` | before setup: `{files}`, the number of files setup will commit; `403` on a wrong token |

Setup answers `{imported}`: an adopted folder's files become the "Initial import" commit,
with the author entered at setup. If that commit fails, setup still succeeds and the files
stay uncommitted changes.

Git (every request needs a session):

| Request | Result |
|---|---|
| `GET /api/v1/git/status` | `{branch, changes: [{path, kind}], skipped, locks}` |
| `POST /api/v1/git/commit` `{message}` | `201 {commit}`; `409` nothing to commit, branch moved or detached HEAD; `422` empty message; `423` a git lock blocks commits |
| `POST /api/v1/git/locks/remove` `{confirmNoOtherGit: true}` | removes the leftover locks; `422` without the confirmation |
| `GET /api/v1/history/{path}?limit=` | `{versions: [{commit, message, author, when, deleted}]}`, newest first |
| `GET /api/v1/versions/{commit}/{path}` | the note's bytes in that commit, without an `ETag` |
| `POST /api/v1/restore` `{path, commit}` with `If-Match` | through the save path: `204` + `ETag`, `412` with the current note, `404` if the note is gone, `428` without `If-Match` |

Every notes request needs a session (`401` otherwise). Non-safe cross-origin requests are
refused with `403` (`net/http.CrossOriginProtection`), and the JSON endpoints accept only
`application/json`. Before setup, ADR-0009's routing applies. The session cookie is
`HttpOnly` and `SameSite=Lax`; over TLS it is `__Host-` prefixed and `Secure`.

### Git
- The vault is the git repository: the working tree is `/data/vaults/notes`, and its git
  metadata is `/data/vaults/notes/.git` (the repository setup adopts or creates). `/data`
  itself is never a repository. The editor shows the uncommitted changes, and the user
  commits them with a message. **There is no auto-commit in the MVP.** ADR-0010 allows it
  only behind the commit gate (the server scans the exact bytes it commits and skips a file
  with a finding), and the MVP's secret warning runs in the editor only. A manual commit is
  safe because the user has seen the warning first; an auto-commit would put a pasted secret
  into the history unseen, and removing it means rewriting history. Auto-commit arrives in
  v0.2, with the gate.
- A commit, and the initial import, take what `git add -A` would: an untracked file that
  a `.gitignore` (each scoped to its directory) or `.git/info/exclude` ignores is neither a
  change nor committed, and an ignored directory isn't walked. A tracked file stays tracked
  even if a pattern matches it, so its edits and its deletion are committed. A global
  `core.excludesFile` isn't read (the container has none). The editor's tree and file list
  still show ignored files.
- A commit is built from bytes (#39), its objects are durable before the ref moves (#56),
  and the ref is moved with git's lockfile protocol (#54). After a crash, the branch is the
  old or the new commit. Files whose commit didn't land are still uncommitted changes, so the
  next commit includes them. There's no intent.
- A commit also resets `.git/index` to the new commit (ADR-0016), before the branch moves
  and with git's lockfile protocol, so the user's own `git status` on the host shows what
  PostelMD committed. A repository PostelMD creates lists `.postelmd-trash/` and
  `.postelmd-tmp/` in `.git/info/exclude`; an adopted one gets the lines it lacks appended
  when it is opened (written aside and renamed into place; its other lines are kept), so a
  host `git add -A` never commits the trash. This is best effort: if `.git/info` is
  read-only, or `info/exclude` is a symlink or not a regular file (never followed or
  replaced, and then its patterns don't apply), the server logs a warning and starts. A repository is
  created in the vault's temp folder and renamed into place, so a crash never leaves half a
  `.git`.
- **A leftover git lock needs the user.** A crash during the ref or index update can leave
  `refs/heads/<branch>.lock` or `.git/index.lock`, and then every commit fails. PostelMD never
  removes that lock by itself: a git process the user runs on the host creates the same file,
  and one signal (the file exists) can't tell the two apart (CLAUDE.md rule 14). Instead:
  - the git panel shows "a git lock is blocking commits", with the lock's path and age;
  - **"Remove the lock"** asks the user to confirm that no other git command is running on
    this repository, then removes it under the vault mutex. Nothing is lost: the branch
    still points to the old or the new commit, and anything not committed is still in the
    working tree.

  Saving notes keeps working while commits are blocked.
- History and diff per file; "restore this version" writes the old content through the save
  path.
- **No push and no pull in the MVP.** A remote brings credentials, the push gate for secrets,
  and pull's multi-file worktree changes. All three come together later (ADR-0010,
  ADR-0016). Until then, the repository is a plain git repository on the host, and the user
  can push it with their own tools.

### In the editor
CodeMirror 6, the sanitized live preview in a Worker (ADR-0007), Mermaid, copy buttons, the
file tree, a fuzzy finder over the file list, and the runbook/postmortem/ADR templates. The
finder (`Ctrl+P`) ranks paths in the browser (`web/src/core/fuzzy.ts`), preferring matches
in the file name, at word starts and in a row. The
starter templates (runbook, postmortem, ADR, on-call handoff) ship inside the app
(`web/src/templates/`) and are offered by the "New note" form. `{{title}}` and `{{date}}` are
filled in automatically, and the form asks for each template's other placeholders; an empty
one stays as `{{name}}` in the note. Substitution is plain text, with no logic (ADR-0011).
Editable instance templates in `.postelmd/templates/` (architecture §3) come later. A
**secret warning** runs on save and paste (a regex scan with a small rule set, not stored).
It only warns, because nothing leaves the machine in the MVP. It runs in the browser
(`web/src/core/secrets.ts`): a paste rescans the note at once, and every save scans the exact
text it sends. The rules are a few high-precision patterns after gitleaks' (cloud and SaaS
tokens, private keys, JWTs, kubeconfig client keys, passwords in URLs), and documentation
placeholders don't match. A finding names the rule, the line and the first four characters,
never the secret; nothing is stored or logged. A dismissed warning stays hidden until the
findings change. The server-side scan, stored findings and the commit and push gates of
ADR-0010 come with remotes in v0.2.

### Kept as designed
The single-binary image with the embedded SPA, the instance lock (ADR-0014, CLAUDE.md
rule 12), confinement and reserved paths (ADR-0006), secure defaults (auth always on, strict
CSP, CSRF protection, no debug endpoints), the egress client for any outbound request,
`/healthz` and `/readyz`. Security-relevant events (logins, failed logins, password changes)
are written as structured `slog` records to stdout. The audit database and the HMAC-chained
file sink arrive with multiple users.

### Deferred
| Next | What | ADRs |
|---|---|---|
| v0.2 | Push and pull, remote credentials, the commit and push secret gates, findings | 0010, 0016 |
| v0.2 | SQLite: file IDs, generations, save and structural intents, outbox | 0002, 0003 |
| v0.2 | Watcher, reconciler, SSE; search (FTS5); media upload | 0002, 0004 |
| v0.3 | Multiple users: accounts, roles, shared and personal vaults, invites, passkeys, the account lifecycle, the audit database | 0005, 0006, 0018 |
| v0.3 | Encrypted backup and restore, key rotation, `/metrics`, Helm | 0009, 0014, 0017 |
| v0.4 | Plugins | 0008 |

The order after v0.1 is a starting point; what the MVP shows is missing decides it.

### How we'll know it makes sense
The MVP succeeds if, after **four weeks** of the maintainer using it for real runbooks and
notes:
- it's used without falling back to another editor for those notes;
- no edit has been lost (zero `5xx` on saves; every `412` resolved in the conflict view);
- there's a short list of what's missing, and it decides v0.2's order.

If it fails that test, the project stops or changes direction before building the deferred
design.

## Consequences
- Positive:
  - A usable release with a fraction of the code: no database, no intent recovery, no
    network.
  - Every MVP mutation is one atomic rename or one ref update, and both are already
    crash-tested.
  - The earlier design isn't lost: it stays Proposed for the milestones that need it.
- Negative / costs:
  - A stolen session cookie is valid until it expires or the epoch is bumped; one session
    can't be revoked server-side. It can't change the password.
  - A crash during a commit can need one confirmed click to remove a git lock.
  - A vault bind mount without `RENAME_NOREPLACE` (Windows paths in Docker Desktop) isn't
    supported; a named volume is.
  - Notes edited outside PostelMD aren't shown live.
  - The repository is pushed by hand.
  - Adding users later means migrating `auth.json` into SQLite (a one-time, tested import).
- Follow-ups: `roadmap.md` lists the deferred items by milestone.

## Alternatives considered
| Option | Why not |
|---|---|
| Keep the full v0.1 design | Months of recovery code before anyone can say whether the editor is worth using. |
| SQLite from the start, for the credential and sessions | A database and migrations for one row; a `0600` file with a session epoch covers a single user. |
| No auth for a single user | Auth stays on (ADR-0005, FrankMD lesson); one password costs little. |
| A few trusted accounts with equal access | Not needed yet: the maintainer is the only user. Accounts come with roles in v0.3. |
| Push in the MVP, without the secret gate | Pushing a secret to a remote is the one MVP-sized mistake we can't undo; push waits for the gate. |
