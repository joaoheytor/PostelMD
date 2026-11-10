import type { Version } from "./api";
import { Store } from "./store";
import { message } from "./workspace";

export interface HistoryState {
  path: string;
  versions?: Version[] | undefined;
  /** The version being looked at, with its text once loaded. */
  selected?: { version: Version; content?: string | undefined } | undefined;
  error?: string | undefined;
}

export interface HistoryDeps {
  history(path: string): Promise<Version[]>;
  version(commit: string, path: string): Promise<string>;
}

/** One note's history: the commits that changed it, and the text of the one selected. */
export class History {
  readonly state: Store<HistoryState>;
  /** Bumped by every select(), so a slow answer for an earlier choice is dropped. */
  private pick = 0;

  constructor(
    readonly path: string,
    private readonly deps: HistoryDeps,
  ) {
    this.state = new Store<HistoryState>({ path });
  }

  async load(): Promise<void> {
    try {
      const versions = await this.deps.history(this.path);
      this.state.update((s) => ({ ...s, versions, error: undefined }));
    } catch (err) {
      this.state.update((s) => ({ ...s, error: `Couldn't read the history of ${this.path}: ${message(err)}` }));
    }
  }

  async select(version: Version): Promise<void> {
    const pick = ++this.pick;
    this.state.update((s) => ({ ...s, selected: { version }, error: undefined }));
    if (version.deleted) return; // nothing to show: the commit removed the note
    try {
      const content = await this.deps.version(version.commit, this.path);
      if (pick === this.pick) this.state.update((s) => ({ ...s, selected: { version, content } }));
    } catch (err) {
      if (pick === this.pick) this.state.update((s) => ({ ...s, error: `Couldn't read that version: ${message(err)}` }));
    }
  }
}
