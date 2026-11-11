import * as api from "./api";
import { DocumentSession } from "./document";
import type { Draft, Drafts } from "./drafts";
import { nameProblem, noteFile, noteTitle } from "./names";
import { Store } from "./store";
import { SecretWatch } from "./secretwatch";
import { join, parent, rebase, Tree } from "./tree";
import { fill, localDate, type Template } from "./templates";
import { fileText, type OpenNote, openFile, takeTheirs } from "./open-file";
import { normalize, textFormat } from "./textfile";
import { message, moveError, openError, recoveredPath } from "./workspace-errors";

export type { OpenNote } from "./open-file";
export { message } from "./workspace-errors";

export interface WorkspaceState {
  open?: OpenNote | undefined;
  /** A failed action the user should see (opening, creating or deleting). */
  error?: string | undefined;
}

/** The workspace: the file tree and the open note. */
export class Workspace {
  readonly tree = new Tree(api.listFolder);
  readonly state = new Store<WorkspaceState>({});

  /** The editor registers how to read its text, so saves read it only when they start. */
  reader: (() => string) | undefined;

  /** Local drafts (ADR-0003 §7); set once the tab's esid is claimed, before any note opens. */
  drafts: Drafts | undefined;
  /** Every draft not of this tab, newest first: what the "Unsaved drafts" list shows. */
  readonly offered = new Store<Draft[]>([]);
  /**
   * The open note's drafts from other tabs or earlier loads, for the user to restore or
   * discard. A store of its own: changing it must never replace the open note, which would
   * remount the editor.
   */
  readonly noteDrafts = new Store<Draft[]>([]);
  /** What the last scan of the open note found: it runs on every save and on paste. */
  readonly secrets = new SecretWatch();
  /** Counts open() and close() calls, so a slower, earlier open can tell it was superseded. */
  private opens = 0;
  /** The note createNote just made, until its editor asks (takeCreated). */
  private created: string | undefined;

  useDrafts(drafts: Drafts): void {
    this.drafts = drafts;
  }

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

  /**
   * Closes the open note (call saveOpen first): its session stops saving, and an edit not in a
   * draft yet goes into one while the editor is still there. A loading open() opens nothing.
   */
  close(): void {
    this.opens++;
    this.state.value.open?.doc.dispose();
    this.state.set({});
    this.noteDrafts.set([]);
    this.secrets.clear();
  }

  /**
   * Closes the open note, whatever it holds (call saveOpen first), and opens path. When opens
   * overlap, the last one wins: an earlier one that finishes later changes nothing.
   */
  async open(path: string): Promise<void> {
    this.close();
    const seq = this.opens;
    try {
      const note = await api.readNote(path);
      if (seq !== this.opens) return;
      const format = textFormat(note.content); // refuses a file a save would change
      const offered = (await this.drafts?.offered(path)) ?? [];
      if (seq !== this.opens) return;
      // Only the editor holds the edits: with it gone, the session neither saves nor drafts.
      // Saves and drafts hold the file's text: its byte order mark and line endings put back.
      const read = () => {
        const text = this.reader?.();
        return text === undefined ? undefined : fileText(opened, text);
      };
      const draft = this.drafts?.forNote(path);
      // The save scans the exact text it sends; it warns and never blocks (ADR-0020).
      const save = (p: string, content: string, base: string) => {
        this.secrets.check(content);
        return api.saveNote(p, content, base);
      };
      const doc = new DocumentSession(path, note.etag, { save, read, ...(draft && { draft }) });
      const opened = openFile(doc, note.content, format);
      this.noteDrafts.set(offered);
      this.state.set({ open: opened });
      await this.refreshOffered(); // the note closed to open this one may have left a draft
      await this.tree.reveal(path); // the tree shows it, however it was opened
    } catch (err) {
      if (seq === this.opens) this.state.set({ error: openError(path, err) });
    }
  }

