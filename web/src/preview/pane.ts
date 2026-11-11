import { type Disposable, DisposableStore, toDisposable } from "../core/disposable";
import { batchHTML, intact, keyAttr, newKey } from "./blocks";
import { type Diagram, diagramClass, Diagrams } from "./diagrams";
import type { Block } from "./render";
import { assetWorker, sanitized } from "./sanitize";
import type { Reply, Request } from "./worker";
import workerUrl from "./worker.ts?worker&url";

/** Blocks per sanitize call in the first render, between yields (ADR-0007). */
const batch = 200;

const copyLabel = "Copy code";

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
 * that only the blocks whose HTML changed are sanitized and replaced. Code blocks get a copy
 * button. Create it only while the preview is visible, and dispose it when hidden.
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
  /** Created on the first Mermaid block. */
  private diagrams: Diagrams | undefined;
  /** The copy buttons' "Copied" timers. */
  private readonly flashes = new DisposableStore();
  /** Numbers this preview's block wrappers, so a note can't forge one (blocks.ts). */
  private readonly key = newKey();

  /** onRender runs after each render, which can move the blocks (scroll sync follows). */
  constructor(
    private readonly el: HTMLElement,
    private readonly onRender: () => void = () => {},
    private readonly debounceMs = 300,
  ) {
    this.worker.onmessage = (e: MessageEvent<Reply>) => {
      const reply = e.data;
      this.applying = this.applying
        .then(() => this.apply(reply))
        .then(() => {
          if (!this.disposed) this.onRender();
        });
    };
    el.addEventListener("click", this.onClick);
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
    this.flashes.dispose();
    this.diagrams?.dispose();
    this.el.removeEventListener("click", this.onClick);
    this.worker.terminate();
  }

  private onClick = (e: MouseEvent) => {
    const button = (e.target as Element).closest("button.pm-copy");
    const code = button?.parentElement?.querySelector("code");
    if (button instanceof HTMLButtonElement && code) void this.copy(button, code);
  };

  /** Copies a code block. Without clipboard access (plain http off localhost), selects it. */
  private async copy(button: HTMLButtonElement, code: HTMLElement): Promise<void> {
    try {
      await navigator.clipboard.writeText(code.textContent ?? "");
      this.flash(button, "Copied");
    } catch {
      getSelection()?.selectAllChildren(code);
      this.flash(button, "Selected: press Ctrl+C");
    }
  }

  private flash(button: HTMLButtonElement, text: string): void {
    button.textContent = text;
    const id = setTimeout(() => (button.textContent = copyLabel), 2000);
    this.flashes.add(toDisposable(() => clearTimeout(id)));
  }

  /**
   * Replaces a mermaid code block with its diagram, once rendered (ADR-0021). On an error the
   * code stays, with the error under it as text.
   */
  private async diagram(pre: HTMLElement, source: string): Promise<void> {
    const diagrams = (this.diagrams ??= new Diagrams((url) => [...this.el.querySelectorAll<HTMLImageElement>(`img.${diagramClass}`)].some((img) => img.src === url)));
    const d = await diagrams.render(source);
    if (!this.disposed && pre.isConnected) this.place(pre, d);
    diagrams.sweep(); // the diagram this one replaced on screen, if any, can go now
  }

  private place(pre: HTMLElement, d: Diagram): void {
    if (!d.ok) {
      const p = document.createElement("p");
      p.className = "pm-diagram-error";
      p.textContent = `This diagram has an error: ${d.error}`;
      pre.after(p);
      return;
    }
    const img = document.createElement("img");
    img.className = diagramClass;
    img.alt = d.label;
    if (d.width > 0 && d.height > 0) {
      img.width = d.width;
      img.height = d.height;
    }
    img.src = d.url;
    pre.replaceWith(img);
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

  /**
   * Sanitizes blocks into elements, remembering each one's source HTML. Copy buttons are
   * added after sanitizing, with DOM calls: the sanitizer allows no buttons in a note.
   */
  private render(blocks: Block[]): DocumentFragment {
    const frag = this.wrapped(blocks);
    [...frag.children].forEach((el, i) => this.source.set(el, blocks[i]?.html ?? ""));
    for (const code of frag.querySelectorAll("pre > code.language-mermaid")) {
      if (code.parentElement) void this.diagram(code.parentElement, code.textContent ?? "");
    }
    for (const pre of frag.querySelectorAll("pre:not(.pm-frontmatter)")) {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "pm-copy";
      button.textContent = copyLabel;
      pre.append(button);
    }
    return frag;
  }

  /**
   * Sanitizes blocks into one wrapper each. One call for the whole batch (ADR-0007), unless
   * a block's HTML broke the wrappers (blocks.ts): then each block alone, in a wrapper made
   * with DOM calls, where no tag in it can reach outside its own.
   */
  private wrapped(blocks: Block[]): DocumentFragment {
    const frag = sanitized(batchHTML(blocks, this.key));
    if (intact(frag.childNodes, blocks, this.key)) {
      for (const el of frag.children) el.removeAttribute(keyAttr);
      return frag;
    }
    const out = document.createDocumentFragment();
    for (const b of blocks) {
      const div = document.createElement("div");
      div.className = "pm-block";
      div.dataset["line"] = String(b.line);
      div.append(sanitized(b.html));
      out.append(div);
    }
    return out;
  }
}
