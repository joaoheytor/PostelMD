import { join, moveTarget, parent, rebase, type TreeState } from "./tree";
import type { Workspace } from "./workspace";

export type ItemKind = "file" | "folder";

/**
 * The drag data type of an item dragged in the file tree. A drag that doesn't carry it (a file
 * from the desktop) isn't a move (#130).
 */
export const dragType = "application/x-postelmd-item";

/**
 * Where from goes when moved into folder, under its own name. Undefined when it is already
 * there (nothing to do); refused for a folder into itself or one of its subfolders.
 */
export function destination(from: string, kind: ItemKind, folder: string): { to: string } | { refused: string } | undefined {
  if (parent(from) === folder) return undefined;
  if (kind === "folder" && rebase(folder, from, from) !== undefined) {
    const where = folder === from ? "itself" : `${folder}, which is inside it`;
    return { refused: `Can't move the folder ${from} into ${where}.` };
  }
  return { to: join(folder, from.slice(from.lastIndexOf("/") + 1)) };
}

/**
 * The folders the tree has seen (every folder in a loaded listing, and the vault) that from can
 * be in: its own, and those it can move into. Sorted, the vault ("") first. Folders in listings
 * never loaded aren't known: the Rename or move form says so, and a typed path reaches them.
 */
export function folderChoices(s: TreeState, from: string, kind: ItemKind): string[] {
  const known = new Set(["", parent(from)]);
  for (const [folder, entries] of s.folders) {
    for (const e of entries) if (e.kind === "folder") known.add(join(folder, e.name));
  }
  const fits = (f: string) => {
    const d = destination(from, kind, f);
    return d === undefined || "to" in d;
  };
  return [...known].filter(fits).sort();
}

/** The folder a typed move target puts from in, read as the move reads it (moveTarget). */
export function folderOf(from: string, typed: string, kind: ItemKind): string {
  return parent(moveTarget(from, typed, kind));
}

/** The typed path's last part (its name) in folder: what picking a folder in the form types. */
export function intoFolder(typed: string, folder: string): string {
  const path = typed.trim().replace(/^\/+|\/+$/g, "");
  return join(folder, path.slice(path.lastIndexOf("/") + 1));
}

/**
 * Moves a note or folder into folder through the workspace's move (move.ts): it never
 * overwrites, and the open note follows with its edits. The tree's status says what happened:
 * the move (Tree.moved) or why it was refused; a failed move is the workspace's error instead.
 * Its new path when it moved.
 */
export async function moveInto(ws: Workspace, from: string, kind: ItemKind, folder: string): Promise<string | undefined> {
  ws.tree.announce("");
  const d = destination(from, kind, folder);
  if (!d) return undefined;
  if ("refused" in d) {
    ws.tree.announce(d.refused);
    return undefined;
  }
  return (await ws.move(from, d.to)) ? d.to : undefined;
}
