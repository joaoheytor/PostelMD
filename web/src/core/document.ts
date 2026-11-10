import type { SaveResult } from "./api";
import { type Disposable, toDisposable } from "./disposable";
import { Store } from "./store";

export type DocStatus = "clean" | "dirty" | "saving" | "conflict" | "gone" | "error";

export interface DocState {
  status: DocStatus;
  /** Why the last save failed (status "error"). */
  message?: string;
  /** The server's copy after a 412 (status "conflict"). */
  conflict?: { content: string; etag: string };
}

export interface DocOptions {
  save: (path: string, content: string, baseETag: string) => Promise<SaveResult>;
  /** Reads the editor's current text. Called only when a save starts, never per keystroke. */
  read: () => string;
  debounceMs?: number;
}

const maxBackoffMs = 30_000;

function timeout(fn: () => void, ms: number): Disposable {
  const id = setTimeout(fn, ms);
  return toDisposable(() => clearTimeout(id));
}

/**
 * The autosave of one open note (ADR-0003 §7, ADR-0020):
 *
 * - each edit bumps the edit generation; a save sends a snapshot {content, generation, base
 *   ETag}, with If-Match, and only one save is in flight;
 * - a response clears the dirty state only when its generation is still the newest, so an
 *   older save can never clear a newer edit;
 * - saves are debounced after the last edit; flush() saves at once (blur, navigation);
 * - a 412 stops autosave until the user keeps theirs or mine; a 404 means the note was
 *   deleted, and autosave never recreates it; busy and failed saves retry with backoff.
 */
export class DocumentSession implements Disposable {
  readonly state = new Store<DocState>({ status: "clean" });

  private gen = 0;
  private savedGen = 0;
  private inflight: Promise<void> | undefined;
  private timer: Disposable | undefined;
  private failures = 0;
  private disposed = false;
  private readonly debounceMs: number;

  constructor(
    readonly path: string,
    private baseETag: string,
    private readonly opts: DocOptions,
  ) {
    this.debounceMs = opts.debounceMs ?? 1000;
  }

  /** True while some edit isn't saved. */
  get dirty(): boolean {
    return this.gen !== this.savedGen;
  }

  /** Records an edit and schedules a save. */
  edit(): void {
    this.gen++;
    const { status } = this.state.value;
    if (status === "conflict" || status === "gone") return; // waits for the user
    if (status !== "saving") this.state.set({ status: "dirty" });
    this.failures = 0;
    this.schedule(this.debounceMs);
  }

  /** Saves now, and again for edits made meanwhile, until clean or stopped. */
  async flush(): Promise<void> {
    this.cancelTimer();
    while (this.dirty && this.saveable()) {
      const before = this.savedGen;
      await this.run();
      if (this.savedGen === before) return; // the save didn't land: a conflict, gone or error
    }
  }

  /** Resolves a conflict with the editor's text: it is saved over the server's copy. */
  keepMine(): void {
    const c = this.state.value.conflict;
    if (!c) return;
    this.baseETag = c.etag;
    this.state.set({ status: "dirty" });
    void this.flush();
  }

  /** Resolves a conflict with the server's copy; the caller puts it in the editor. */
  takeTheirs(): string {
    const c = this.state.value.conflict;
    if (!c) throw new Error("no conflict to resolve");
    this.cancelTimer();
    this.baseETag = c.etag;
    this.gen++;
    this.savedGen = this.gen;
    this.state.set({ status: "clean" });
    return c.content;
  }

  dispose(): void {
    this.disposed = true;
    this.cancelTimer();
  }

  private saveable(): boolean {
    const { status } = this.state.value;
    return !this.disposed && status !== "conflict" && status !== "gone";
  }

  private schedule(ms: number): void {
    this.cancelTimer();
    this.timer = timeout(() => {
      this.timer = undefined;
      void this.run();
    }, ms);
  }

  private cancelTimer(): void {
    this.timer?.dispose();
    this.timer = undefined;
  }

  /** Sends one save, unless one is in flight already. */
  private run(): Promise<void> {
    if (!this.inflight) {
      this.inflight = this.saveOnce().finally(() => (this.inflight = undefined));
    }
    return this.inflight;
  }

  private async saveOnce(): Promise<void> {
    if (!this.dirty || !this.saveable()) return;
    const snap = { content: this.opts.read(), gen: this.gen, base: this.baseETag };
    this.state.set({ status: "saving" });
    const r = await this.opts.save(this.path, snap.content, snap.base);
    if (this.disposed) return;
    switch (r.kind) {
      case "saved":
        this.baseETag = r.etag;
        this.savedGen = snap.gen;
        this.failures = 0;
        if (this.dirty) {
          // Edits arrived during the save: save them after the debounce, not at once, so
          // typing through a save doesn't turn into a save per keystroke.
          this.state.set({ status: "dirty" });
          if (!this.timer) this.schedule(this.debounceMs);
        } else {
          this.state.set({ status: "clean" });
        }
        return;
      case "conflict":
        this.cancelTimer();
        this.state.set({ status: "conflict", conflict: { content: r.content, etag: r.etag } });
        return;
      case "gone":
        this.cancelTimer();
        this.state.set({ status: "gone" });
        return;
      case "error":
        this.state.set({ status: "error", message: r.message });
        if (r.retry) {
          const backoff = Math.min(1000 * 2 ** this.failures, maxBackoffMs);
          this.failures++;
          this.schedule(Math.max(r.retryAfterMs, backoff));
        }
        return;
    }
  }
}
