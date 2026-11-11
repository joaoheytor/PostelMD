import * as api from "./api";
import type { Workspace } from "./workspace";
import { message } from "./workspace-errors";

/**
 * Replaces the open note's text with its version from commit, through the save path
 * (ADR-0020): the open note is saved first, and the server replaces it only if it is still
 * that saved version (If-Match). The note is then reopened with what the server holds.
 * True when it was replaced; false with no note open; otherwise why it wasn't.
 */
export async function restoreVersion(ws: Workspace, commit: string): Promise<boolean | string> {
  const open = ws.state.value.open;
  if (!open) return false;
  const path = open.doc.path;
  if (!(await ws.saveOpen())) return `Save or resolve ${path} before restoring a version.`;
  // Closed first, so no autosave races the restore.
  open.doc.dispose();
  ws.state.set({});
  let failed: unknown;
  try {
    await api.restore(path, commit, open.doc.etag);
  } catch (err) {
    failed = err;
  }
  await ws.open(path); // reopening clears the error
  if (failed === undefined) return true;
  const why =
    failed instanceof api.ApiError && failed.status === 412 ? "it changed in the meantime, so nothing was replaced" : message(failed);
  return `Couldn't restore ${path}: ${why}`;
}
