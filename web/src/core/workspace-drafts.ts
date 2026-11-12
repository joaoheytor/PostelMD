/** Recovering a note's text from local drafts (ADR-0003 §7), for the Workspace. */
import * as api from "./api";
import type { Draft } from "./drafts";
import { normalize } from "./textfile";
import type { Workspace } from "./workspace";
import { message } from "./workspace-errors";

/** Where a copy of path's text goes ("x.md" → "x-recovered.md"). */
export function recoveredPath(path: string): string {
  return path.replace(/(\.[a-z]+)?$/i, (ext) => `-recovered${ext || ".md"}`);
}

/** Reads the drafts to offer again. */
export async function refreshOffered(ws: Workspace): Promise<void> {
  ws.offered.set((await ws.drafts?.offered()) ?? []);
}

/**
 * Saves the open note's text as a new note next to it ("x.md" → "x-recovered.md"), for a
 * note deleted elsewhere: autosave never recreates it (ADR-0003 §7), but the user can.
 * Returns why it couldn't, if it couldn't.
 */
export async function saveAsNew(ws: Workspace): Promise<string | undefined> {
  const open = ws.state.value.open;
  if (!open) return;
  const path = recoveredPath(open.doc.path);
  try {
    await api.createNote(path, ws.currentText() ?? open.initial);
  } catch (err) {
    return `Couldn't create ${path}: ${message(err)}`;
  }
  await ws.tree.changed(path);
  await ws.open(path); // closing the old note writes its draft, which goes next
  // Only if the copy opened: a note that stays open (the copy couldn't be read) keeps its draft.
  if (ws.state.value.open?.doc.path === path) open.doc.dropDraft();
}

/**
 * Puts a draft of the open note in the editor (put replaces its text) as an edit, which then
 * saves through the normal path. A draft is based on the version it was edited from: its
 * save sends If-Match with that base, so a draft older than the saved note gets a 412 and the
 * conflict view, never a silent overwrite. The old draft is removed only once this tab's own
 * draft holds the text.
 */
export async function recoverDraft(ws: Workspace, d: Draft, put: (text: string) => void): Promise<void> {
  const open = ws.state.value.open;
  if (!open || open.doc.path !== d.path) return;
  if (d.base !== open.doc.etag) {
    await open.doc.flush(); // no save in flight may move the base past the draft's
    if (ws.state.value.open !== open) return;
  }
  put(normalize(d.text)); // drafts hold the file's text
  open.doc.rebase(d.base);
  open.doc.edit();
  if (!(await open.doc.writeDraft())) return; // no other copy yet: the old draft stays
  await discardDraft(ws, d);
}

/** Deletes a draft the user chose to discard. */
export async function discardDraft(ws: Workspace, d: Draft): Promise<void> {
  await ws.drafts?.discard(d);
  await refreshOffered(ws);
  ws.noteDrafts.update((all) => all.filter((x) => x.esid !== d.esid || x.path !== d.path));
}

/**
 * Saves a draft as a new note ("x.md" → "x-recovered.md"); the draft goes only once saved.
 * Returns why it couldn't, if it couldn't.
 */
export async function saveDraftAsNew(ws: Workspace, d: Draft): Promise<string | undefined> {
  const path = recoveredPath(d.path);
  try {
    await api.createNote(path, d.text);
  } catch (err) {
    const why = err instanceof api.ApiError && err.status === 409 ? `${path} already exists` : message(err);
    return `Couldn't save the draft as ${path}: ${why}`;
  }
  await ws.drafts?.discard(d);
  await refreshOffered(ws);
  await ws.tree.changed(path);
  if (await ws.saveOpen()) await ws.open(path);
}
