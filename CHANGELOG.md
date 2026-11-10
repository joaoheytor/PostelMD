# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Before 1.0, breaking changes bump the minor version (see [GOVERNANCE.md](GOVERNANCE.md)).

## [Unreleased]

### Added
- Foundation documents: PRD, architecture, ADRs 0001–0018, threat model, roadmap, CI/CD
  design, and governance files (Apache-2.0 license, DCO, Code of Conduct, security policy).
- The web app, embedded in the binary: first-run setup with the server's token, login, a file
  tree to open, create and delete notes and folders, and a CodeMirror 6 editor that autosaves
  with `If-Match` and shows a conflict instead of overwriting an edit made elsewhere.
- Rename or move a note or folder from the web app. A move never overwrites an existing note
  or folder, an open note is saved first and follows the move, and a folder can't move into
  itself (the API now answers 400 instead of an internal error).
- Commit from the web app: a dialog lists the changes since the last commit and commits them
  with a message, after saving the open note. A git lock left by a crash is shown with its
  age and removed only after you confirm that no other git command is running.
- A note's history in the web app: the commits that changed it, what changed since each one
  (a line diff against the current text) or its whole text, and "Restore this version",
  which saves the note first and replaces it only if it hasn't changed in the meantime.
- A container image (`make image`): distroless, non-root, about 24 MB, for amd64 and arm64,
  with a health check and `/data` owned by the image's user so a named volume needs no
  `--user`. Not published yet.
- A live preview beside the editor (the Preview button): CommonMark and GFM (tables, task
  lists, strikethrough, autolinks) and YAML front matter, rendered off the main thread and
  sanitized before it reaches the page. A relative link to a note opens it. The web app's CSP
  now requires Trusted Types and allows no inline script or style.
- The preview scrolls with the editor, both ways, and code blocks have a "Copy code"
  button. Where the browser denies clipboard access (plain http off localhost), the button
  selects the code instead.
- Mermaid diagrams in the preview. They render in a sandboxed frame that can't reach the
  app or the network, and are shown as images named by the diagram's `accTitle`. A diagram
  with an error keeps its code and shows the error under it.
- Unsaved edits are kept as drafts in the browser until the server has them. If a tab crashes,
  closes or reloads before a save lands, opening the note offers the draft: compare it with
  the saved text, restore it or discard it. "Unsaved drafts" lists every draft on the device,
  and a draft of a note deleted meanwhile can be saved as a new note. PostelMD never deletes
  a draft without asking.
- A warning when a note looks like it contains a secret (cloud and SaaS tokens, private
  keys, JWTs, kubeconfig client keys, passwords in URLs): at once on paste, and on every save.
  It names the kind of secret and the line, never the secret itself, and doesn't block saving.
- A fuzzy finder: press `Ctrl+P` (or **Find a note**), type part of a note's path, and press
  Enter to open it. Matches in the file name and at word starts rank first.
- Note templates: **New note** can start from a runbook, postmortem, ADR or on-call handoff.
  The note's name and today's date are filled in; the form asks for the rest, and anything left
  empty stays as a `{{placeholder}}` to fill in later.
- An optional settings file, `.postelmd/config.yaml` in the data directory. Flags and
  `POSTELMD_*` environment variables still win over it. An unknown key stops the server with
  its name, so a typo is never silently ignored.
- One log line for every write to the API (saves, creates, moves, deletes, commits, logins):
  the method, the route, the status and how long it took, never the note's path or content.
  Conflicts (412) and server errors can be counted from the logs.
- The server refuses to start when no volume is mounted at `/data`, since the next
  `docker rm` (every upgrade) would delete everything. It says how to add one;
  `POSTELMD_ALLOW_EPHEMERAL_DATA=true` (or `allow_ephemeral_data` in `config.yaml`) starts it
  anyway for a throwaway try.
- The notes folder's filesystem is checked like the data directory's: an adopted folder on a
  Windows path (9p) or a macOS share (virtiofs) is refused, with the fix.
- A runbook for running v0.1: backups, a restore drill, logs, upgrade and rollback, and
  recovering from a lost password, a git lock or a deleted note.

### Fixed
- An idle server no longer keeps about 65 MB after a login: the memory the password hash
  (argon2id) needs is handed back at once, so idle memory stays near 15 MB, well under the
  80 MB budget.
- Commits, and the initial import at setup, now skip the files your `.gitignore` files and
  `.git/info/exclude` ignore (such as `.env` or `node_modules/`), as `git add -A` does;
  files already in the history stay tracked. An existing repository you adopt also gets
  PostelMD's trash folder added to `.git/info/exclude`, so your own `git add -A` no longer
  commits deleted notes.
