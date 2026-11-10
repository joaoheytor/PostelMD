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
  let start = 0;
  tokens.forEach((t, i) => {
    if (t.level !== 0 || t.nesting === 1) return;
    const line = (tokens[start]?.map?.[0] ?? 0) + offset;
    blocks.push({ line, html: md.renderer.render(tokens.slice(start, i + 1), md.options, env) });
    start = i + 1;
  });
  return blocks;
}
