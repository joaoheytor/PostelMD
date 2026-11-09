// Markdown → HTML off the main thread. Output is UNTRUSTED until sanitized on the main thread.
import MarkdownIt from "markdown-it";

const md = new MarkdownIt({ html: true, linkify: true });
// Source line mapping for scroll sync: every top-level block gets data-line.
md.core.ruler.push("source_lines", (state) => {
  for (const t of state.tokens) if (t.map && t.level === 0 && t.nesting !== -1) t.attrSet("data-line", String(t.map[0]));
});

export interface Block { line: number; html: string }
export type Reply = { id: number; ms: number; blocks: Block[] };

self.onmessage = (e: MessageEvent<{ id: number; text: string }>) => {
  const t0 = performance.now();
  const env = {};
  const tokens = md.parse(e.data.text, env);
  // Split at top-level block boundaries so the main thread can patch only what changed.
  const blocks: Block[] = [];
  let start = 0;
  tokens.forEach((t, i) => {
    if (t.level !== 0 || t.nesting === 1) return;
    blocks.push({ line: tokens[start]?.map?.[0] ?? 0, html: md.renderer.render(tokens.slice(start, i + 1), md.options, env) });
    start = i + 1;
  });
  (self as unknown as Worker).postMessage({ id: e.data.id, ms: performance.now() - t0, blocks } satisfies Reply);
};
