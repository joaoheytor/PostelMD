# ADR-0023: A draft key per note session

- **Status:** Accepted
- **Date:** 2026-11-10

## Context
Supersedes the draft-key bullets of [ADR-0020](0020-mvp-scope.md) ("**Key:** `esid` + path"
and "**A reload** keeps the `esid`… drafts already under the tab's `esid` are moved to a
fresh one"), and with them the rule that every draft of the tab's own `esid` is hidden from
the user. The rest of ADR-0020's drafts section stands: the `esid` itself, the timing, the
in-memory fallback and what is deferred. It refines these bullets of
[ADR-0003 §7](0003-save-protocol.md), which is Proposed and is edited to match: "Drafts are
per editing session", the draft key, which draft a save acknowledgement clears, and which
drafts count as orphaned.

Edit generations restart each time a note is opened, so one tab can hold several editing
sessions of the same note in turn. With drafts keyed by the tab's `esid` + path, those
sessions shared one draft. A note closed while its save failed left its edit in that draft,
but reopening the note in the same tab didn't offer it (it was "this tab's"), and the
reopened session's first draft write replaced it. A later save then made the loss permanent
([#105](https://github.com/joaoheytor/PostelMD/issues/105)). The workaround of #104, which
remembered which session wrote each path's draft last, kept a late save from clearing a newer
session's draft but couldn't keep the older draft from being overwritten.

## Decision
- **Draft key:** `<esid>.<n>` + path, where `n` counts the note sessions this tab has opened
  since it loaded (each opening of a note is one session). The `esid` stays per tab. The
  draft key is stored in the draft's existing `esid` field, so the IndexedDB store and its
  key path are unchanged and need no migration.
- A save acknowledgement clears **only its own session's draft**, and only up to the
  generation it saved (ADR-0003 §7). Discard, restore, "save as new note" and "take theirs"
  remove one session's draft; nothing else does.
- **Offered drafts:** every draft except those of this tab's sessions still open. Drafts of
  notes closed in this tab are offered like a closed tab's, so reopening a note shows its
  unsaved draft (restore, compare, discard) instead of writing over it. The list is read
  after this tab's pending draft writes are applied, so the draft that closing writes is in
  it, and the vault-wide "Unsaved drafts" list is read again each time a note is opened.
- **Restore** puts the draft in the editor based on the draft's own base ETag: its save sends
  If-Match with that base, so a draft older than the saved note gets a `412` and the
  conflict view, where the user chooses, never a silent overwrite.
- **A reload** moves every draft under the tab's `esid` or any `<esid>.<n>` to a fresh
  random `esid` before any write, and offers them like a closed tab's. The bare `esid` case
  covers drafts written before this ADR.

## Consequences
- Positive:
  - Two sessions never share a draft: a later session can neither overwrite an earlier one's
    draft nor have its own cleared by the earlier one's late save, with no extra bookkeeping.
  - Same-tab recovery works like recovery after a reload or a closed tab, and a draft left
    behind by newer saves can't revert them.
- Negative / costs:
  - The store grows by one draft per note closed with unsaved edits, until the user restores,
    discards or saves it as a new note. Saved and cleanly closed notes leave nothing.
  - A note closed while its save is still in flight and reopened at once can briefly offer a
    draft that the save then removes; restoring it opens a conflict whose two sides match.
  - A reload rekeys one `esid` per earlier session, each in its own transaction; a crash
    between them leaves the rest for the next load, which rekeys them the same way.
- Follow-ups: ADR-0003's status ([#107](https://github.com/joaoheytor/PostelMD/issues/107)).

## Alternatives considered
| Option | Why not |
|---|---|
| A new random `esid` per note session | It loses the tie between a draft and its tab (the reload rule and "this tab's open notes" could no longer find a tab's drafts), and adds a second id scheme beside the claimed `esid`. |
| Keep the per-path `writers` map of #104 | It stops a late save from clearing a newer draft, but both sessions still write one row, so the newer session overwrites the older draft (#105). |
| Move a closed session's draft aside when the note is reopened | Its late save could then no longer clear it, so a saved edit would stay offered as a stale draft. |
