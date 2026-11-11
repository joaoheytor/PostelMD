import { describe, expect, it } from "vitest";
import { renderBlocks } from "./render";

describe("renderBlocks", () => {
  it("splits the note into top-level blocks with their source line", () => {
    const blocks = renderBlocks("# Title\n\nSome *text*.\n\n- a\n- b\n");
    expect(blocks.map((b) => b.line)).toEqual([0, 2, 4]);
    expect(blocks[0]?.html).toContain("<h1>Title</h1>");
    expect(blocks[1]?.html).toContain("<em>text</em>");
    expect(blocks[2]?.html).toContain("<ul>");
  });

  it("renders GFM: tables with alignment as classes, strikethrough, autolinks", () => {
    const [table] = renderBlocks("| a | b |\n|:--|--:|\n| 1 | 2 |\n");
    expect(table?.html).toContain('<th class="pm-align-left">a</th>');
    expect(table?.html).toContain('<td class="pm-align-right">2</td>');
    expect(table?.html).not.toContain("style=");
    expect(renderBlocks("~~gone~~")[0]?.html).toContain("<s>gone</s>");
    expect(renderBlocks("see https://example.com")[0]?.html).toContain('<a href="https://example.com">');
  });

  it("marks task list items as done or to do", () => {
    const [list] = renderBlocks("- [ ] todo\n- [x] done\n- plain\n");
    expect(list?.html).toContain('<li class="pm-task-item"><span class="pm-task" role="img" aria-label="To do"></span>todo</li>');
    expect(list?.html).toContain('<span class="pm-task pm-task-done" role="img" aria-label="Done"></span>done</li>');
    expect(list?.html).toContain("<li>plain</li>");
  });

  it("shows YAML front matter as a code block and keeps the following lines right", () => {
    const blocks = renderBlocks("---\ntitle: x\ntags: [a]\n---\n# Heading\n");
    expect(blocks[0]).toEqual({ line: 0, html: '<pre class="pm-frontmatter"><code>title: x\ntags: [a]</code></pre>' });
    expect(blocks[1]).toMatchObject({ line: 4 });
    expect(blocks[1]?.html).toContain("<h1>Heading</h1>");
  });

  it("escapes front matter", () => {
    expect(renderBlocks("---\na: <b>\n---\n")[0]?.html).toContain("a: &lt;b&gt;");
  });

  it("leaves raw HTML to the sanitizer: its output is untrusted", () => {
    expect(renderBlocks('<img src=x onerror="alert(1)">')[0]?.html).toContain("onerror");
  });

  it("keeps a <details> and the Markdown inside it as one block", () => {
    const blocks = renderBlocks("# T\n\n<details><summary>Logs</summary>\n\nRun **this**.\n\n- a\n\n</details>\n\nAfter.\n");
    expect(blocks.map((b) => b.line)).toEqual([0, 2, 10]);
    expect(blocks[1]?.html).toMatch(/^<details><summary>Logs<\/summary>\n<p>Run <strong>this<\/strong>.<\/p>\n<ul>[\s\S]*<\/ul>\n<\/details>\n$/);
    expect(blocks[2]?.html).toBe("<p>After.</p>\n");
  });

  it("counts nested containers, and ignores tags in comments and void or self-closed ones", () => {
    const text = "<div>\n<!-- </div> -->\n\n<details>\n\nA<br>\n\n</details>\n<div />\n\n</div>\n\nB\n";
    const blocks = renderBlocks(text);
    expect(blocks).toHaveLength(2);
    expect(blocks[0]?.html).toContain("<p>A<br></p>");
    expect(blocks[1]).toEqual({ line: 12, html: "<p>B</p>\n" });
  });

  it("groups nothing for a container never closed: the rest of the note stays in blocks", () => {
    // Typing "<details>" before its "</details>" must not make the rest of the note one block.
    const blocks = renderBlocks("Before.\n\n<div>\n\nOne.\n\nTwo.\n");
    expect(blocks.map((b) => b.line)).toEqual([0, 2, 4, 6]);
    // Closed later: grouped, however far; a second unclosed one after it isn't.
    expect(renderBlocks("<div>\n\nA\n\n</div>\n\n<div>\n\nB\n").map((b) => b.line)).toEqual([0, 6, 8]);
  });

  it("takes linear time on an HTML block full of unclosed tags", () => {
    // "<div" then "<a" over and over, no ">" and no blank line: one HTML block that a
    // backtracking tag pattern scans to its end from every "<" (security review of #117).
    const note = (n: number) => `<div\n${"<a".repeat(n)}\n`;
    const median = (n: number) => {
      const runs: number[] = [];
      for (let i = 0; i < 5; i++) {
        const t0 = performance.now();
        renderBlocks(note(n));
        runs.push(performance.now() - t0);
      }
      return runs.sort((a, b) => a - b)[2] ?? 0;
    };
    const n = 8_000;
    median(n); // warm up
    const small = median(n);
    const large = median(4 * n);
    // Linear: about 4×; quadratic: about 16×.
    expect(large / Math.max(small, 0.05)).toBeLessThan(9);
    expect(large).toBeLessThan(5_000); // a hang guard only
  }, 60_000);

  it("splits HTML blocks that close what they open as before", () => {
    expect(renderBlocks("<div>a</div>\n\n<p>b</p>\n\nc\n").map((b) => b.line)).toEqual([0, 2, 4]);
  });

  it("renders an empty note as no blocks", () => {
    expect(renderBlocks("")).toEqual([]);
  });
});
