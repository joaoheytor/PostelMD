import { type Disposable, toDisposable } from "../core/disposable";
import type { Block } from "./render";
import { assetWorker, sanitized } from "./sanitize";
import type { Reply, Request } from "./worker";
import workerUrl from "./worker.ts?worker&url";

/** Blocks per sanitize call in the first render, between yields (ADR-0007). */
const batch = 200;

/** Lets the event loop run (input, paint) without setTimeout's clamping. */
function yieldNow(): Promise<void> {
  return new Promise((resolve) => {
    const c = new MessageChannel();
    c.port1.onmessage = () => {
      c.port1.close();
      resolve();
    };
    c.port2.postMessage(0);
  });
}

/**
 * The live preview of one note, rendered into el (ADR-0007): markdown-it in a Worker, then
 * DOMPurify inside the Trusted Types policy. The first render is batched and yields; after
 * that only the blocks whose HTML changed are sanitized and replaced. Create it only while
 * the preview is visible, and dispose it when hidden.
 */
export class Preview implements Disposable {
  private readonly worker = assetWorker(workerUrl);
  /** The source HTML of each block element, to skip unchanged blocks. */
  private readonly source = new WeakMap<Element, string>();
  private seq = 0;
  private timer: Disposable | undefined;
  /** Replies are applied one at a time, in order. */
  private applying: Promise<void> = Promise.resolve();
  private disposed = false;

  constructor(
    private readonly el: HTMLElement,
    private readonly debounceMs = 300,
  ) {
    this.worker.onmessage = (e: MessageEvent<Reply>) => {
      const reply = e.data;
      this.applying = this.applying.then(() => this.apply(reply));
    };
  }

  /** Renders text now. */
  show(text: string): void {
    this.timer?.dispose();
    const req: Request = { id: ++this.seq, text };
    this.worker.postMessage(req);
  }

  /**
   * Renders the text after the debounce; a newer update replaces a pending one. The text is
   * read only then, so typing doesn't copy the note on every keystroke.
   */
  update(read: () => string): void {
    this.timer?.dispose();
    const id = setTimeout(() => this.show(read()), this.debounceMs);
    this.timer = toDisposable(() => clearTimeout(id));
  }

  dispose(): void {
    this.disposed = true;
    this.timer?.dispose();
    this.worker.terminate();
  }

  /** A newer text was sent since this reply's: skip it, or stop working on it. */
  private stale(r: Reply): boolean {
    return this.disposed || r.id !== this.seq;
  }

  private async apply(r: Reply): Promise<void> {
    if (this.stale(r)) return;
    if (this.el.childElementCount === 0) {
      for (let i = 0; i < r.blocks.length; i += batch) {
        this.el.append(this.render(r.blocks.slice(i, i + batch)));
        await yieldNow();
        if (this.stale(r)) return; // the next reply patches from here
      }
      return;
    }
    const kids = this.el.children;
    r.blocks.forEach((b, i) => {
      const el = kids[i];
      if (el && this.source.get(el) === b.html) {
        if (el.getAttribute("data-line") !== String(b.line)) el.setAttribute("data-line", String(b.line));
        return;
      }
      const next = this.render([b]);
      if (el) el.replaceWith(next);
      else this.el.append(next);
    });
    while (kids.length > r.blocks.length) this.el.lastElementChild?.remove();
  }

  /** Sanitizes blocks into elements, remembering each one's source HTML. */
  private render(blocks: Block[]): DocumentFragment {
    const frag = sanitized(blocks.map((b) => `<div class="pm-block" data-line="${b.line}">${b.html}</div>`).join(""));
    [...frag.children].forEach((el, i) => this.source.set(el, blocks[i]?.html ?? ""));
    return frag;
  }
}