  /**
   * Creates a note, blank or from a template with its placeholders' values, and opens it.
   * False when it wasn't created (a refused name, an existing note): the error says why.
   */
  async createNote(folder: string, name: string, from?: { template: Template; values: Record<string, string> }): Promise<boolean> {
    const path = join(folder, noteFile(name));
    const problem = nameProblem(path);
    if (problem) {
      this.state.update((s) => ({ ...s, error: problem }));
      return false;
    }
    const title = noteTitle(name);
    const content = from ? fill(from.template.body, { ...from.values, title, date: localDate(new Date()) }) : `# ${title}\n\n`;
    try {
      await api.createNote(path, content);
    } catch (err) {
      const why = err instanceof api.ApiError && err.status === 409 ? "it already exists" : message(err);
      this.state.update((s) => ({ ...s, error: `Couldn't create ${path}: ${why}` }));
      return false;
    }
    await this.tree.changed(path);
    this.created = path;
    if (await this.saveOpen()) await this.open(path);
    else this.created = undefined;
    return true;
  }

  /**
   * Whether path is the note createNote just opened (its editor puts the cursor after the
   * text it starts with). True once: a later open of the same note is an ordinary one.
   */
  takeCreated(path: string): boolean {
    const created = this.created === path;
    this.created = undefined;
    return created;
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

  /**
   * Renames or moves a note or folder; never onto an existing one. An open note inside it is
   * saved first and reopened at its new path; if it can't be saved, nothing moves. True when
   * it moved.
   */
  async move(from: string, to: string): Promise<boolean> {
    // Never creates folders on the way: a move has no contract for that (CLAUDE.md rule 4).
    const into = parent(to);
    if (await this.tree.missing(into)) {
      this.state.update((s) => ({ ...s, error: `Folder ${into} doesn't exist. Create it first with New folder.` }));
      return false;
    }
    const open = this.state.value.open;
    const reopen = open && rebase(open.doc.path, from, to);
    if (open && reopen) {
      if (!(await this.saveOpen())) {
        this.state.update((s) => ({ ...s, error: `Save or resolve ${open.doc.path} before moving it.` }));
        return false;
      }
      // Closed before the move, so no autosave can target the old path.
      open.doc.dispose();
      this.state.set({});
    }
    try {
      await api.move(from, to);
    } catch (err) {
      const why = moveError(err, from, to);
      if (open && reopen) await this.open(open.doc.path); // reopening clears the error
      this.state.update((s) => ({ ...s, error: `Couldn't move ${from}: ${why}` }));
      return false;
    }
    await this.tree.moved(from, to);
    if (reopen) await this.open(reopen);
    return true;
  }

  /**
   * Replaces the open note's text with its version from commit, through the save path
   * (ADR-0020): the open note is saved first, and the server replaces it only if it is still
   * that saved version (If-Match). The note is then reopened with what the server holds.
   */
  async restore(commit: string): Promise<boolean> {
    const open = this.state.value.open;
    if (!open) return false;
    const path = open.doc.path;
    if (!(await this.saveOpen())) {
      this.state.update((s) => ({ ...s, error: `Save or resolve ${path} before restoring a version.` }));
      return false;
    }
    // Closed first, so no autosave races the restore.
    open.doc.dispose();
    this.state.set({});
    let failed: unknown;
    try {
      await api.restore(path, commit, open.doc.etag);
    } catch (err) {
      failed = err;
    }
    await this.open(path); // reopening clears the error
    if (failed !== undefined) {
      const why =
        failed instanceof api.ApiError && failed.status === 412
          ? "it changed in the meantime, so nothing was replaced"
          : message(failed);
      this.state.update((s) => ({ ...s, error: `Couldn't restore ${path}: ${why}` }));
      return false;
    }
    return true;
  }

  /** Moves a note or folder to the trash; true when it went. An open note inside it is closed unsaved. */
  async trash(path: string, kind: "file" | "folder"): Promise<boolean> {
    const open = this.state.value.open;
    if (open && (open.doc.path === path || open.doc.path.startsWith(`${path}/`))) {
      open.doc.dispose();
      this.state.set({});
    }
    let ok = true;
    try {
      await api.trash(path, kind);
    } catch (err) {
      ok = false;
      this.state.update((s) => ({ ...s, error: `Couldn't delete ${path}: ${message(err)}` }));
    }
    await this.tree.changed(path);
    return ok;
  }

  /**
   * Saves the open note's text as a new note next to it ("x.md" → "x-recovered.md"), for a
   * note deleted elsewhere: autosave never recreates it (ADR-0003 §7), but the user can.
   */
  async saveAsNew(): Promise<void> {
    const open = this.state.value.open;
    if (!open) return;
    const path = recoveredPath(open.doc.path);
    try {
      await api.createNote(path, this.currentText() ?? open.initial);
    } catch (err) {
      this.state.update((s) => ({ ...s, error: `Couldn't create ${path}: ${message(err)}` }));
      return;
    }
    await this.tree.changed(path);
    await this.open(path); // closing the old note writes its draft, which goes next
    open.doc.dropDraft();
  }

  /** Reads the drafts to offer again. */
  async refreshOffered(): Promise<void> {
    this.offered.set((await this.drafts?.offered()) ?? []);
  }

  /**
   * Puts a draft of the open note in the editor (put replaces its text) as an edit, which then
   * saves through the normal path. A draft is based on the version it was edited from: its
   * save sends If-Match with that base, so a draft older than the saved note gets a 412 and the
   * conflict view, never a silent overwrite. The old draft is removed only once this tab's own
   * draft holds the text.
   */
  async recoverDraft(d: Draft, put: (text: string) => void): Promise<void> {
    const open = this.state.value.open;
    if (!open || open.doc.path !== d.path) return;
    if (d.base !== open.doc.etag) {
      await open.doc.flush(); // no save in flight may move the base past the draft's
      if (this.state.value.open !== open) return;
    }
    put(normalize(d.text)); // drafts hold the file's text
    open.doc.rebase(d.base);
    open.doc.edit();
    if (!(await open.doc.writeDraft())) return; // no other copy yet: the old draft stays
    await this.discardDraft(d);
  }

  /** Deletes a draft the user chose to discard. */
  async discardDraft(d: Draft): Promise<void> {
    await this.drafts?.discard(d);
    await this.refreshOffered();
    this.noteDrafts.update((all) => all.filter((x) => x.esid !== d.esid || x.path !== d.path));
  }

  /** Saves a draft as a new note ("x.md" → "x-recovered.md"); the draft goes only once saved. */
  async saveDraftAsNew(d: Draft): Promise<void> {
    const path = recoveredPath(d.path);
    try {
      await api.createNote(path, d.text);
    } catch (err) {
      const why = err instanceof api.ApiError && err.status === 409 ? `${path} already exists` : message(err);
      this.state.update((s) => ({ ...s, error: `Couldn't save the draft as ${path}: ${why}` }));
      return;
    }
    await this.drafts?.discard(d);
    await this.refreshOffered();
    await this.tree.changed(path);
    if (await this.saveOpen()) await this.open(path);
  }

  /** The open file's text as a save would write it; undefined with no editor. */
  currentText(): string | undefined {
    const open = this.state.value.open;
    const text = this.reader?.();
    return open && text !== undefined ? fileText(open, text) : undefined;
  }

  /** The conflict's other version for the editor (open-file.ts); undefined, with why, if it can't be. */
  takeTheirs(): string | undefined {
    const open = this.state.value.open;
    const theirs = open?.doc.state.value.conflict?.content;
    if (!open || theirs === undefined) return undefined;
    try {
      return takeTheirs(open, theirs);
    } catch (err) {
      this.state.update((s) => ({ ...s, error: `Couldn't use the other version: ${message(err)}.` }));
      return undefined;
    }
  }

  /** Something was pasted into the open note: scan it now, not at the next save. */
  pasted(): void {
    if (this.state.value.open && this.reader) this.secrets.check(this.reader());
  }

  dismissError(): void {
    this.state.update((s) => ({ ...s, error: undefined }));
  }
}
