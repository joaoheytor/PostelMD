import { defaultKeymap, history, historyKeymap, insertNewlineKeepIndent } from "@codemirror/commands";
import { html } from "@codemirror/lang-html";
import { markdown } from "@codemirror/lang-markdown";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { Annotation, Compartment, EditorState, type Extension } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { tags as t } from "@lezer/highlight";
import { type Disposable, toDisposable } from "../core/disposable";
import type { Mode } from "../core/filetype";
import { loadLanguage } from "./languages";

/** Marks a change the app made (loading the server's copy), so it isn't an edit to save. */
const external = Annotation.define<boolean>();

export interface Editor extends Disposable {
  /** The current text. Costs a full copy: call it when saving, not per keystroke. */
  text(): string;
  /** Replaces the text without reporting an edit. */
  replace(text: string): void;
  /** Focuses the text; at its end puts the cursor after the last character first. */
  focus(at?: "end"): void;
  /** The line (0-based) at the top of the visible area. */
  topLine(): number;
  /** Scrolls so line (0-based) is at the top. */
  scrollToLine(line: number): void;
  /** Calls fn when the text scrolls, until disposed. */
  onScroll(fn: () => void): Disposable;
}

export interface EditorOptions {
  doc: string;
  /** How the file is edited: a Markdown note, or another file with its syntax highlighted. */
  mode: Mode;
  /** The text area's accessible name. */
  label: string;
  /** The user edited the text: it needs saving. */
  onEdit(): void;
  /** The text changed, by the user or replace(): the preview follows both. */
  onChange(): void;
  /** The user pasted (or dropped) text in. */
  onPaste?(): void;
}

// Colours come from the page's custom properties, which inherit into the shadow root, so the
// editor follows the light and dark themes of styles.css.
const theme = EditorView.theme({
  "&": { height: "100%", fontSize: "15px", color: "var(--fg)", backgroundColor: "var(--bg)" },
  ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--fg)" },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection": { backgroundColor: "var(--selection)" },
  ".cm-activeLine": { backgroundColor: "transparent" },
  ".cm-scroller": { fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace", lineHeight: "1.6" },
  // The browser draws the caret (no drawSelection): CodeMirror's base theme makes it black
  // unless the editor theme is dark, which this one never says it is.
  ".cm-content": { padding: "16px 0", maxWidth: "80ch", caretColor: "var(--fg)" },
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
  { tag: [t.propertyName, t.typeName], color: "var(--link)" }, // YAML and JSON keys, code types
]);

/** A note's Markdown support; raw HTML is highlighted, but no tag is completed or closed for the writer. */
const markdownSupport = () =>
  markdown({ htmlTagLanguage: html({ autoCloseTags: false, matchClosingTags: false }), completeHTMLTags: false });

/**
 * The file's language: Markdown at once; another mode once its chunk loads (the text shows
 * meanwhile, unhighlighted). Nothing happens after the editor is gone.
 */
function language(view: () => EditorView | undefined, mode: Mode): Extension {
  if (mode === "markdown") return markdownSupport();
  const slot = new Compartment();
  void loadLanguage(mode).then((ext) => view()?.dispatch({ effects: slot.reconfigure(ext) }));
  return slot.of([]);
}

/**
 * Mounts CodeMirror 6 in root, a shadow root: there its styles use constructable
 * stylesheets, which the CSP's style-src 'self' allows (ADR-0019).
 */
export function mountEditor(root: ShadowRoot, { doc, mode, label, onEdit, onChange, onPaste }: EditorOptions): Editor {
  let live = true;
  const view: EditorView = new EditorView({
    root,
    parent: root,
    state: EditorState.create({
      doc,
      extensions: [
        history(),
        // Enter keeps the line's own indent: HTML's indent rules would push a <details> body
        // four spaces in, which Markdown reads as a code block. Lists still continue (markdown()).
        keymap.of([{ key: "Enter", run: insertNewlineKeepIndent }, ...defaultKeymap, ...historyKeymap]),
        language(() => (live ? view : undefined), mode),
        syntaxHighlighting(highlight),
        EditorView.lineWrapping,
        EditorView.contentAttributes.of({ "aria-label": label }),
        theme,
        EditorView.updateListener.of((u) => {
          if (!u.docChanged) return;
          onChange();
          if (!u.transactions.some((t) => t.annotation(external))) onEdit();
          if (u.transactions.some((t) => t.isUserEvent("input.paste") || t.isUserEvent("input.drop"))) onPaste?.();
        }),
      ],
    }),
  });
  return {
    text: () => view.state.doc.toString(),
    replace(text) {
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text }, annotations: external.of(true) });
    },
    focus(at) {
      if (at === "end") view.dispatch({ selection: { anchor: view.state.doc.length }, scrollIntoView: true });
      view.focus();
    },
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
    dispose() {
      live = false; // a language still loading is dropped
      view.destroy();
    },
  };
}
