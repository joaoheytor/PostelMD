import * as api from "./api";
import { parent, rebase } from "./tree";
import type { Workspace } from "./workspace";
import { moveError } from "./workspace-errors";

/**
 * Renames or moves a note or folder; never onto an existing one, and never creating folders on
 * the way: a move has no contract for that (CLAUDE.md rule 4). An open note inside it is saved
 * first, closed, and reopened at its new path, unless the user opened another meanwhile; if it
 * can't be saved, nothing moves. True when it moved; otherwise why it didn't.
 */
export async function moveItem(ws: Workspace, from: string, to: string): Promise<true | string> {
  const into = parent(to);
  if (await ws.tree.missing(into)) return `Folder ${into} doesn't exist. Create it first with New folder.`;
  const open = ws.state.value.open;
  const reopen = open && rebase(open.doc.path, from, to);
  if (open && reopen && !(await ws.saveOpen())) return `Save or resolve ${open.doc.path} before moving it.`;
  // Closed before the move, so no autosave can target the old path.
  const current = reopen ? ws.close() : () => false;
  try {
    await api.move(from, to);
  } catch (err) {
    const why = moveError(err, from, to);
    if (open && current()) await ws.open(open.doc.path); // the caller shows the error after
    return `Couldn't move ${from}: ${why}`;
  }
  await ws.tree.moved(from, to);
  if (reopen && current()) await ws.open(reopen);
  return true;
}
