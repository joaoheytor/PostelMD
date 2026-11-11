import * as api from "./api";
import type { DocState } from "./document";
import type { Drafts } from "./drafts";
import { Git } from "./git";
import { Store } from "./store";
import { message, Workspace } from "./workspace";

export type Phase = "loading" | "setup" | "login" | "ready" | "failed";

export interface AppState {
  phase: Phase;
  /** The session expired while working: ask for the password over the open workspace. */
  reauth: boolean;
  error?: string | undefined;
  /** Said once, until dismissed: what setup imported. */
  notice?: string | undefined;
  /** Logging out found edits it couldn't save: the user chooses to stay or keep a draft. */
  leaving?: Leaving | undefined;
  /** Logging out is saving the open note first. */
  loggingOut?: boolean | undefined;
}

export interface Leaving {
  path: string;
  /** Why the edits aren't saved, as a sentence. */
  why: string;
  /** Keeping them as a local draft failed too: the only safe choice is to stay. */
  draftFailed: boolean;
  /** Drafts outlive the page: without that, a draft can't outlive the logout. */
  persistent: boolean;
}

const files = (n: number) => `${n} file${n === 1 ? "" : "s"}`;

/**
 * What setup's initial import did, or nothing if it imported nothing. ADR-0020: the import
 * commits the files as they are, without a secret check, and leaves out files over 50 MB.
 */
export function importNotice({ imported, skipped }: api.SetupResult): string | undefined {
  const parts: string[] = [];
  if (imported > 0) {
    parts.push(
      `Imported ${files(imported)} from your notes folder as the first commit. ` +
        "Check them for secrets before you push this repository anywhere.",
    );
  }
  if (skipped > 0) parts.push(`${files(skipped)} over 50 MB ${skipped === 1 ? "was" : "were"} left out of git.`);
  return parts.length > 0 ? parts.join(" ") : undefined;
}

/** The app's top-level state: which screen shows, and the workspace once logged in. */
export class App {
  readonly state = new Store<AppState>({ phase: "loading", reauth: false });
  readonly workspace = new Workspace();
  readonly git = new Git({
    status: api.gitStatus,
    readFile: api.readFileBytes,
    commit: api.commit,
    removeLocks: api.removeGitLocks,
    saveOpen: () => this.workspace.saveOpen(),
  });

  /** navigate loads another page: after a logout, a fresh one keeps nothing of the vault. */
  constructor(private readonly navigate: (url: string) => void = (url) => location.replace(url)) {
    api.setUnauthorizedHandler(() => {
      if (this.state.value.phase === "ready") this.state.update((s) => ({ ...s, reauth: true }));
    });
  }

  /** drafts opens this tab's drafts; it runs before any note opens (ADR-0003 §7). */
  async boot(drafts: () => Promise<Drafts>): Promise<void> {
    try {
      this.workspace.useDrafts(await drafts());
      this.state.set({ phase: await api.sessionState(), reauth: false });
    } catch (err) {
      this.state.set({ phase: "failed", reauth: false, error: `PostelMD didn't answer: ${message(err)}` });
    }
  }

  /** Logs in; after an expired session, saves the open note's pending edits. */
  async login(password: string): Promise<void> {
    await api.login(password);
    const reauth = this.state.value.reauth;
    this.state.set({ phase: "ready", reauth: false });
    if (reauth) await this.workspace.saveOpen();
  }

  /** First-run setup; returns how many files the initial commit imported. */
  async setup(form: api.SetupForm): Promise<number> {
    const result = await api.setup(form);
    this.state.set({ phase: "ready", reauth: false, notice: importNotice(result) });
    return result.imported;
  }

  dismissNotice(): void {
    this.state.update((s) => ({ ...s, notice: undefined }));
  }

  /**
   * Logs out once the open note is saved; loggingOut says so meanwhile. If its edits can't be
   * saved, nothing happens yet: leaving asks the user to stay or to log out keeping them as a
   * local draft. Never rejects.
   */
  async logout(): Promise<void> {
    if (this.state.value.loggingOut) return;
    this.state.update((s) => ({ ...s, loggingOut: true }));
    const saved = await this.workspace.saveOpen();
    const doc = this.workspace.state.value.open?.doc;
    if (!saved && doc) {
      const persistent = !!this.workspace.drafts?.persistent;
      const leaving = { path: doc.path, why: unsavedWhy(doc.state.value), draftFailed: doc.draftFailed.value, persistent };
      this.state.update((s) => ({ ...s, loggingOut: false, leaving }));
      return;
    }
    await this.leave();
  }

  /**
   * The user chose to log out with unsaved edits: only once a local draft that outlives the
   * page holds them.
   */
  async logoutKeepingDraft(): Promise<void> {
    const doc = this.workspace.state.value.open?.doc;
    if (!this.workspace.drafts?.persistent) return;
    if (doc && !(await doc.writeDraft())) {
      this.state.update((s) => (s.leaving ? { ...s, leaving: { ...s.leaving, draftFailed: true } } : s));
      return;
    }
    await this.leave();
  }

  /** The user chose to stay with the unsaved edits. */
  stay(): void {
    this.state.update((s) => ({ ...s, leaving: undefined }));
  }

  /**
   * Ends the session, then closes the open note, so no save or draft outlives its editor, and
   * loads a fresh page: the tree and git state don't stay in memory. Drafts stay in storage.
   * If the server can't be reached, nothing changes but the error, and the user can try again.
   */
  private async leave(): Promise<void> {
    try {
      await api.logout();
    } catch (err) {
      this.state.update((s) => ({ ...s, loggingOut: false, leaving: undefined }));
      this.workspace.state.update((s) => ({ ...s, error: `Couldn't log out: ${message(err)}` }));
      return;
    }
    this.workspace.close();
    this.state.set({ phase: "login", reauth: false });
    await this.workspace.drafts?.idle(); // closing wrote the last edits' draft: the reload waits for it
    this.navigate("/");
  }
}

/** Why the open note's edits aren't saved, as a sentence, for the logout question. */
export function unsavedWhy(st: DocState): string {
  switch (st.status) {
    case "conflict":
      return "The note was changed somewhere else too.";
    case "gone":
      return "The note was deleted or moved outside this editor.";
    case "stalled":
      return "The server hasn't answered for 15 seconds.";
    default: {
      const what = (st.message ?? "the server didn't take them").replace(/\.$/, "");
      return `${what.charAt(0).toUpperCase()}${what.slice(1)}.`;
    }
  }
}
