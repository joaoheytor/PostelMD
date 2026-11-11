import type { SaveResult } from "./api";
import { type Disposable, timeout } from "./disposable";
import type { NoteDrafts } from "./drafts";
import { Store } from "./store";

/** "stalled": a save timed out, so it may or may not have landed; it is being retried. */
export type DocStatus = "clean" | "dirty" | "saving" | "stalled" | "conflict" | "gone" | "error";

export interface DocState {
  status: DocStatus;
  /** Why the last save failed (status "error"). */
  message?: string;
  /** What happens next ("Retrying", "Log in again"), if anything. */
  next?: string;
  /** The server's copy after a 412 (status "conflict"); binary: it isn't text, content is "". */
  conflict?: { content: string; etag: string; binary?: true };
}

export interface DocOptions {
  save: (path: string, content: string, baseETag: string) => Promise<SaveResult>;
  /**
   * Reads the editor's text, only when a save or a draft starts. Undefined when the editor is
   * gone: then nothing is saved or drafted, since no other text holds the user's edits.
   */
  read: () => string | undefined;
  debounceMs?: number;
  /** Where unsaved edits are kept until a save lands (ADR-0003 §7). */
  draft?: NoteDrafts;
  /** The draft is written this long after the last edit, well before the save. */
  draftMs?: number;
}

const maxBackoffMs = 30_000;
const maxDraftWaitMs = 2000;
/** Timed-out saves remembered to recognise one that landed; one forgotten costs a conflict view. */
const maxStalls = 8;

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
  /** The last draft write failed (quota, private mode): no local copy holds the newest edits. */
  readonly draftFailed = new Store<boolean>(false);

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
  /** The saves that timed out since one last landed: a 412 with one's bytes means it landed. */
  private stalls: { content: string; gen: number }[] = [];

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
    if (status === "stalled") {
      this.schedule(this.debounceMs); // the stall stays shown, and its failure count stays
      return;
    }
    if (status !== "saving") this.state.set({ status: "dirty" });
    this.failures = 0;
    this.schedule(this.debounceMs);
  }

  /**
   * Writes the draft now, if some edit isn't in it (the tab is being hidden). True when every
   * unsaved edit is in a draft; a write that fails counts for nothing, so the next one retries.
   */
  async writeDraft(): Promise<boolean> {
    this.draftTimer?.dispose();
    this.draftTimer = undefined;
    this.undraftedSince = undefined;
    if (!this.dirty || this.draftedGen === this.gen) return true;
    const gen = this.gen;
    const text = this.opts.read();
    if (!this.opts.draft || text === undefined) return false;
    try {
      await this.opts.draft.write(text, this.baseETag, gen);
    } catch {
      this.draftFailed.set(true);
      return false;
    }
    this.draftedGen = Math.max(this.draftedGen, gen);
    this.draftFailed.set(false);
    return !this.dirty || this.draftedGen === this.gen;
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

  /**
   * The editor's text is now based on etag (a restored draft's base), not on the last save:
   * the next save sends If-Match etag, so if the note changed since, the server answers 412
   * and the user chooses in the conflict view. Call it with no save in flight (after flush).
   */
  rebase(etag: string): void {
    this.baseETag = etag;
    this.stalls = []; // a 412 now is about the restored text: the user decides (ADR-0023)
  }

  /** Drops this session's draft: the user kept its text elsewhere (saved as a new note). */
  dropDraft(): void {
    this.opts.draft?.drop();
  }

  /**
   * Stops saving. An edit not in a draft yet is written to one: closing never loses it, and the
   * draft is offered when the note is opened again.
   */
  dispose(): void {
    void this.writeDraft();
    this.opts.draft?.close();
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
    const content = this.opts.read();
    if (content === undefined) return this.schedule(this.debounceMs); // no editor yet: try again, until disposed
    const snap = { content, gen: this.gen, base: this.baseETag };
    // A retry leaves a failure or a stall shown until it lands: it isn't announced again.
    const { status } = this.state.value;
    if (status !== "error" && status !== "stalled") this.state.set({ status: "saving" });
    const r = await this.opts.save(this.path, snap.content, snap.base);
    // The server holds snap.gen now, even if the note was closed meanwhile: its draft (written
    // on close, under this session's key) is cleared up to that generation, never past it.
    if (r.kind === "saved") this.opts.draft?.saved(snap.gen);
    const landed = r.kind === "conflict" && !r.binary ? this.landedStall(r.content) : undefined; // binary "" is no text
    if (landed !== undefined) this.opts.draft?.saved(landed);
    if (this.disposed) return;
    if (landed !== undefined && r.kind === "conflict") return this.stallLanded(landed, r.etag);
    switch (r.kind) {
      case "saved":
        this.stalls = [];
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
        this.stalls = [];
        this.cancelTimer();
        this.state.set({ status: "conflict", conflict: { content: r.content, etag: r.etag, ...(r.binary && { binary: true }) } });
        return;
      case "gone":
        this.cancelTimer();
        this.state.set({ status: "gone" });
        return;
      case "unknown":
        // Timed out, maybe landed: the retry sends the newest text over the same base. Identical
        // bytes get a 204 (ADR-0027); a 412 with these bytes means they landed (stallLanded).
        this.stalls = [...this.stalls.slice(-(maxStalls - 1)), { content: snap.content, gen: snap.gen }];
        this.state.set({ status: "stalled" });
        this.retry(0);
        return;
      case "error":
        this.state.set({ status: "error", message: r.message, ...(r.next && { next: r.next }) });
        if (r.retry) this.retry(r.retryAfterMs);
        return;
    }
  }

  /** The newest generation among the stalled saves whose bytes are body, if any. */
  private landedStall(body: string): number | undefined {
    const gens = this.stalls.filter((s) => s.content === body).map((s) => s.gen);
    return gens.length > 0 ? Math.max(...gens) : undefined;
  }

  /** A 412 with a stalled save's bytes: it landed as etag. No conflict: save the newest now. */
  private stallLanded(gen: number, etag: string): void {
    this.stalls = [];
    this.baseETag = etag;
    this.savedGen = Math.max(this.savedGen, gen);
    this.failures = 0;
    if (!this.dirty) return this.state.set({ status: "clean" });
    this.state.set({ status: "dirty" });
    this.schedule(0);
  }

  private retry(afterMs: number): void {
    const backoff = Math.min(1000 * 2 ** this.failures, maxBackoffMs);
    this.failures++;
    this.schedule(Math.max(afterMs, backoff));
  }
}
