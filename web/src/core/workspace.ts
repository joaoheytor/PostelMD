import * as api from "./api";
import type { Draft, Drafts } from "./drafts";
import { nameProblem, noteFile, noteTitle } from "./names";
import { Store } from "./store";
import { SecretWatch } from "./secretwatch";
import { join, Tree } from "./tree";
import { fill, localDate, type Template } from "./templates";
import { moveItem } from "./move";
import { checkOpens, fileText, type OpenNote, openSession, takeTheirs } from "./open-file";
import { restoreVersion } from "./restore";
import * as recovery from "./workspace-drafts";
import { draftsUnchecked, message, openFailure, unsavedMeanwhile } from "./workspace-errors";

export type { OpenNote } from "./open-file";
export { message } from "./workspace-errors";

export interface WorkspaceState {
  open?: OpenNote | undefined;
  /** A failed action the user should see (opening, creating or deleting). */
  error?: string | undefined;
  /** The file the error refused for its line endings: the user may convert them (open(…, true)). */
  convert?: string | undefined;
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
   * Returns whether no note was opened or closed since, for a reopen after a request.
   */
  close(): () => boolean {
    const seq = ++this.opens;
    // dispose() first, while state.open is still this session: its closing draft reads the
    // editor only while the session is current. Swapping these two lines drops that draft.
    this.state.value.open?.doc.dispose();
    this.state.set({});
    this.noteDrafts.set([]);
    this.secrets.clear();
    return () => seq === this.opens;
  }

  /**
   * Opens path in place of the open note, whatever it holds (call saveOpen first); convert (the
   * user's click only) makes its line endings LF (open-file.ts). When opens overlap, the last
   * one wins: an earlier one that finishes later changes nothing.
   *
   * The open note closes only once path is read and can open: if it can't, the note stays open
   * and saving, and only the error shows (#134). Edits typed while path loads are saved first;
   * if one can't be, the note stays and the error says so. So continuous typing holds the open
   * back until a save catches up, and a close() meanwhile (a trash, move or restore of the open
   * note, a logout) cancels it. The open note itself, saved, is kept as it is: a read of it could
   * be older than a save landing meanwhile. Reloaded (converted, or edits the user agreed to
   * leave, which go to a draft offered again), it is read again if a save landed meanwhile.
   */
  async open(path: string, convert = false): Promise<void> {
    const seq = ++this.opens;
    let current = () => seq === this.opens;
    const prev = this.state.value.open;
    this.fail(undefined); // a refusal again is announced again, and no old one shows meanwhile
    const wasClean = !prev?.doc.dirty; // an unsaved edit already there, the user agreed to leave
    if (prev?.doc.path === path && wasClean && !convert) return; // nothing to reload: kept as it is
    const etag = prev?.doc.etag;
    try {
      const note = await api.readNote(path);
      checkOpens(note, convert);
      if (!current()) return;
      if (prev && wasClean && prev.doc.dirty) {
        await prev.doc.flush(); // saves again for edits made during a save: dirty only if one failed
        if (!current()) return;
        if (prev.doc.dirty) return this.fail(unsavedMeanwhile(path, prev.doc.path));
      }
      if (prev?.doc.path === path && prev.doc.etag !== etag) return await this.open(path, convert); // read before a save
      // In the same task as the checks above: no edit comes in between. The editor goes with it.
      current = this.close();
      const offered = (await this.drafts?.offered(path)) ?? [];
      if (!current()) return;
      this.noteDrafts.set(offered);
      this.state.set({ open: this.session(path, note, convert) });
    } catch (err) {
      // Before close(), the open note stays: only the error is added (fail cleared convert).
      if (current()) this.state.update((s) => ({ ...s, ...openFailure(path, err) }));
      return;
    }
    // The session is published and closes with the note: nothing from here may replace it.
    // The note closed to open this one may have left a draft; the tree shows this one.
    await this.refreshOffered().catch(() => current() && this.fail(draftsUnchecked));
    await this.tree.reveal(path);
  }

  /** The session of path, read as note: checkOpens passed, so nothing here throws. */
  private session(path: string, note: api.Note, convert: boolean): OpenNote {
    return openSession(path, note, convert, {
      // Only the editor holds the edits: with it gone, the session neither saves nor drafts.
      reader: () => this.reader?.(),
      current: (o) => this.state.value.open === o,
      // The save scans the exact text it sends; it warns and never blocks (ADR-0020).
      save: (p, content, base) => {
        this.secrets.check(content);
        return api.saveNote(p, content, base);
      },
      draft: this.drafts?.forNote(path),
    });
  }

