import { describe, expect, it } from "vitest";
import { batchHTML, intact, keyAttr, newKey, type NodeLike } from "./blocks";

// The DOM parsing itself has an e2e test (2-preview); these check what the preview decides
// from the nodes a batch produced. There is no DOM here, so nodes are plain objects.

function el(attrs: Record<string, string>, name = "DIV"): NodeLike {
  return { nodeType: 1, nodeName: name, getAttribute: (n) => attrs[n] ?? null };
}
const text: NodeLike = { nodeType: 3, nodeName: "#text" };

const blocks = [
  { line: 0, html: "<h1>T</h1>" },
  { line: 2, html: "<p>x</p>" },
];
const key = "k";
const wrapper = (i: number, line: number) => el({ class: "pm-block", "data-line": String(line), [keyAttr]: `${key}-${i}` });

describe("a sanitized batch", () => {
  it("wraps each block with its line and its numbered key", () => {
    expect(batchHTML(blocks, key)).toBe(
      `<div class="pm-block" data-line="0" ${keyAttr}="k-0"><h1>T</h1></div><div class="pm-block" data-line="2" ${keyAttr}="k-1"><p>x</p></div>`,
    );
  });

  it("is intact when its top level is exactly the wrappers, in order", () => {
    expect(intact([wrapper(0, 0), wrapper(1, 2)], blocks, key)).toBe(true);
  });

  it("isn't when an unclosed tag swallowed a wrapper", () => {
    expect(intact([wrapper(0, 0)], blocks, key)).toBe(false);
  });

  it("isn't when a note forged a wrapper, even with the right count and line", () => {
    const forged = el({ class: "pm-block", "data-line": "9999" });
    expect(intact([wrapper(0, 0), forged, wrapper(1, 2)], blocks, key)).toBe(false);
    // "</div><div class="pm-block" data-line="2">" plus an unclosed <div> later: two nodes, lines right.
    expect(intact([wrapper(0, 0), el({ class: "pm-block", "data-line": "2" })], blocks, key)).toBe(false);
    expect(intact([wrapper(0, 0), el({ class: "pm-block", "data-line": "2", [keyAttr]: "guess-1" })], blocks, key)).toBe(false);
  });

  it("isn't when a stray closing tag left content outside the wrappers", () => {
    expect(intact([wrapper(0, 0), text], blocks, key)).toBe(false);
    expect(intact([wrapper(0, 0), wrapper(1, 2), text], blocks, key)).toBe(false);
  });

  it("isn't when a wrapper moved or lost its line", () => {
    expect(intact([wrapper(1, 2), wrapper(0, 0)], blocks, key)).toBe(false);
    expect(intact([wrapper(0, 0), wrapper(1, 3)], blocks, key)).toBe(false);
    expect(intact([wrapper(0, 0), el({ class: "pm-block x", "data-line": "2", [keyAttr]: "k-1" })], blocks, key)).toBe(false);
  });

  it("gets a new random key for each preview", () => {
    expect(newKey()).toMatch(/^[0-9a-f]{32}$/);
    expect(newKey()).not.toBe(newKey());
  });
});
