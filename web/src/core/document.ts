import type { SaveResult } from "./api";
import { type Disposable, toDisposable } from "./disposable";
import type { NoteDrafts } from "./drafts";
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
  /** Where unsaved edits are kept until a save lands (ADR-0003 §7). */
  draft?: NoteDrafts;
  /** The draft is written this long after the last edit, well before the save. */
  draftMs?: number;
}

const maxBackoffMs = 30_000;
const maxDraftWaitMs = 2000;

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
 *   deleted, and autosave never recreates it; busy and failed saves retry with backoff;
 * - each edit is also written as a local draft shortly after, and a save clears only the
 *   draft of the generation it saved; a failed save, a conflict or a deleted note keep it.
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
  private draftTimer: Disposable | undefined;
  /** When the oldest edit not in a draft was made. */
  private undraftedSince: number | undefined;
  /** The generation the last draft write holds. */
  private draftedGen = 0;

  constructor(
    readonly path: string,
    private baseETag: string,
    private readonly opts: DocOptions,
  ) {
    this.debounceMs = opts.debounceMs ?? 1000;
  }

  /** The ETag of the last version saved or read: the If-Match base. */
  get etag(): string {
    return this.baseETag;
  }

  /** True while some edit isn't saved. */
  get dirty(): boolean {
    return this.gen !== this.savedGen;
  }

  /** Records an edit and schedules its draft and its save. */
  edit(): void {
    this.gen++;
    this.scheduleDraft();
    const { status } = this.state.value;
    if (status === "conflict" || status === "gone") return; // waits for the user
    if (status !== "saving") this.state.set({ status: "dirty" });
    this.failures = 0;
    this.schedule(this.debounceMs);
  }

  /**
   * Writes the draft now, if some edit isn't in it yet (the tab is being hidden). Reads the
   * text only here, never per keystroke.
   */
  async writeDraft(): Promise<void> {
    this.draftTimer?.dispose();
    this.draftTimer = undefined;
    this.undraftedSince = undefined;
    if (!this.opts.draft || !this.dirty || this.draftedGen === this.gen) return;
    this.draftedGen = this.gen;
    await this.opts.draft.write(this.opts.read(), this.baseETag, this.gen);
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
    this.opts.draft?.drop(); // the user chose the other version over this tab's edits
    this.state.set({ status: "clean" });
    return c.content;
  }

  /** Stops saving. An edit not in a draft yet is written to one: closing never loses it. */
  dispose(): void {
    void this.writeDraft();
    this.disposed = true;
    this.cancelTimer();
  }

  /**
   * Debounces the draft, but never past maxDraftWaitMs from the first edit not in one: nonstop
   * typing still reaches a draft, and the text isn't copied on every keystroke.
   */
  private scheduleDraft(): void {
    if (!this.opts.draft) return;
    const now = Date.now();
    this.undraftedSince ??= now;
    const wait = Math.min(this.opts.draftMs ?? 300, this.undraftedSince + maxDraftWaitMs - now);
    this.draftTimer?.dispose();
    this.draftTimer = timeout(() => void this.writeDraft(), Math.max(wait, 0));
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
    // The server holds snap.gen now, even if the note was closed meanwhile: its draft (written
    // on close, under this tab's esid) is cleared up to that generation, never past it.
    if (r.kind === "saved") this.opts.draft?.saved(snap.gen);
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