  /**
   * Creates a note, blank or from a template with its placeholders' values, and opens it.
   * False when it wasn't created (a refused name, an existing note): the error says why.
   */
  async createNote(folder: string, name: string, from?: { template: Template; values: Record<string, string> }): Promise<boolean> {
    const path = join(folder, noteFile(name));
    const problem = nameProblem(path);
    if (problem) {
      this.fail(problem);
      return false;
    }
    const title = noteTitle(name);
    const content = from ? fill(from.template.body, { ...from.values, title, date: localDate(new Date()) }) : `# ${title}\n\n`;
    try {
      await api.createNote(path, content);
    } catch (err) {
      const why = err instanceof api.ApiError && err.status === 409 ? "it already exists" : message(err);
      this.fail(`Couldn't create ${path}: ${why}`);
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
      this.fail(`Couldn't create ${path}: ${message(err)}`);
      return;
    }
    await this.tree.changed(path);
  }

  /** Renames or moves a note or folder, never onto an existing one (move.ts). True when it moved. */
  async move(from: string, to: string): Promise<boolean> {
    const r = await moveItem(this, from, to);
    if (r !== true) this.fail(r);
    return r === true;
  }

  /** Replaces the open note's text with its version from commit, through the save path (restore.ts). */
  async restore(commit: string): Promise<boolean> {
    const r = await restoreVersion(this, commit);
    if (typeof r === "string") this.fail(r);
    return r === true;
  }

  /** Moves a note or folder to the trash; true when it went. An open note inside it is closed unsaved. */
  async trash(path: string, kind: "file" | "folder"): Promise<boolean> {
    const open = this.state.value.open;
    // close()'s "still current" check is unused on purpose: a trashed note is never reopened.
    if (open && (open.doc.path === path || open.doc.path.startsWith(`${path}/`))) this.close();
    let ok = true;
    try {
      await api.trash(path, kind);
    } catch (err) {
      ok = false;
      this.fail(`Couldn't delete ${path}: ${message(err)}`);
    }
    await this.tree.changed(path);
    return ok;
  }

  // Drafts and recovered copies (ADR-0003 §7): each is documented in workspace-drafts.ts.

  /** Saves the open note's text as "x-recovered.md" next to it, and opens that (workspace-drafts.ts). */
  async saveAsNew(): Promise<void> {
    const why = await recovery.saveAsNew(this);
    if (why) this.fail(why);
  }

  /** Reads the drafts to offer again (workspace-drafts.ts). */
  async refreshOffered(): Promise<void> {
    await recovery.refreshOffered(this);
  }

  /** Puts a draft of the open note in the editor as an edit; an older one gets the conflict view (workspace-drafts.ts). */
  async recoverDraft(d: Draft, put: (text: string) => void): Promise<void> {
    await recovery.recoverDraft(this, d, put);
  }

  /** Deletes a draft the user chose to discard (workspace-drafts.ts). */
  async discardDraft(d: Draft): Promise<void> {
    await recovery.discardDraft(this, d);
  }

  /** Saves a draft as "x-recovered.md"; the draft goes only once saved (workspace-drafts.ts). */
  async saveDraftAsNew(d: Draft): Promise<void> {
    const why = await recovery.saveDraftAsNew(this, d);
    if (why) this.fail(why);
  }

  /** The open file's text as a save would write it; undefined with no editor. */
  currentText(): string | undefined {
    const open = this.state.value.open;
    const text = this.reader?.();
    return open && text !== undefined ? fileText(open, text) : undefined;
  }

  /**
   * Puts the conflict's other version in the editor (open-file.ts). Converted (the user
   * confirmed), its LF lines are an edit saved over its ETag. False when it can't be taken
   * (not text; or not convert and line endings a save would change): the conflict stays.
   */
  takeTheirs(put: (text: string) => void, convert = false): boolean {
    const open = this.state.value.open;
    const c = open?.doc.state.value.conflict;
    if (!open || !c || c.binary) return false;
    try {
      put(takeTheirs(open, c.content, convert));
    } catch (err) {
      this.fail(`Couldn't use the other version: ${message(err)}.`);
      return false;
    }
    if (convert) open.doc.edit();
    return true;
  }

  /** Something was pasted into the open note: scan it now, not at the next save. */
  pasted(): void {
    if (this.state.value.open && this.reader) this.secrets.check(this.reader());
  }

  dismissError(): void {
    this.fail(undefined);
  }

  /** Shows error (or none), and drops a stale offer to convert a file's line endings. */
  private fail(error: string | undefined): void {
    this.state.update((s) => ({ ...s, error, convert: undefined }));
  }
}
