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
  /** Said once, until dismissed: what setup imported. */
  notice?: string | undefined;
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
    const result = await api.setup(form);
    this.state.set({ phase: "ready", reauth: false, notice: importNotice(result) });
    return result.imported;
  }

  dismissNotice(): void {
    this.state.update((s) => ({ ...s, notice: undefined }));
  }

  async logout(): Promise<void> {
    await this.workspace.saveOpen();
    await api.logout();
    this.state.set({ phase: "login", reauth: false });
  }
}
