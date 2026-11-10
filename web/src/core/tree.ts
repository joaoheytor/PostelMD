import type { Entry } from "./api";
import { Store } from "./store";

export interface TreeState {
  /** Loaded folders by path ("" is the vault root). */
  folders: ReadonlyMap<string, readonly Entry[]>;
  expanded: ReadonlySet<string>;
  error?: string | undefined;
}

/** Joins a folder path and an entry name ("" + "a.md" → "a.md"). */
export function join(folder: string, name: string): string {
  return folder === "" ? name : `${folder}/${name}`;
}

/** The folder part of a path ("a/b.md" → "a", "b.md" → ""). */
export function parent(path: string): string {
  const i = path.lastIndexOf("/");
  return i < 0 ? "" : path.slice(0, i);
}

/** The file tree: folders load when first expanded and reload after a change in them. */
export class Tree {
  readonly state = new Store<TreeState>({ folders: new Map(), expanded: new Set([""]) });

  constructor(private readonly list: (path: string) => Promise<Entry[]>) {}

  async load(path: string): Promise<void> {
    try {
      const entries = await this.list(path);
      this.state.update((s) => ({ ...s, folders: new Map(s.folders).set(path, entries), error: undefined }));
    } catch (err) {
      this.state.update((s) => ({ ...s, error: `Couldn't list ${path || "the vault"}: ${String(err)}` }));
    }
  }

  async toggle(path: string): Promise<void> {
    const open = !this.state.value.expanded.has(path);
    this.state.update((s) => {
      const expanded = new Set(s.expanded);
      if (open) expanded.add(path);
      else expanded.delete(path);
      return { ...s, expanded };
    });
    if (open && !this.state.value.folders.has(path)) await this.load(path);
  }

  /** Reloads the folder that holds path, expanding it. */
  async changed(path: string): Promise<void> {
    const folder = parent(path);
    this.state.update((s) => ({ ...s, expanded: new Set(s.expanded).add(folder) }));
    await this.load(folder);
  }
}
