import { defaultKeymap, history, historyKeymap } from "@codemirror/commands";
import { markdown } from "@codemirror/lang-markdown";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { Annotation, EditorState } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { tags as t } from "@lezer/highlight";
import type { Disposable } from "../core/disposable";

/** Marks a change the app made (loading the server's copy), so it isn't an edit to save. */
const external = Annotation.define<boolean>();

export interface Editor extends Disposable {
  /** The current text. Costs a full copy: call it when saving, not per keystroke. */
  text(): string;
  /** Replaces the text without reporting an edit. */
  replace(text: string): void;
  focus(): void;
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
 * stylesheets, which the CSP's style-src 'self' allows (ADR-0019). onEdit runs for the
 * user's edits (they need saving); onChange for every change of the text, including
 * replace() (the preview follows both).
 */
export function mountEditor(root: ShadowRoot, doc: string, label: string, onEdit: () => void, onChange: () => void): Editor {
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
    dispose: () => view.destroy(),
  };
}
