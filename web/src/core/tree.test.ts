import { describe, expect, it } from "vitest";
import type { Entry } from "./api";
import { join, parent, Tree } from "./tree";

describe("Tree", () => {
  it("loads a folder when it is first expanded, and reloads after a change", async () => {
    const calls: string[] = [];
    const tree = new Tree(async (p) => {
      calls.push(p);
      return [{ name: `${calls.length}.md`, kind: "file" } satisfies Entry];
    });
    await tree.load("");
    await tree.toggle("ops");
    expect(tree.state.value.expanded.has("ops")).toBe(true);
    await tree.toggle("ops");
    await tree.toggle("ops"); // already loaded: no second request
    expect(calls).toEqual(["", "ops"]);
    await tree.changed("ops/new.md");
    expect(calls).toEqual(["", "ops", "ops"]);
    expect(tree.state.value.folders.get("ops")).toEqual([{ name: "3.md", kind: "file" }]);
  });

  it("keeps the tree and reports the error when a listing fails", async () => {
    const tree = new Tree(() => Promise.reject(new Error("503")));
    await tree.load("");
    expect(tree.state.value.error).toContain("503");
    expect(tree.state.value.folders.size).toBe(0);
  });

  it("joins and splits paths", () => {
    expect(join("", "a.md")).toBe("a.md");
    expect(join("a/b", "c.md")).toBe("a/b/c.md");
    expect(parent("a/b/c.md")).toBe("a/b");
    expect(parent("c.md")).toBe("");
  });
});
