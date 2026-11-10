import type { GitStatus } from "./api";
import { Store } from "./store";
import { message } from "./workspace";

export interface GitState {
  status?: GitStatus | undefined;
  busy: boolean;
  /** The short hash of the last commit made here. */
  committed?: string | undefined;
  error?: string | undefined;
}

export interface GitDeps {
  status(): Promise<GitStatus>;
  commit(message: string): Promise<string>;
  removeLocks(): Promise<void>;
  /** Saves the open note; false when some edits couldn't be saved. */
  saveOpen(): Promise<boolean>;
}

/** The vault's git working tree: what changed since the last commit, and committing it. */
export class Git {
  readonly state = new Store<GitState>({ busy: false });

  constructor(private readonly deps: GitDeps) {}

  /** Starts a fresh look: clears the last commit's messages and reads the status. */
  async open(): Promise<void> {
    this.state.update((s) => ({ ...s, committed: undefined, error: undefined }));
    await this.refresh();
  }

  async refresh(): Promise<void> {
    try {
      const status = await this.deps.status();
      this.state.update((s) => ({ ...s, status }));
    } catch (err) {
      this.state.update((s) => ({ ...s, error: `Couldn't read the git status: ${message(err)}` }));
    }
  }

  /** Commits every change, after saving the open note so its last edits are in it. */
  async commit(msg: string): Promise<boolean> {
    this.state.update((s) => ({ ...s, busy: true, error: undefined, committed: undefined }));
    try {
      if (!(await this.deps.saveOpen())) {
        this.state.update((s) => ({ ...s, error: "Some edits to the open note aren't saved. Save or resolve them first." }));
        return false;
      }
      const hash = await this.deps.commit(msg);
      this.state.update((s) => ({ ...s, committed: hash.slice(0, 7) }));
      return true;
    } catch (err) {
      this.state.update((s) => ({ ...s, error: `Couldn't commit: ${message(err)}` }));
      return false;
    } finally {
      await this.refresh();
      this.state.update((s) => ({ ...s, busy: false }));
    }
  }

  /** Removes the git locks. Only after the user confirms no other git command is running. */
  async removeLocks(): Promise<void> {
    this.state.update((s) => ({ ...s, busy: true, error: undefined }));
    try {
      await this.deps.removeLocks();
    } catch (err) {
      this.state.update((s) => ({ ...s, error: `Couldn't remove the lock: ${message(err)}` }));
    } finally {
      await this.refresh();
      this.state.update((s) => ({ ...s, busy: false }));
    }
  }
}
