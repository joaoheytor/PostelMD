import type { Entry } from "./api";
import { Store } from "./store";

export interface TreeState {
  /** Loaded folders by path ("" is the vault root). */
  folders: ReadonlyMap<string, readonly Entry[]>;
  expanded: ReadonlySet<string>;
  error?: string | undefined;
}

/**
 * Note files, by name (.md, .markdown, .txt): they open in the Markdown mode, with the
 * preview. Any other UTF-8 text file opens too, in its own mode (filetype.ts).
 */
export function isNote(name: string): boolean {
  return /\.(md|markdown|txt)$/i.test(name);
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

/** The folders above path, from the vault root down ("a/b/c.md" → "", "a", "a/b"). */
export function ancestors(path: string): string[] {
  const parts = path.split("/").slice(0, -1);
  return ["", ...parts.map((_, i) => parts.slice(0, i + 1).join("/"))];
}

/** Where path is after from moved to to, or undefined if the move didn't touch it. */
export function rebase(path: string, from: string, to: string): string | undefined {
  if (path === from) return to;
  return path.startsWith(`${from}/`) ? to + path.slice(from.length) : undefined;
}

/**
 * The vault path a typed move target means: trimmed, without leading or trailing slashes. A
 * note keeps its extension unless the new name already ends in it or in a note's ("a/b.md",
 * "c" → "c.md"; "nginx.conf" → "nginx.conf.md": a dot alone isn't an extension, as for a new
 * note). Empty means nothing usable was typed.
 */
export function moveTarget(from: string, typed: string, kind: "file" | "folder"): string {
  const to = typed.trim().replace(/^\/+|\/+$/g, "");
  if (to === "" || kind === "folder") return to;
  const ext = /\.[a-z]+$/i.exec(from)?.[0] ?? "";
  return isNote(to) || to.toLowerCase().endsWith(ext.toLowerCase()) ? to : to + ext;
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
      const why = err instanceof Error ? err.message : String(err);
      this.state.update((s) => ({ ...s, error: `Couldn't list ${path || "the vault"}: ${why}` }));
    }
  }

  /**
   * Shows path's entry: expands every folder above it, loading those not loaded yet. Folders
   * the user collapsed elsewhere stay as they are.
   */
  async reveal(path: string): Promise<void> {
    const above = ancestors(path);
    this.state.update((s) => ({ ...s, expanded: new Set([...s.expanded, ...above]) }));
    await Promise.all(above.filter((f) => !this.state.value.folders.has(f)).map((f) => this.load(f)));
  }

  /**
   * Whether folder surely doesn't exist: a listing on the way to it lacks the next folder, even
   * after loading that listing again (something may have created it since). False when it
   * exists, or when no loaded listing can tell.
   */
  async missing(folder: string): Promise<boolean> {
    const gap = this.gap(folder);
    if (gap === undefined) return false;
    await this.load(gap);
    return this.state.value.error === undefined && this.gap(folder) !== undefined; // a failed listing tells nothing
  }

  /** The loaded folder whose listing lacks the next folder on the way to folder, if any. */
  private gap(folder: string): string | undefined {
    if (folder === "") return undefined;
    let at = "";
    for (const name of folder.split("/")) {
      const entries = this.state.value.folders.get(at);
      if (!entries) return undefined;
      if (!entries.some((e) => e.kind === "folder" && e.name === name)) return at;
      at = join(at, name);
    }
    return undefined;
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

  /**
   * After a move: forgets the moved folder and everything under it (stale listings), then
   * reloads both parents. The new location starts collapsed.
   */
  async moved(from: string, to: string): Promise<void> {
    const gone = (p: string) => rebase(p, from, to) !== undefined;
    this.state.update((s) => ({
      ...s,
      folders: new Map([...s.folders].filter(([p]) => !gone(p))),
      expanded: new Set([...s.expanded].filter((p) => !gone(p))),
    }));
    await Promise.all([...new Set([parent(from), parent(to)])].map((f) => this.reload(f)));
  }

  /** Reloads the folder that holds path, expanding it. */
  async changed(path: string): Promise<void> {
    await this.reload(parent(path));
  }

  private async reload(folder: string): Promise<void> {
    this.state.update((s) => ({ ...s, expanded: new Set(s.expanded).add(folder) }));
    await this.load(folder);
  }
}
