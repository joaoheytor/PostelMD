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

  it("runs an unclosed <div> to the end of the note, as the browser would", () => {
    const blocks = renderBlocks("Before.\n\n<div>\n\nOne.\n\nTwo.\n");
    expect(blocks.map((b) => b.line)).toEqual([0, 2]);
    expect(blocks[1]?.html).toBe("<div>\n<p>One.</p>\n<p>Two.</p>\n");
  });

  it("splits HTML blocks that close what they open as before", () => {
    expect(renderBlocks("<div>a</div>\n\n<p>b</p>\n\nc\n").map((b) => b.line)).toEqual([0, 2, 4]);
  });

  it("renders an empty note as no blocks", () => {
    expect(renderBlocks("")).toEqual([]);
  });
});
