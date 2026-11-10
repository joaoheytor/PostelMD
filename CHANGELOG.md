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
  Windows path (9p) or a FUSE share (such as Docker Desktop's on macOS) is refused, with the
  fix.
- A runbook for running v0.1: backups, a restore drill, logs, upgrade and rollback, and
  recovering from a lost password, a git lock or a deleted note.

### Changed
- The conflict banner shows a line diff between your text and the other version, and says
  what "Keep my version" and "Use the other version" each do.
- The file tree shows the open note however it was opened (Ctrl+P, a link, "Save it as a new
  note"): its folders expand, it is marked as current and scrolled into view.
- Setup shows the password rule (at least 12 characters) under the field, and says how many
  files from the notes folder became the first commit.
- Save errors read as plain sentences, e.g. "Not saved: can't reach the server. Retrying;
  your edits are kept in this browser.", never a raw browser error.

### Fixed
- New note, New folder, Rename or move and the second setup step put the focus in their
  field, so typing a name and pressing Enter works. Cancelling gives the focus back to the
  button; after Delete it goes to the note's folder, and after a restore to the editor.
- Moving a note into a folder that doesn't exist says so ("Create it first with New folder")
  and keeps what was typed, instead of "not found" and a reset field.
- Error messages in setup, login and the commit dialog are red again, not grey.
- Accessibility: the login and setup pages have a main landmark, diffs can be scrolled from
  the keyboard, and text areas, selects and expandable summaries show a focus ring.
- An idle server no longer keeps about 65 MB after a login: the memory the password hash
  (argon2id) needs is handed back at once, so idle memory stays near 15 MB, well under the
  80 MB budget.
- Commits, and the initial import at setup, now skip the files your `.gitignore` files and
  `.git/info/exclude` ignore (such as `.env` or `node_modules/`), as `git add -A` does;
  files already in the history stay tracked. An existing repository you adopt also gets
  PostelMD's trash folder added to `.git/info/exclude`, so your own `git add -A` no longer
  commits deleted notes. Patterns are matched exactly as git matches them, including
  `!` re-includes, `**`, character classes and `core.ignorecase`. If
  `.git/info/exclude` is a symlink, commits stop with a message asking you to replace it
  with a regular file, rather than skip the patterns it holds.

### Security
- PostelMD answers only to `localhost`, IP addresses and the host names in the new
  `allowed_hosts` setting (`POSTELMD_ALLOWED_HOSTS`); any other name gets
  `421 Misdirected Request`, which stops DNS-rebinding attacks from a web page. **If you reach
  PostelMD by a host name, add it to `allowed_hosts`.**
- Responses carry `Cross-Origin-Opener-Policy` and `Cross-Origin-Resource-Policy`
  (`same-origin`).
- The setup page checks the token before hashing the password, and parallel wrong
  passwords from one address can no longer get past the login backoff: neither can be
  used to keep the server busy hashing and slow down your own login.
- Two password changes racing each other (for example someone with a stolen session and
  the old password, and you) can no longer overwrite each other: the second to save is
  refused.
- The wrong password that starts the login wait now says how long to wait, instead of a
  plain "Wrong password." the next attempt would contradict.
- The secret warning no longer freezes the tab on a crafted note: its patterns used to take
  seconds on some 200 KB inputs, and now take milliseconds.
- A diagram's SVG is sanitized in the app before it becomes an image, in case the sandboxed
  renderer is ever compromised.
- The startup message prints the setup page's full address (for example
  `http://localhost:8080/setup`), with the token kept out of the URL.
