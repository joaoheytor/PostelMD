// Benchmark page for the PRD editor budget (ADR-0007): typing in a 10k-line note with the
// preview open ("editor"), against a plain <textarea> ("control") measured in the same run.
// It implements the ADR-0007 design: markdown-it in a Worker, DOMPurify on the main thread
// inside the only Trusted Types policy, batched initial render, changed-block patching.
import DOMPurify from "dompurify";
import { EditorState } from "@codemirror/state";
import { EditorView, keymap } from "@codemirror/view";
import { defaultKeymap } from "@codemirror/commands";
import { markdown } from "@codemirror/lang-markdown";
import { bigNote } from "./gen";
import type { Block, Reply } from "./worker";
import workerUrl from "./worker.ts?worker&url";

// Minimal Trusted Types typing (not yet in TypeScript's DOM lib).
interface TrustedTypePolicyFactory {
  createPolicy(name: string, rules: { createHTML(s: string): string; createScriptURL(s: string): string }):
    { createHTML(s: string): unknown; createScriptURL(s: string): unknown };
}
declare global { interface Window { trustedTypes?: TrustedTypePolicyFactory } }

interface Bench { ready: boolean; renders: number; goto(): void }
const bench: Bench = { ready: false, renders: 0, goto: () => undefined };
(window as unknown as { __bench: Bench }).__bench = bench;

const mode = new URLSearchParams(location.search).get("mode") === "control" ? "control" : "editor";
const host = document.getElementById("editor")!;
const preview = document.getElementById("preview")!;
const shadow = host.attachShadow({ mode: "open" }); // ADR-0019: CodeMirror styles need a shadow root

if (mode === "control") {
  preview.hidden = true;
  const ta = document.createElement("textarea");
  ta.className = "control";
  ta.value = bigNote();
  ta.setAttribute("aria-label", "Note content");
  shadow.append(ta);
  bench.goto = () => { ta.focus(); ta.setSelectionRange(ta.value.length / 2, ta.value.length / 2); };
  bench.ready = true;
} else {
  startEditor();
}

function startEditor(): void {
  const policy = window.trustedTypes!.createPolicy("postelmd-preview", {
    createHTML: (s: string) => DOMPurify.sanitize(s, { USE_PROFILES: { html: true } }),
    createScriptURL: (u: string) => {
      const url = new URL(u, location.href);
      if (url.origin !== location.origin || !url.pathname.startsWith("/assets/")) throw new TypeError(`blocked ${u}`);
      return url.href;
    },
  });
  const worker = new Worker(policy.createScriptURL(workerUrl) as unknown as string, { type: "module" });
  const cache = new WeakMap<Element, string>();
  let seq = 0;
  let timer = 0;
  const yieldNow = () => new Promise<void>((res) => {
    const c = new MessageChannel();
    c.port1.onmessage = () => res();
    c.port2.postMessage(0);
  });
  const sanitized = (blocks: Block[]) => {
    const tpl = document.createElement("template");
    tpl.innerHTML = policy.createHTML(blocks.map((b) => `<div data-line="${b.line}">${b.html}</div>`).join("")) as unknown as string;
    return tpl.content;
  };

  worker.onmessage = async (e: MessageEvent<Reply>) => {
    const r = e.data;
    if (r.id !== seq) return;
    const kids = preview.children;
    if (kids.length === 0) {
      for (let i = 0; i < r.blocks.length && r.id === seq; i += 200) {
        const part = r.blocks.slice(i, i + 200);
        const frag = sanitized(part);
        [...frag.children].forEach((el, j) => cache.set(el, part[j]!.html));
        preview.append(frag);
        await yieldNow();
      }
    } else {
      r.blocks.forEach((b, i) => {
        const el = kids[i];
        if (el && cache.get(el) === b.html) return;
        const next = sanitized([b]).firstElementChild!;
        cache.set(next, b.html);
        if (el) el.replaceWith(next); else preview.append(next);
      });
      while (kids.length > r.blocks.length) preview.lastElementChild!.remove();
    }
    bench.renders++;
  };

  const schedule = (text: string) => {
    clearTimeout(timer);
    timer = window.setTimeout(() => worker.postMessage({ id: ++seq, text }), 300);
  };
  const view = new EditorView({
    parent: shadow,
    state: EditorState.create({
      doc: bigNote(),
      extensions: [keymap.of(defaultKeymap), markdown(),
        EditorView.contentAttributes.of({ "aria-label": "Note content" }),
        EditorView.updateListener.of((u) => { if (u.docChanged) schedule(u.state.doc.toString()); })],
    }),
  });
  schedule(view.state.doc.toString());
  bench.goto = () => {
    view.dispatch({ selection: { anchor: view.state.doc.line(5000).to }, scrollIntoView: true });
    view.focus();
  };
  bench.ready = true;
}
