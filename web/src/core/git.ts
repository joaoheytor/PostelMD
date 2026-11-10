import { ApiError, type GitStatus, type ListedChange } from "./api";
import { type FileScan, type ReadFile, scanChanges, withFindings } from "./commit-scan";
import { Store } from "./store";
import { message } from "./workspace";

export interface GitState {
  status?: GitStatus | undefined;
  /** The secret check of the listed changes; undefined until it has run on this status. */
  scan?: FileScan[] | undefined;
  /** Reading and checking the listed changes. */
  scanning: boolean;
  busy: boolean;
  /** The short hash of the last commit made here. */
  committed?: string | undefined;
  /** The list changed under the user: they review it again. */
  notice?: string | undefined;
  error?: string | undefined;
}

export interface GitDeps {
  status(): Promise<GitStatus>;
  /** Reads a file's current bytes for the secret check; undefined if it's gone. */
  readFile: ReadFile;
  commit(message: string, changes: ListedChange[]): Promise<string>;
  removeLocks(): Promise<void>;
  /** Saves the open note; false when some edits couldn't be saved. */
  saveOpen(): Promise<boolean>;
}

export const changedNotice = "The changes were updated since you opened this dialog; review them again.";

/** How many times a refresh lists the changes again when a file changes while it reads. */
const maxRelists = 3;

/**
 * The vault's git working tree: what changed since the last commit, a secret check of
 * exactly those bytes, and committing exactly that list (ADR-0020).
 */
export class Git {
  readonly state = new Store<GitState>({ busy: false, scanning: false });
  private generation = 0;

  constructor(private readonly deps: GitDeps) {}

  /** Starts a fresh look: saves the open note, clears the last messages and reads the status. */
  async open(): Promise<void> {
    this.state.update((s) => ({ ...s, committed: undefined, error: undefined, notice: undefined }));
    await this.deps.saveOpen();
    await this.refresh();
  }

  /** Lists the changes and checks their bytes for secrets. */
  async refresh(): Promise<void> {
    const gen = ++this.generation;
    this.state.update((s) => ({ ...s, scanning: true }));
    try {
      for (let i = 0; i < maxRelists; i++) {
        const status = await this.deps.status();
        if (gen !== this.generation) return;
        this.state.update((s) => ({ ...s, status, scan: undefined }));
        const scan = await scanChanges(status.changes, this.deps.readFile);
        if (gen !== this.generation) return;
        if (scan.kind === "scanned") {
          this.state.update((s) => ({ ...s, scan: scan.files }));
          return;
        }
        this.state.update((s) => ({ ...s, notice: changedNotice }));
      }
      this.state.update((s) => ({ ...s, error: "The files keep changing while they're checked. Try again in a moment." }));
    } catch (err) {
      if (gen === this.generation) this.state.update((s) => ({ ...s, error: `Couldn't read the changes: ${message(err)}` }));
    } finally {
      if (gen === this.generation) this.state.update((s) => ({ ...s, scanning: false }));
    }
  }

  /** The listed changes have been checked, and the check found something. */
  hasFindings(): boolean {
    return withFindings(this.state.value.scan).length > 0;
  }

  /**
   * Commits exactly the listed changes. With findings it commits only when anyway is true:
   * the user chose "Commit anyway". If the changes moved meanwhile, nothing is committed and
   * the dialog lists and checks them again.
   */
  async commit(msg: string, anyway = false): Promise<boolean> {
    const { status, scan, busy, scanning } = this.state.value;
    if (busy || scanning || !status || !scan) return false;
    if (this.hasFindings() && !anyway) return false;
    this.state.update((s) => ({ ...s, busy: true, error: undefined, notice: undefined, committed: undefined }));
    try {
      if (!(await this.deps.saveOpen())) {
        this.state.update((s) => ({ ...s, error: "Some edits to the open note aren't saved. Save or resolve them first." }));
        return false;
      }
      const hash = await this.deps.commit(msg, status.changes.map((c) => ({ path: c.path, blob: c.blob })));
      this.state.update((s) => ({ ...s, committed: hash.slice(0, 7) }));
      return true;
    } catch (err) {
      if (err instanceof ApiError && err.code === "changes-differ") {
        this.state.update((s) => ({ ...s, notice: changedNotice }));
      } else {
        this.state.update((s) => ({ ...s, error: `Couldn't commit: ${message(err)}` }));
      }
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
