import * as api from "./api";
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

  constructor() {
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
    const imported = await api.setup(form);
    this.state.set({ phase: "ready", reauth: false });
    return imported;
  }

  async logout(): Promise<void> {
    await this.workspace.saveOpen();
    await api.logout();
    this.state.set({ phase: "login", reauth: false });
  }
}
