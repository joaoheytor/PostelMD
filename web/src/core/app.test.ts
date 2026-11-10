import { describe, expect, it } from "vitest";
import { importNotice } from "./app";

describe("importNotice (ADR-0020: what setup imported)", () => {
  it("says how many files the first commit holds, and to check them for secrets", () => {
    expect(importNotice({ imported: 1, skipped: 0 })).toBe(
      "Imported 1 file from your notes folder as the first commit. Check them for secrets before you push this repository anywhere.",
    );
    expect(importNotice({ imported: 3, skipped: 0 })).toMatch(/^Imported 3 files /);
  });

  it("says which files were too large to commit, so the notice never claims the whole folder", () => {
    expect(importNotice({ imported: 2, skipped: 1 })).toContain("1 file over 50 MB was left out of git.");
    expect(importNotice({ imported: 0, skipped: 2 })).toBe("2 files over 50 MB were left out of git.");
  });

  it("says nothing when there was nothing to import", () => {
    expect(importNotice({ imported: 0, skipped: 0 })).toBeUndefined();
  });
});
