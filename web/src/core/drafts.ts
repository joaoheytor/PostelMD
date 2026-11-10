/**
 * Local drafts of unsaved edits (ADR-0003 §7, ADR-0020): until the server answers a save with
 * 200, the draft is the only other copy of the edit. Each note session of a tab writes its own
 * draft, under its esid and session number; a save clears only its own session's draft, and
 * only up to the generation it saved. Every other draft, of closed or crashed tabs, of notes
 * closed in this tab and of earlier loads of it, is offered to the user, who alone decides to
 * restore or discard it: PostelMD never deletes a draft unasked.
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
  /** The note was closed: from now on its draft is offered like a closed tab's. */
  close(): void;
}

export class Drafts {
  /** This tab's writes and clears, applied in the order they were made. */
  private queue: Promise<void> = Promise.resolve();
  /** How many note sessions this tab has opened: each writes under its own key. */
  private sessions = 0;
  /** The keys of this tab's sessions still open: only their drafts aren't offered. */
  private readonly live = new Set<string>();

  private constructor(
    private readonly backend: DraftBackend,
    readonly esid: string,
  ) {}

  /**
   * Opens the drafts for a tab whose esid was just claimed. Drafts already under that esid are
   * an earlier load's (a reload keeps the esid but restarts the session count), so they are
   * moved to fresh esids and offered like a closed tab's, never written over.
   */
  static async open(backend: DraftBackend, esid: string, mint: () => string): Promise<Drafts> {
    const earlier = new Set((await backend.all()).map((d) => d.esid).filter((k) => ownedBy(k, esid)));
    for (const k of earlier) await backend.rekey(k, mint());
    return new Drafts(backend, esid);
  }

  /**
   * One session of a note, with a draft key of its own (`<esid>.<n>`). Each session counts its
   * generations from 0 and a closed session's save can still land, so sessions never share a
   * draft: a session opened on the note since can neither write over a closed one's draft nor
   * have its own cleared by that late save.
   */
  forNote(path: string): NoteDrafts {
    const key = `${this.esid}.${++this.sessions}`;
    this.live.add(key);
    return {
      write: (text, base, gen) => this.enqueue(() => this.backend.put({ path, esid: key, text, base, gen, savedAt: Date.now() })),
      saved: (gen) => void this.enqueue(() => this.backend.deleteUpTo(path, key, gen)),
      drop: () => void this.enqueue(() => this.backend.delete(path, key)),
      close: () => void this.live.delete(key),
    };
  }

  /**
   * The drafts to offer, newest first, for path or all: every draft but those of this tab's
   * open notes. Waits for this tab's pending writes, so a note just closed has its draft in.
   */
  async offered(path?: string): Promise<Draft[]> {
    await this.idle();
    const all = await this.backend.all();
    return all.filter((d) => !this.live.has(d.esid) && (path === undefined || d.path === path)).sort((a, b) => b.savedAt - a.savedAt);
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

/** True for the tab's esid itself (drafts written before sessions had keys) and its sessions'. */
function ownedBy(key: string, esid: string): boolean {
  return key === esid || key.startsWith(`${esid}.`);
}

function id(path: string, esid: string): string {
  return `${esid}\u0000${path}`;
}
