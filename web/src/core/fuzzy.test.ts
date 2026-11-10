import { describe, expect, it } from "vitest";
import { match, rank } from "./fuzzy";

const top = (files: string[], q: string) => rank(files, q, 10).map((m) => m.path);

describe("fuzzy match", () => {
  it("matches the query's characters in order, ignoring case and spaces", () => {
    expect(match("ops/Runbook.md", "rnbk")).not.toBeNull();
    expect(match("ops/runbook.md", "run book")).not.toBeNull();
    expect(match("ops/runbook.md", "kobnur")).toBeNull();
  });

  it("returns the matched positions, for highlighting", () => {
    expect(match("ops/db.md", "db")?.positions).toEqual([4, 5]);
  });

  it("prefers word starts over letters inside words", () => {
    expect(top(["afro.md", "failover-redis.md"], "fr")).toEqual(["failover-redis.md", "afro.md"]);
  });

  it("prefers a match in the file name over one in the folders", () => {
    expect(top(["db/readme.md", "ops/db.md"], "db")).toEqual(["ops/db.md", "db/readme.md"]);
  });

  it("prefers consecutive letters", () => {
    expect(top(["ops/run/book-club.md", "ops/runbook.md"], "runbook")[0]).toBe("ops/runbook.md");
  });

  it("puts the shorter path first when the matches are alike", () => {
    expect(top(["archive/2024/notes/a.md", "a.md"], "a")).toEqual(["a.md", "archive/2024/notes/a.md"]);
  });

  it("with an empty query, lists files in order, up to the limit", () => {
    expect(rank(["b.md", "a.md", "c.md"], "", 2).map((m) => m.path)).toEqual(["a.md", "b.md"]);
  });

  it("keeps only the best matches, up to the limit", () => {
    const files = Array.from({ length: 1000 }, (_, i) => `notes/${i}.md`).concat(["postmortem-2026.md"]);
    expect(rank(files, "pm", 5)[0]?.path).toBe("postmortem-2026.md");
    expect(rank(files, "pm", 5)).toHaveLength(1);
  });
});
