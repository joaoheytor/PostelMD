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
