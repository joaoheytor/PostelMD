import { afterEach, describe, expect, it, vi } from "vitest";
import { setup } from "./api";
import { importNotice } from "./app";

describe("importNotice (ADR-0020: what setup imported)", () => {
  it("says how many files the first commit holds, and to check them for secrets", () => {
    expect(importNotice({ imported: 1, skipped: 0, repositories: 0 })).toBe(
      "Imported 1 file from your notes folder as the first commit. Check it for secrets before you push this repository anywhere.",
    );
    expect(importNotice({ imported: 3, skipped: 0, repositories: 0 })).toBe(
      "Imported 3 files from your notes folder as the first commit. Check them for secrets before you push this repository anywhere.",
    );
  });

  it("says which files were too large to commit, so the notice never claims the whole folder", () => {
    expect(importNotice({ imported: 2, skipped: 1, repositories: 0 })).toContain("1 file over 50 MB was left out of git.");
    expect(importNotice({ imported: 0, skipped: 2, repositories: 0 })).toBe("2 files over 50 MB were left out of git.");
  });

  it("says how many folders were left out as repositories of their own (ADR-0026)", () => {
    expect(importNotice({ imported: 2, skipped: 0, repositories: 1 })).toMatch(
      / 1 folder is a git repository of its own and was left out of git; PostelMD doesn't commit its notes\.$/,
    );
    expect(importNotice({ imported: 0, skipped: 0, repositories: 3 })).toBe(
      "3 folders are git repositories of their own and were left out of git; PostelMD doesn't commit their notes.",
    );
  });

  it("says nothing when there was nothing to import", () => {
    expect(importNotice({ imported: 0, skipped: 0, repositories: 0 })).toBeUndefined();
  });
});

describe("setup's result", () => {
  afterEach(() => vi.unstubAllGlobals());

  const answering = (body: object) =>
    vi.stubGlobal("fetch", (() => Promise.resolve(Response.json(body, { status: 201 }))) as typeof fetch);
  const form = { token: "t", password: "p", name: "N", email: "n@example.com" };

  it("reads the count of repositories left out", async () => {
    answering({ imported: 4, skipped: 1, repositories: 2 });
    expect(await setup(form)).toEqual({ imported: 4, skipped: 1, repositories: 2 });
  });

  it("counts none when the server omits them", async () => {
    answering({ imported: 4 });
    expect(await setup(form)).toEqual({ imported: 4, skipped: 0, repositories: 0 });
  });
});
