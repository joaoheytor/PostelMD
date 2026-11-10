import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { SaveResult } from "./api";
import { DocumentSession } from "./document";
import { Drafts, MemoryDraftBackend, type NoteDrafts } from "./drafts";

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

  describe("closed while its save is in flight", () => {
    let backend: MemoryDraftBackend;
    let closing: DocumentSession;

    beforeEach(async () => {
      backend = new MemoryDraftBackend();
      const drafts = await Drafts.open(backend, "me", () => "moved");
      closing = new DocumentSession("a.md", '"e0"', {
        save: (_p, content) => new Promise((answer) => saves.push({ content, answer })),
        read: () => text,
        debounceMs: 1000,
        draft: drafts.forNote("a.md"),
      });
      text = "v1";
      closing.edit();
      void closing.flush(); // the save of gen 1 is in flight
      closing.dispose(); // closing writes the draft of gen 1
      await drafts.idle();
    });

    // After a reload this tab's leftover drafts are offered like a closed tab's.
    const offeredAfterReload = async () => (await Drafts.open(backend, "me", () => "reloaded")).offered("a.md");

    it("the save that lands clears the draft it covered: reopening offers no stale draft", async () => {
      expect((await backend.all()).map((d) => d.gen)).toEqual([1]);
      saves.at(-1)?.answer({ kind: "saved", etag: '"e1"' });
      await vi.advanceTimersByTimeAsync(0);
      expect(await backend.all()).toEqual([]);
      expect(await offeredAfterReload()).toEqual([]);
    });

    it("a save that fails keeps the draft", async () => {
      saves.at(-1)?.answer({ kind: "error", message: "down", retryAfterMs: 0, retry: true });
      await vi.advanceTimersByTimeAsync(0);
      expect((await offeredAfterReload()).map((d) => d.text)).toEqual(["v1"]);
    });
  });

  it("an edit made after the save started keeps its draft when the save lands after closing", async () => {
    type("v1");
    void doc.flush(); // gen 1 in flight
    type("v2");
    doc.dispose(); // the draft holds gen 2
    saves.at(-1)?.answer({ kind: "saved", etag: '"e1"' });
    await vi.advanceTimersByTimeAsync(0);
    expect(draft.writes.at(-1)).toEqual(["v2", '"e0"', 2]);
    expect(draft.saved).toEqual([1]); // clears only up to gen 1: the gen 2 draft stays
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
