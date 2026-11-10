import { type Disposable, DisposableStore, toDisposable } from "../core/disposable";
import type { Editor } from "../editor/editor";

/**
 * The block (by index) that holds line, and how far into it the line is (0 to 1). lines are
 * the blocks' first lines, ascending. The last block has no end to measure, so it gives 0.
 */
export function blockAt(lines: readonly number[], line: number): { index: number; fraction: number } | undefined {
  if (lines.length === 0) return undefined;
  let lo = 0;
  let hi = lines.length - 1;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if ((lines[mid] ?? 0) <= line) lo = mid;
    else hi = mid - 1;
  }
  const start = lines[lo] ?? 0;
  const end = lines[lo + 1];
  const fraction = end === undefined || line < start ? 0 : (line - start) / (end - start);
  return { index: lo, fraction };
}

/** The line that a point fraction (0 to 1) of the way into block index stands for. */
export function lineAt(lines: readonly number[], index: number, fraction: number): number {
  const start = lines[index] ?? 0;
  const end = lines[index + 1] ?? start;
  return Math.round(start + fraction * (end - start));
}

/**
 * Keeps the editor and the preview showing the same part of the note (ADR-0007: each
 * preview block has its first source line in data-line). The pane the user is working in
 * leads and the other follows, so the two never chase each other's scroll events.
 */
export class ScrollSync implements Disposable {
  private lead: "editor" | "preview" = "editor";
  private frame = 0;
  private readonly subs = new DisposableStore();

  constructor(
    private readonly editor: Editor,
    editorHost: HTMLElement,
    private readonly preview: HTMLElement,
  ) {
    for (const type of ["wheel", "pointerdown", "keydown", "touchstart", "focusin"] as const) {
      this.subs.add(on(editorHost, type, () => (this.lead = "editor")));
      this.subs.add(on(preview, type, () => (this.lead = "preview")));
    }
    this.subs.add(editor.onScroll(() => this.lead === "editor" && this.schedule(() => this.followEditor())));
    this.subs.add(on(preview, "scroll", () => this.lead === "preview" && this.schedule(() => this.followPreview())));
    this.subs.add(toDisposable(() => cancelAnimationFrame(this.frame)));
  }

  /** The preview re-rendered, which can move its blocks: it follows the editor again. */
  rendered(): void {
    if (this.lead === "editor") this.followEditor();
  }

  dispose(): void {
    this.subs.dispose();
  }

  /** At most one sync per frame. */
  private schedule(fn: () => void): void {
    cancelAnimationFrame(this.frame);
    this.frame = requestAnimationFrame(fn);
  }

  private blocks(): { els: HTMLElement[]; lines: number[] } {
    const els = [...this.preview.children].filter((el): el is HTMLElement => el instanceof HTMLElement && el.dataset["line"] !== undefined);
    return { els, lines: els.map((el) => Number(el.dataset["line"])) };
  }

  private followEditor(): void {
    const line = this.editor.topLine();
    if (line === 0) {
      this.preview.scrollTop = 0; // the very top, with whatever is above the first block
      return;
    }
    const { els, lines } = this.blocks();
    const at = blockAt(lines, line);
    const el = at && els[at.index];
    if (at && el) this.preview.scrollTop = el.offsetTop + at.fraction * el.offsetHeight;
  }

  private followPreview(): void {
    const { els, lines } = this.blocks();
    const top = this.preview.scrollTop;
    // The last block starting at or above the top of the preview.
    let lo = 0;
    let hi = els.length - 1;
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1;
      if ((els[mid]?.offsetTop ?? 0) <= top) lo = mid;
      else hi = mid - 1;
    }
    const el = els[lo];
    if (!el) return;
    const fraction = el.offsetHeight > 0 ? Math.min(Math.max((top - el.offsetTop) / el.offsetHeight, 0), 1) : 0;
    this.editor.scrollToLine(lineAt(lines, lo, fraction));
  }
}

function on(el: HTMLElement, type: string, fn: () => void): Disposable {
  el.addEventListener(type, fn, { passive: true });
  return toDisposable(() => el.removeEventListener(type, fn));
}
