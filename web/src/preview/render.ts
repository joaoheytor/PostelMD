import MarkdownIt, { type StateCore } from "markdown-it";

/** One top-level block of the note: its first source line, and its HTML (untrusted). */
export interface Block {
  line: number;
  html: string;
}

/**
 * CommonMark + GFM (ADR-0007). Raw HTML is passed through: the output is untrusted until
 * the main thread sanitizes it (sanitize.ts). Nothing here needs a DOM, so it runs in the
 * preview Worker.
 */
const md = new MarkdownIt({ html: true, linkify: true });
md.core.ruler.push("postelmd_gfm", (state) => {
  tableAlignment(state);
  taskLists(state);
});

/** markdown-it aligns table cells with style="text-align:…"; the CSP allows no inline styles. */
function tableAlignment(state: StateCore): void {
  for (const t of state.tokens) {
    if (t.type !== "th_open" && t.type !== "td_open") continue;
    const align = /text-align:(left|right|center)/.exec(String(t.attrGet("style") ?? ""))?.[1];
    if (!align) continue;
    t.attrs = (t.attrs ?? []).filter(([name]) => name !== "style");
    t.attrJoin("class", `pm-align-${align}`);
  }
}

/**
 * "- [ ] x" and "- [x] x" get a read-only marker, named "To do" or "Done" for screen readers
 * (no <input>: forms are stripped).
 */
function taskLists(state: StateCore): void {
  const tokens = state.tokens;
  for (let i = 2; i < tokens.length; i++) {
    const inline = tokens[i];
    const item = tokens[i - 2];
    if (inline?.type !== "inline" || tokens[i - 1]?.type !== "paragraph_open" || item?.type !== "list_item_open") continue;
    const first = inline.children?.[0];
    const m = first?.type === "text" ? /^\[([ xX])\] /.exec(first.content) : null;
    if (!first || !m) continue;
    first.content = first.content.slice(m[0].length);
    const box = new state.Token("html_inline", "", 0);
    const done = m[1] !== " ";
    box.content = `<span class="pm-task${done ? " pm-task-done" : ""}" role="img" aria-label="${done ? "Done" : "To do"}"></span>`;
    inline.children?.unshift(box);
    item.attrJoin("class", "pm-task-item");
  }
}

const frontMatter = /^---\r?\n([\s\S]*?)\r?\n---(?:\r?\n|$)/;

function escape(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

/** Renders the note as top-level blocks, so the preview can replace only those that changed. */
export function renderBlocks(text: string): Block[] {
  const blocks: Block[] = [];
  let offset = 0;
  const fm = frontMatter.exec(text);
  if (fm) {
    blocks.push({ line: 0, html: `<pre class="pm-frontmatter"><code>${escape(fm[1] ?? "")}</code></pre>` });
    offset = fm[0].split("\n").length - (fm[0].endsWith("\n") ? 1 : 0);
    text = text.slice(fm[0].length);
  }
  const env = {};
  const tokens = md.parse(text, env);
  const closes = closingAt(tokens);
  let start = 0;
  let until = -1; // the token that closes the container being grouped, if any
  tokens.forEach((t, i) => {
    if (i > until) until = closes[i] ?? -1;
    if (t.level !== 0 || t.nesting === 1 || i < until) return;
    const line = (tokens[start]?.map?.[0] ?? 0) + offset;
    blocks.push({ line, html: md.renderer.render(tokens.slice(start, i + 1), md.options, env) });
    start = i + 1;
  });
  return blocks;
}

/**
 * For each top-level HTML block that opens containers, the index of the HTML block that
 * closes them all; -1 when none does. A container never closed (typed before its closing
 * tag) groups nothing, so the rest of the note isn't re-rendered as one block. Linear: the
 * close of a block that opens is the next token at or below the depth before it.
 */
function closingAt(tokens: readonly { type: string; level: number; content: string }[]): number[] {
  // depth[k] is the depth after token k-1; depth[0] = 0 is the note's start.
  const depth = [0];
  for (const t of tokens) depth.push((depth.at(-1) ?? 0) + (t.type === "html_block" && t.level === 0 ? containerDepth(t.content) : 0));
  // atOrBelow[k]: the first k' > k with depth[k'] <= depth[k] (a monotonic stack), or -1.
  const atOrBelow = new Array<number>(depth.length).fill(-1);
  const stack: number[] = [];
  depth.forEach((v, k) => {
    while (stack.length && (depth[stack.at(-1) ?? 0] ?? 0) >= v) atOrBelow[stack.pop() ?? 0] = k;
    stack.push(k);
  });
  // Token i opens when its depth rises; it is closed by token atOrBelow[i] - 1.
  return tokens.map((_, i) => ((depth[i + 1] ?? 0) > (depth[i] ?? 0) && (atOrBelow[i] ?? -1) > 0 ? (atOrBelow[i] ?? 0) - 1 : -1));
}

/**
 * Raw HTML containers whose content can be Markdown blocks: "<details>", blank line, a
 * paragraph, "</details>" are three HTML and Markdown blocks to markdown-it, but one element.
 * Rendered apart, the preview would put the paragraph outside it.
 */
const containers = new Set(["details", "div", "section", "article", "aside", "figure", "blockquote", "header", "footer", "nav", "main"]);

/** How many containers an HTML block opens (positive) or closes (negative). */
function containerDepth(html: string): number {
  let depth = 0;
  // [^<>], not [^>]: a "<" without its ">" ends the attempt there, so no "<" is scanned to
  // the end of the block from every one before it (quadratic; security review of #117).
  const tags = /<(\/?)([a-zA-Z][a-zA-Z0-9-]*)\b[^<>]*?(\/?)>/g;
  for (const m of html.replace(/<!--[\s\S]*?(?:-->|$)/g, "").matchAll(tags)) {
    if (!containers.has((m[2] ?? "").toLowerCase()) || m[3]) continue;
    depth += m[1] ? -1 : 1;
  }
  return depth;
}
