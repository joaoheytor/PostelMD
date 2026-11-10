import { describe, expect, it } from "vitest";
import { blockAt, lineAt } from "./sync";

describe("scroll sync mapping", () => {
  const lines = [0, 4, 10, 30];

  it("finds the block that holds a line", () => {
    expect(blockAt(lines, 0)).toEqual({ index: 0, fraction: 0 });
    expect(blockAt(lines, 2)).toEqual({ index: 0, fraction: 0.5 });
    expect(blockAt(lines, 10)).toEqual({ index: 2, fraction: 0 });
    expect(blockAt(lines, 20)).toEqual({ index: 2, fraction: 0.5 });
    expect(blockAt(lines, 99)).toEqual({ index: 3, fraction: 0 }); // the last block: no end to measure
    expect(blockAt([], 5)).toBeUndefined();
    expect(blockAt([3], 1)).toEqual({ index: 0, fraction: 0 }); // before the first block
  });

  it("finds the line a position in a block stands for", () => {
    expect(lineAt(lines, 1, 0.5)).toBe(7);
    expect(lineAt(lines, 3, 0.5)).toBe(30);
    expect(lineAt(lines, 0, 0)).toBe(0);
  });
});
