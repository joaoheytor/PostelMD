import { describe, expect, it } from "vitest";
import { nameProblem, noteFile, noteTitle } from "./names";
import { moveTarget } from "./tree";

describe("note names", () => {
  it("adds .md unless the name already ends in .md, in any case", () => {
    expect(noteFile("Disk full")).toBe("Disk full.md");
    expect(noteFile("nginx.conf")).toBe("nginx.conf.md");
    expect(noteFile("v1.2")).toBe("v1.2.md");
    expect(noteFile("notes.txt")).toBe("notes.txt.md");
    expect(noteFile("db.md")).toBe("db.md");
    expect(noteFile("DB.MD")).toBe("DB.MD");
  });

  it("takes the title from the name without a trailing .md only", () => {
    expect(noteTitle("nginx.conf")).toBe("nginx.conf");
    expect(noteTitle("db.md")).toBe("db");
    expect(noteTitle("DB.Md")).toBe("DB");
    expect(noteTitle("v1.2")).toBe("v1.2");
  });

  it("refuses a note whose own name starts with a dot; its folders may", () => {
    expect(nameProblem(".hidden.md")).toMatch(/^A note name can't start with a dot \("\.hidden\.md"\)/);
    expect(nameProblem("ops/.env.md")).toMatch(/can't start with a dot/);
    expect(nameProblem(".github/pr-checklist.md")).toBeUndefined();
    expect(nameProblem("nginx.conf.md")).toBeUndefined();
    expect(nameProblem("ops/runbook.md")).toBeUndefined();
    // Not a note: a folder or another file is not checked.
    expect(nameProblem(".config")).toBeUndefined();
    expect(nameProblem("ops/.env")).toBeUndefined();
  });

  it("applies the same rule to a renamed note", () => {
    expect(moveTarget("a/b.md", "nginx.conf", "file")).toBe("nginx.conf.md");
    expect(moveTarget("a/b.md", "c/v1.2", "file")).toBe("c/v1.2.md");
    expect(moveTarget("a/b.txt", "c.conf", "file")).toBe("c.conf.txt");
    expect(moveTarget("a/b.md", "c.MD", "file")).toBe("c.MD");
    expect(moveTarget("a/b.txt", "c.md", "file")).toBe("c.md"); // still a note
    expect(moveTarget("a/b", "c.d", "folder")).toBe("c.d");
    // A file that isn't a note keeps its own name when restored or moved as it was.
    expect(moveTarget("nginx.conf", "nginx.conf", "file")).toBe("nginx.conf");
    expect(moveTarget("nginx.conf", "ops/nginx", "file")).toBe("ops/nginx.conf");
  });
});
