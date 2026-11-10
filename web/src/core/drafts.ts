/**
 * Local drafts of unsaved edits (ADR-0003 §7, ADR-0020): until the server answers a save with
 * 200, the draft is the only other copy of the edit. Each tab writes its own drafts, under its
 * esid; a save clears only its own tab's draft, and only up to the generation it saved. Every
 * other draft, of closed or crashed tabs and of earlier loads of this tab, is offered to the
 * user, who alone decides to restore or discard it: PostelMD never deletes a draft unasked.
 */

export interface Draft {
  path: string;
  esid: string;
  text: string;
  /** The ETag the edit was based on. */
  base: string;
  /** The edit generation the text holds. */
  gen: number;
  /** When it was written (ms since the epoch). */
  savedAt: number;
}

/** Where drafts are kept: IndexedDB in the browser (drafts-idb.ts), memory in tests. */
export interface DraftBackend {
  put(d: Draft): Promise<void>;
  /** Deletes path's draft of esid if its generation is at most gen. */
  deleteUpTo(path: string, esid: string, gen: number): Promise<void>;
  delete(path: string, esid: string): Promise<void>;
  all(): Promise<Draft[]>;
  /** Moves every draft of esid `from` to esid `to`. */
  rekey(from: string, to: string): Promise<void>;
}

/** One note's drafts in this tab, as DocumentSession uses them. */
export interface NoteDrafts {
  write(text: string, base: string, gen: number): Promise<void>;
  saved(gen: number): void;
  drop(): void;
}

export class Drafts {
  /** This tab's writes and clears, applied in the order they were made. */
  private queue: Promise<void> = Promise.resolve();
  /** The session that last wrote each note's draft in this tab. */
  private readonly writers = new Map<string, object>();

  private constructor(
    private readonly backend: DraftBackend,
    readonly esid: string,
  ) {}

  /**
   * Opens the drafts for a tab whose esid was just claimed. Drafts already under that esid are
   * an earlier load's (a reload keeps the esid but restarts the edit generations), so they are
   * moved to a fresh esid and offered like a closed tab's, never written over.
   */
  static async open(backend: DraftBackend, esid: string, mint: () => string): Promise<Drafts> {
    if ((await backend.all()).some((d) => d.esid === esid)) await backend.rekey(esid, mint());
    return new Drafts(backend, esid);
  }

  /**
   * One session of a note. Each session counts its generations from 0, and a closed session's
   * save can still land, so a save clears the note's draft only while that session wrote it
   * last: never the draft of a session opened on the note since.
   */
  forNote(path: string): NoteDrafts {
    const session = {};
    return {
      write: (text, base, gen) =>
        this.enqueue(() => {
          this.writers.set(path, session);
          return this.backend.put({ path, esid: this.esid, text, base, gen, savedAt: Date.now() });
        }),
      saved: (gen) =>
        void this.enqueue(() => (this.writers.get(path) === session ? this.backend.deleteUpTo(path, this.esid, gen) : Promise.resolve())),
      drop: () => void this.enqueue(() => this.backend.delete(path, this.esid)),
    };
  }

  /** The drafts to offer, newest first: of other tabs and earlier loads, for path or all. */
  async offered(path?: string): Promise<Draft[]> {
    const all = await this.backend.all();
    return all.filter((d) => d.esid !== this.esid && (path === undefined || d.path === path)).sort((a, b) => b.savedAt - a.savedAt);
  }

  /** Deletes a draft the user chose to discard (or restored). */
  discard(d: Draft): Promise<void> {
    return this.enqueue(() => this.backend.delete(d.path, d.esid));
  }

  /** Resolves once every write and clear made so far is applied. */
  idle(): Promise<void> {
    return this.queue;
  }

  private enqueue(op: () => Promise<void>): Promise<void> {
    const next = this.queue.then(op);
    this.queue = next.catch(() => {}); // one failed write doesn't stop the next
    return next;
  }
}

/** Drafts kept in memory: for tests, and when the browser offers no IndexedDB. */
export class MemoryDraftBackend implements DraftBackend {
  private readonly rows = new Map<string, Draft>();

  put(d: Draft): Promise<void> {
    this.rows.delete(id(d.path, d.esid)); // a rewrite goes last, like a new row
    this.rows.set(id(d.path, d.esid), { ...d });
    return Promise.resolve();
  }

  deleteUpTo(path: string, esid: string, gen: number): Promise<void> {
    const d = this.rows.get(id(path, esid));
    if (d && d.gen <= gen) this.rows.delete(id(path, esid));
    return Promise.resolve();
  }

  delete(path: string, esid: string): Promise<void> {
    this.rows.delete(id(path, esid));
    return Promise.resolve();
  }

  all(): Promise<Draft[]> {
    return Promise.resolve([...this.rows.values()].map((d) => ({ ...d })));
  }

  async rekey(from: string, to: string): Promise<void> {
    for (const d of await this.all()) {
      if (d.esid !== from) continue;
      this.rows.delete(id(d.path, from));
      await this.put({ ...d, esid: to });
    }
  }
}

function id(path: string, esid: string): string {
  return `${esid}\u0000${path}`;
}
