import { describe, expect, it } from "vitest";
import type { Entry } from "./api";
import { ancestors, join, moveTarget, parent, rebase, Tree } from "./tree";

/** A vault of folders: each path lists its subfolders; anything not listed is empty. */
function lister(folders: Record<string, string[]>, calls: string[] = []) {
  return async (p: string): Promise<Entry[]> => {
    calls.push(p);
    return (folders[p] ?? []).map((name) => ({ name, kind: "folder" }));
  };
}

describe("Tree.reveal and Tree.missing", () => {
  it("expands every folder above a note, loading only those not loaded yet", async () => {
    const calls: string[] = [];
    const tree = new Tree(lister({ "": ["ops"], ops: ["db"] }, calls));
    await tree.load("");
    await tree.reveal("ops/db/failover.md");
    const s = tree.state.value;
    expect([...s.expanded].sort()).toEqual(["", "ops", "ops/db"]);
    expect(calls.sort()).toEqual(["", "ops", "ops/db"]);
    await tree.reveal("ops/db/other.md");
    expect(calls).toHaveLength(3);
    expect(ancestors("a.md")).toEqual([""]);
  });

  it("knows a folder is missing only from a loaded listing, checked again first", async () => {
    const vault: Record<string, string[]> = { "": ["runbooks"] };
    const calls: string[] = [];
    const tree = new Tree(lister(vault, calls));
    expect(await tree.missing("runbooks/tls")).toBe(false); // nothing loaded: can't tell
    await tree.load("");
    expect(await tree.missing("")).toBe(false);
    expect(await tree.missing("runbooks")).toBe(false);
    expect(await tree.missing("runbooks/tls")).toBe(false); // runbooks isn't loaded
    await tree.load("runbooks");
    calls.length = 0;
    expect(await tree.missing("runbooks/tls")).toBe(true);
    expect(calls).toEqual(["runbooks"]); // reloaded before saying so
    expect(await tree.missing("nowhere/deep")).toBe(true);

    vault["runbooks"] = ["tls"]; // created outside the editor since
    expect(await tree.missing("runbooks/tls")).toBe(false);
  });

  it("can't tell when the listing fails to load again", async () => {
    let fail = false;
    const tree = new Tree(async () => {
      if (fail) throw new Error("offline");
      return [];
    });
    await tree.load("");
    fail = true;
    expect(await tree.missing("x")).toBe(false);
  });
});

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
    expect(s.status).toBe("Moved ops to archive/ops."); // for the "File tree status" region
    tree.announce("");
    expect(tree.state.value.status).toBe("");
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
