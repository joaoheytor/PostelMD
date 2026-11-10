import { defaultKeymap, history, historyKeymap } from "@codemirror/commands";
import { markdown } from "@codemirror/lang-markdown";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { Annotation, EditorState } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { tags as t } from "@lezer/highlight";
import { type Disposable, toDisposable } from "../core/disposable";

/** Marks a change the app made (loading the server's copy), so it isn't an edit to save. */
const external = Annotation.define<boolean>();

export interface Editor extends Disposable {
  /** The current text. Costs a full copy: call it when saving, not per keystroke. */
  text(): string;
  /** Replaces the text without reporting an edit. */
  replace(text: string): void;
  focus(): void;
  /** The line (0-based) at the top of the visible area. */
  topLine(): number;
  /** Scrolls so line (0-based) is at the top. */
  scrollToLine(line: number): void;
  /** Calls fn when the text scrolls, until disposed. */
  onScroll(fn: () => void): Disposable;
}

export interface EditorOptions {
  doc: string;
  /** The text area's accessible name. */
  label: string;
  /** The user edited the text: it needs saving. */
  onEdit(): void;
  /** The text changed, by the user or replace(): the preview follows both. */
  onChange(): void;
}

// Colours come from the page's custom properties, which inherit into the shadow root, so the
// editor follows the light and dark themes of styles.css.
const theme = EditorView.theme({
  "&": { height: "100%", fontSize: "15px", color: "var(--fg)", backgroundColor: "var(--bg)" },
  ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--fg)" },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection": { backgroundColor: "var(--selection)" },
  ".cm-activeLine": { backgroundColor: "transparent" },
  ".cm-scroller": { fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace", lineHeight: "1.6" },
  ".cm-content": { padding: "16px 0", maxWidth: "80ch" },
  ".cm-line": { padding: "0 24px" },
  "&.cm-focused": { outline: "none" },
});

const highlight = HighlightStyle.define([
  { tag: t.heading, fontWeight: "700", color: "var(--accent)" },
  { tag: t.strong, fontWeight: "700" },
  { tag: t.emphasis, fontStyle: "italic" },
  { tag: t.strikethrough, textDecoration: "line-through" },
  { tag: [t.link, t.url], color: "var(--link)" },
  { tag: t.monospace, color: "var(--code)" },
  { tag: t.quote, color: "var(--muted)", fontStyle: "italic" },
  { tag: [t.processingInstruction, t.meta, t.contentSeparator], color: "var(--muted)" },
  { tag: [t.keyword, t.atom, t.bool], color: "var(--accent)" },
  { tag: [t.string, t.number], color: "var(--code)" },
  { tag: t.comment, color: "var(--muted)" },
]);

/**
 * Mounts CodeMirror 6 in root, a shadow root: there its styles use constructable
 * stylesheets, which the CSP's style-src 'self' allows (ADR-0019).
 */
export function mountEditor(root: ShadowRoot, { doc, label, onEdit, onChange }: EditorOptions): Editor {
  const view = new EditorView({
    root,
    parent: root,
    state: EditorState.create({
      doc,
      extensions: [
        history(),
        keymap.of([...defaultKeymap, ...historyKeymap]),
        markdown(),
        syntaxHighlighting(highlight),
        EditorView.lineWrapping,
        EditorView.contentAttributes.of({ "aria-label": label }),
        theme,
        EditorView.updateListener.of((u) => {
          if (!u.docChanged) return;
          onChange();
          if (!u.transactions.some((t) => t.annotation(external))) onEdit();
        }),
      ],
    }),
  });
  return {
    text: () => view.state.doc.toString(),
    replace(text) {
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text }, annotations: external.of(true) });
    },
    focus: () => view.focus(),
    topLine() {
      const block = view.lineBlockAtHeight(view.scrollDOM.scrollTop);
      return view.state.doc.lineAt(block.from).number - 1;
    },
    scrollToLine(line) {
      const doc = view.state.doc;
      const pos = doc.line(Math.min(Math.max(line + 1, 1), doc.lines)).from;
      view.dispatch({ effects: EditorView.scrollIntoView(pos, { y: "start" }) });
    },
    onScroll(fn) {
      view.scrollDOM.addEventListener("scroll", fn, { passive: true });
      return toDisposable(() => view.scrollDOM.removeEventListener("scroll", fn));
    },
    dispose: () => view.destroy(),
  };
}
