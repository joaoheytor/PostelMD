import { ApiError } from "./api";
import { nameProblem } from "./names";
import { Store } from "./store";
import type { TrashItem } from "./trash-api";
import { moveTarget, parent } from "./tree";
import { message } from "./workspace";

export interface TrashState {
  /** Newest first; undefined until loaded. */
  items?: TrashItem[] | undefined;
  /** The trash holds more than the server lists. */
  truncated?: boolean | undefined;
  error?: string | undefined;
}

export interface TrashDeps {
  list(): Promise<{ items: TrashItem[]; truncated: boolean }>;
  restore(id: string, folder: string, name: string): Promise<string>;
  /** The tree: reload the folder that holds path, and show path. */
  changed(path: string): Promise<void>;
  reveal(path: string): Promise<void>;
}

/**
 * Where a typed path puts a restored item: empty means the vault root under its own name, a
 * trailing slash a folder under its own name ("ops/"), anything else a folder and a new name.
 * A note keeps its extension when the new name has none, as in a move.
 */
export function restoreTarget(typed: string, name: string, kind: "file" | "folder"): { folder: string; name: string } {
  const t = typed.trim();
  const to = moveTarget(name, t === "" || t.endsWith("/") ? `${t}${name}` : t, kind);
  const folder = parent(to);
  return { folder, name: folder === "" ? to : to.slice(folder.length + 1) };
}

/** The trash (ADR-0024): what is in it, and restoring an item to a folder of the user's choice. */
export class Trash {
  readonly state = new Store<TrashState>({});

  constructor(private readonly deps: TrashDeps) {}

  async load(): Promise<void> {
    try {
      const { items, truncated } = await this.deps.list();
      this.state.update((s) => ({ ...s, items, truncated, error: undefined }));
    } catch (err) {
      this.state.update((s) => ({ ...s, error: `Couldn't list the trash: ${message(err)}` }));
    }
  }

  /** Restores item to the typed path (see restoreTarget); returns its new path, or undefined (the state says why). */
  async restore(item: TrashItem, typed: string): Promise<string | undefined> {
    const to = restoreTarget(typed, item.name, item.kind);
    // A note's name follows the same rule as for New note and Rename (names.ts).
    const problem = item.kind === "file" ? nameProblem(to.name) : undefined;
    if (problem) {
      this.state.update((s) => ({ ...s, error: `Couldn't restore ${item.name}: ${problem}` }));
      return undefined;
    }
    let path: string;
    try {
      path = await this.deps.restore(item.id, to.folder, to.name);
    } catch (err) {
      // Read the list again whatever the failure: the item may be gone (410, restored or
      // removed elsewhere), or restored although the answer was an error (ADR-0024).
      await this.load();
      this.state.update((s) => ({ ...s, error: `Couldn't restore ${item.name}: ${restoreError(err, to)}` }));
      return undefined;
    }
    this.state.update((s) => ({ ...s, items: s.items?.filter((i) => i.id !== item.id), error: undefined }));
    await this.deps.changed(path);
    await this.deps.reveal(path);
    return path;
  }
}

function restoreError(err: unknown, to: { folder: string; name: string }): string {
  if (err instanceof ApiError && err.status === 409) {
    return `${to.folder === "" ? "the vault" : to.folder} already has something named ${to.name}. Type another name.`;
  }
  return message(err);
}
