import * as api from "./api";
import { DocumentSession } from "./document";
import type { Draft, Drafts } from "./drafts";
import { Store } from "./store";
import { sameFindings, scanSecrets, type SecretFinding } from "./secrets";
import { join, parent, rebase, Tree } from "./tree";
import { fill, localDate, type Template } from "./templates";

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

/** The open note's secret warning (ADR-0020): findings from the last scan, and whether hidden. */
export interface SecretWarning {
  findings: SecretFinding[];
  /** The user dismissed these findings; a different set shows again. */
  dismissed: boolean;
}

const noSecrets: SecretWarning = { findings: [], dismissed: false };

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
  readonly secrets = new Store<SecretWarning>(noSecrets);

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

  /** Closes the open note, whatever it holds (call saveOpen first), and opens path. */
  async open(path: string): Promise<void> {
    this.state.value.open?.doc.dispose();
    this.state.set({});
    this.noteDrafts.set([]);
    this.secrets.set(noSecrets);
    try {
      const note = await api.readNote(path);
      const read = () => this.reader?.() ?? note.content;
      const draft = this.drafts?.forNote(path);
      // The save scans the exact text it sends; it warns and never blocks (ADR-0020).
      const save = (p: string, content: string, base: string) => {
        this.checkSecrets(content);
        return api.saveNote(p, content, base);
      };
      const doc = new DocumentSession(path, note.etag, { save, read, ...(draft && { draft }) });
      this.noteDrafts.set((await this.drafts?.offered(path)) ?? []);
      this.state.set({ open: { doc, initial: note.content } });
      await this.tree.reveal(path); // the tree shows it, however it was opened
    } catch (err) {
      this.state.set({ error: `Couldn't open ${path}: ${message(err)}` });
    }
  }

  /** Creates a note, blank or from a template with its placeholders' values, and opens it. */
  async createNote(folder: string, name: string, from?: { template: Template; values: Record<string, string> }): Promise<void> {
    const file = /\.[a-z]+$/i.test(name) ? name : `${name}.md`;
    const path = join(folder, file);
    const title = name.replace(/\.[a-z]+$/i, "");
    const content = from ? fill(from.template.body, { ...from.values, title, date: localDate(new Date()) }) : `# ${title}\n\n`;
    try {
      await api.createNote(path, content);
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
    const path = recoveredPath(open.doc.path);
    try {
      await api.createNote(path, this.reader?.() ?? open.initial);
    } catch (err) {
      this.state.update((s) => ({ ...s, error: `Couldn't create ${path}: ${message(err)}` }));
      return;
    }
    await this.tree.changed(path);
    await this.open(path); // closing the old note writes its draft, which goes next
    this.drafts?.forNote(open.doc.path).drop();
  }

  /** Reads the drafts to offer again. */
  async refreshOffered(): Promise<void> {
    this.offered.set((await this.drafts?.offered()) ?? []);
  }

  /**
   * Puts a draft of the open note in the editor (put replaces its text) as an edit, which then
   * saves through the normal path (If-Match). The old draft is removed only once this tab's
   * own draft holds the text.
   */
  async recoverDraft(d: Draft, put: (text: string) => void): Promise<void> {
    const open = this.state.value.open;
    if (!open || open.doc.path !== d.path) return;
    put(d.text);
    open.doc.edit();
    await open.doc.writeDraft();
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

  /** Something was pasted into the open note: scan it now, not at the next save. */
  pasted(): void {
    if (this.state.value.open && this.reader) this.checkSecrets(this.reader());
  }

  dismissSecrets(): void {
    this.secrets.update((w) => ({ ...w, dismissed: true }));
  }

  private checkSecrets(text: string): void {
    const findings = scanSecrets(text);
    if (sameFindings(findings, this.secrets.value.findings)) return; // a dismissed warning stays dismissed
    this.secrets.set({ findings, dismissed: false });
  }

  dismissError(): void {
    this.state.update((s) => ({ ...s, error: undefined }));
  }
}

/** Why a move failed, in words: the server's 404 doesn't say which side is missing. */
function moveError(err: unknown, from: string, to: string): string {
  if (!(err instanceof api.ApiError)) return message(err);
  if (err.status === 409) return `${to} already exists`;
  if (err.status === 404) return `${from} or the folder ${parent(to) || "it goes into"} doesn't exist any more`;
  return message(err);
}

function recoveredPath(path: string): string {
  return path.replace(/(\.[a-z]+)?$/i, (ext) => `-recovered${ext || ".md"}`);
}

export function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
