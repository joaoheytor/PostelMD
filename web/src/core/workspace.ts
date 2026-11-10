import * as api from "./api";
import { DocumentSession } from "./document";
import { Store } from "./store";
import { join, Tree } from "./tree";

/** The note open in the editor, with the text it was loaded with. */
export interface OpenNote {
  doc: DocumentSession;
  initial: string;
}

export interface WorkspaceState {
  open?: OpenNote | undefined;
  /** A failed action the user should see (opening, creating or deleting). */
  error?: string | undefined;
}

/** Note files the editor opens; anything else is listed but not opened as text. */
export function isNote(name: string): boolean {
  return /\.(md|markdown|txt)$/i.test(name);
}

/** The workspace: the file tree and the open note. */
export class Workspace {
  readonly tree = new Tree(api.listFolder);
  readonly state = new Store<WorkspaceState>({});

  /** The editor registers how to read its text, so saves read it only when they start. */
  reader: (() => string) | undefined;

  /**
   * Saves the open note now. False means some edits couldn't be saved (a conflict, a
   * deleted note, an error): the UI asks before leaving them.
   */
  async saveOpen(): Promise<boolean> {
    const doc = this.state.value.open?.doc;
    if (!doc) return true;
    await doc.flush();
    return !doc.dirty;
  }

  /** Closes the open note, whatever it holds (call saveOpen first), and opens path. */
  async open(path: string): Promise<void> {
    this.state.value.open?.doc.dispose();
    this.state.set({});
    try {
      const note = await api.readNote(path);
      const read = () => this.reader?.() ?? note.content;
      const doc = new DocumentSession(path, note.etag, { save: api.saveNote, read });
      this.state.set({ open: { doc, initial: note.content } });
    } catch (err) {
      this.state.set({ error: `Couldn't open ${path}: ${message(err)}` });
    }
  }

  async createNote(folder: string, name: string): Promise<void> {
    const file = /\.[a-z]+$/i.test(name) ? name : `${name}.md`;
    const path = join(folder, file);
    try {
      await api.createNote(path, `# ${name.replace(/\.[a-z]+$/i, "")}\n\n`);
    } catch (err) {
      this.state.update((s) => ({ ...s, error: `Couldn't create ${path}: ${message(err)}` }));
      return;
    }
    await this.tree.changed(path);
    if (await this.saveOpen()) await this.open(path);
  }

  async createFolder(folder: string, name: string): Promise<void> {
    const path = join(folder, name);
    try {
      await api.createFolder(path);
    } catch (err) {
      this.state.update((s) => ({ ...s, error: `Couldn't create ${path}: ${message(err)}` }));
      return;
    }
    await this.tree.changed(path);
  }

  /** Moves a note or folder to the trash. An open note inside it is closed unsaved. */
  async trash(path: string, kind: "file" | "folder"): Promise<void> {
    const open = this.state.value.open;
    if (open && (open.doc.path === path || open.doc.path.startsWith(`${path}/`))) {
      open.doc.dispose();
      this.state.set({});
    }
    try {
      await api.trash(path, kind);
    } catch (err) {
      this.state.update((s) => ({ ...s, error: `Couldn't delete ${path}: ${message(err)}` }));
    }
    await this.tree.changed(path);
  }

  /**
   * Saves the open note's text as a new note next to it ("x.md" → "x-recovered.md"), for a
   * note deleted elsewhere: autosave never recreates it (ADR-0003 §7), but the user can.
   */
  async saveAsNew(): Promise<void> {
    const open = this.state.value.open;
    if (!open) return;
    const path = open.doc.path.replace(/(\.[a-z]+)?$/i, (ext) => `-recovered${ext || ".md"}`);
    try {
      await api.createNote(path, this.reader?.() ?? open.initial);
    } catch (err) {
      this.state.update((s) => ({ ...s, error: `Couldn't create ${path}: ${message(err)}` }));
      return;
    }
    await this.tree.changed(path);
    await this.open(path);
  }

  dismissError(): void {
    this.state.update((s) => ({ ...s, error: undefined }));
  }
}

export function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
