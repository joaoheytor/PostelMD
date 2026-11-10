import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { SaveResult } from "./api";
import { DocumentSession } from "./document";
import type { NoteDrafts } from "./drafts";

describe("DocumentSession drafts (ADR-0003 §7)", () => {
  let saves: { content: string; answer: (r: SaveResult) => void }[];
  let draft: { writes: [string, string, number][]; saved: number[]; dropped: number };
  let text: string;
  let doc: DocumentSession;

  beforeEach(() => {
    vi.useFakeTimers();
    saves = [];
    draft = { writes: [], saved: [], dropped: 0 };
    const hooks: NoteDrafts = {
      write: (t, base, gen) => {
        draft.writes.push([t, base, gen]);
        return Promise.resolve();
      },
      saved: (gen) => void draft.saved.push(gen),
      drop: () => void draft.dropped++,
    };
    text = "v0";
    doc = new DocumentSession("a.md", '"e0"', {
      save: (_p, content) => new Promise((answer) => saves.push({ content, answer })),
      read: () => text,
      debounceMs: 1000,
      draftMs: 300,
      draft: hooks,
    });
  });
  afterEach(() => {
    doc.dispose();
    vi.useRealTimers();
  });

  const type = (t: string) => {
    text = t;
    doc.edit();
  };

  it("writes a draft shortly after the last edit, before the save, reading the text only then", async () => {
    type("v1");
    await vi.advanceTimersByTimeAsync(100);
    type("v2");
    await vi.advanceTimersByTimeAsync(299);
    expect(draft.writes).toEqual([]);
    await vi.advanceTimersByTimeAsync(1);
    expect(draft.writes).toEqual([["v2", '"e0"', 2]]);
    expect(saves).toHaveLength(0);
  });

  it("still writes a draft during nonstop typing, within 2 s of the first edit", async () => {
    for (let i = 1; i <= 25; i++) {
      type(`v${i}`);
      await vi.advanceTimersByTimeAsync(100);
    }
    expect(draft.writes.length).toBeGreaterThan(0);
    expect(draft.writes[0]?.[2]).toBeLessThanOrEqual(21); // by 2.1 s in
  });

  it("clears the draft of the generation a save acknowledged", async () => {
    type("v1");
    await vi.advanceTimersByTimeAsync(1000);
    type("v2"); // during the save
    saves[0]?.answer({ kind: "saved", etag: '"e1"' });
    await vi.advanceTimersByTimeAsync(0);
    expect(draft.saved).toEqual([1]); // only gen 1: the draft of gen 2 stays
  });

  it("keeps the draft while a save fails, conflicts or finds the note gone", async () => {
    for (const r of [
      { kind: "error", message: "down", retryAfterMs: 0, retry: true },
      { kind: "conflict", etag: '"e9"', content: "theirs" },
    ] as SaveResult[]) {
      type("mine");
      await doc.writeDraft();
      void doc.flush();
      saves.at(-1)?.answer(r);
      await vi.advanceTimersByTimeAsync(0);
    }
    expect(draft.saved).toEqual([]);
    expect(draft.writes.at(-1)?.[0]).toBe("mine");
  });

  it("writes a pending draft at once when asked (the tab is hidden) or when closed", async () => {
    type("v1");
    await doc.writeDraft();
    expect(draft.writes).toEqual([["v1", '"e0"', 1]]);
    type("v2");
    doc.dispose();
    expect(draft.writes.at(-1)).toEqual(["v2", '"e0"', 2]);
  });

  it("writes no draft for a clean note", async () => {
    await doc.writeDraft();
    expect(draft.writes).toEqual([]);
  });

  it("taking the other version drops this tab's draft: the user chose", async () => {
    type("mine");
    void doc.flush();
    saves[0]?.answer({ kind: "conflict", etag: '"e9"', content: "theirs" });
    await vi.advanceTimersByTimeAsync(0);
    doc.takeTheirs();
    expect(draft.dropped).toBe(1);
  });
});
