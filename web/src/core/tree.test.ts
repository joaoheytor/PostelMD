import { describe, expect, it } from "vitest";
import type { Entry } from "./api";
import { join, moveTarget, parent, rebase, Tree } from "./tree";

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

  it("forgets a moved folder and reloads both parents", async () => {
    const calls: string[] = [];
    const tree = new Tree(async (p) => {
      calls.push(p);
      return [];
    });
    await tree.load("");
    await tree.toggle("ops");
    await tree.toggle("ops/db");
    await tree.toggle("ops-old");
    calls.length = 0;
    await tree.moved("ops", "archive/ops");
    const s = tree.state.value;
    expect([...s.folders.keys()].sort()).toEqual(["", "archive", "ops-old"]);
    expect(s.expanded.has("ops") || s.expanded.has("ops/db")).toBe(false);
    expect(s.expanded.has("ops-old")).toBe(true);
    expect(calls.sort()).toEqual(["", "archive"]);
    calls.length = 0;
    await tree.moved("ops-old", "ops-2020");
    expect(calls).toEqual([""]); // a rename reloads its folder once
  });

  it("rebases paths inside a moved item", () => {
    expect(rebase("ops/db/a.md", "ops", "archive/ops")).toBe("archive/ops/db/a.md");
    expect(rebase("ops", "ops", "x")).toBe("x");
    expect(rebase("ops-old/a.md", "ops", "x")).toBeUndefined();
  });

  it("normalizes a typed move target", () => {
    expect(moveTarget("a/b.md", " c/d.md ", "file")).toBe("c/d.md");
    expect(moveTarget("a/b.md", "/c/d/", "file")).toBe("c/d.md");
    expect(moveTarget("a/b.md", "c/d", "file")).toBe("c/d.md");
    expect(moveTarget("a/b.txt", "c", "file")).toBe("c.txt");
    expect(moveTarget("a/b", "c/d", "folder")).toBe("c/d");
    expect(moveTarget("a/b.md", "  ", "file")).toBe("");
  });

  it("joins and splits paths", () => {
    expect(join("", "a.md")).toBe("a.md");
    expect(join("a/b", "c.md")).toBe("a/b/c.md");
    expect(parent("a/b/c.md")).toBe("a/b");
    expect(parent("c.md")).toBe("");
  });
});
