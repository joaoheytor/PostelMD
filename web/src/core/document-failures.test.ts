import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { SaveResult } from "./api";
import { DocumentSession } from "./document";
import type { NoteDrafts } from "./drafts";

/**
 * What a session does when the world misbehaves: a save that never answers, an editor that is
 * gone, a draft store that refuses writes. Each must keep the edit, never claim it is safe.
 */
describe("DocumentSession failures", () => {
  let saves: { content: string; base: string; answer: (r: SaveResult) => void }[];
  let writes: [string, number][];
  let failWrites: boolean;
  let text: string | undefined;
  let doc: DocumentSession;

  beforeEach(() => {
    vi.useFakeTimers();
    saves = [];
    writes = [];
    failWrites = false;
    const draft: NoteDrafts = {
      write: (t, _base, gen) => {
        if (failWrites) return Promise.reject(new DOMException("quota", "QuotaExceededError"));
        writes.push([t, gen]);
        return Promise.resolve();
      },
      saved: () => {},
      drop: () => {},
      close: () => {},
    };
    text = "v0";
    doc = new DocumentSession("a.md", '"e0"', {
      save: (_p, content, base) => new Promise((answer) => saves.push({ content, base, answer })),
      read: () => text,
      debounceMs: 1000,
      draftMs: 300,
      draft,
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

  describe("a save that times out (its outcome is unknown)", () => {
    it("keeps the note dirty, says it is retrying, never 'clean', and retries the same base", async () => {
      type("v1");
      void doc.flush();
      saves[0]?.answer({ kind: "unknown" });
      await vi.advanceTimersByTimeAsync(0);
      expect(doc.dirty).toBe(true);
      expect(doc.state.value.status).toBe("stalled");
      await vi.advanceTimersByTimeAsync(1000); // the backoff
      expect(saves).toHaveLength(2);
      // The bytes may have landed: the retry sends them again over the same base, and the
      // server answers an identical retry with 204 (ADR-0027).
      expect(saves[1]).toMatchObject({ content: "v1", base: '"e0"' });
      saves[1]?.answer({ kind: "saved", etag: '"e1"' });
      await vi.advanceTimersByTimeAsync(0);
      expect(doc.state.value.status).toBe("clean");
      expect(doc.dirty).toBe(false);
    });

    it("an edit made while stalled is in the retry, and only the retry's generation clears it", async () => {
      type("v1");
      void doc.flush();
      saves[0]?.answer({ kind: "unknown" });
      await vi.advanceTimersByTimeAsync(0);
      type("v2");
      expect(doc.state.value.status).toBe("stalled"); // typing doesn't hide the stall
      await vi.advanceTimersByTimeAsync(1000);
      expect(saves.at(-1)?.content).toBe("v2");
      type("v3"); // during the retry
      saves.at(-1)?.answer({ kind: "saved", etag: '"e1"' });
      await vi.advanceTimersByTimeAsync(0);
      expect(doc.dirty).toBe(true);
      expect(doc.state.value.status).toBe("dirty");
    });

    it("flush reports the edit as not saved", async () => {
      type("v1");
      const flushed = doc.flush();
      saves[0]?.answer({ kind: "unknown" });
      await flushed;
      expect(doc.dirty).toBe(true);
    });

    it("typing while stalled doesn't reset the failure count: the next stall backs off 2 s, not 1 s", async () => {
      type("v1");
      void doc.flush();
      saves[0]?.answer({ kind: "unknown" }); // backoff 1 s
      await vi.advanceTimersByTimeAsync(0);
      type("v2");
      await vi.advanceTimersByTimeAsync(1000); // the debounce sends v2
      saves[1]?.answer({ kind: "unknown" }); // backoff 2 s
      await vi.advanceTimersByTimeAsync(1999);
      expect(saves).toHaveLength(2);
      await vi.advanceTimersByTimeAsync(1);
      expect(saves).toHaveLength(3);
    });

    it("a retry doesn't announce 'saving' again: the status stays on the stall until it lands", async () => {
      type("v1");
      void doc.flush();
      saves[0]?.answer({ kind: "unknown" });
      await vi.advanceTimersByTimeAsync(1000);
      expect(saves).toHaveLength(2);
      expect(doc.state.value.status).toBe("stalled");
    });

    describe("then a 412 whose body is the stalled save's bytes (it had landed)", () => {
      const stallThenType = async () => {
        type("v1");
        void doc.flush();
        saves[0]?.answer({ kind: "unknown" });
        await vi.advanceTimersByTimeAsync(0);
        type("v2");
        await vi.advanceTimersByTimeAsync(1000);
        expect(saves[1]).toMatchObject({ content: "v2", base: '"e0"' });
      };

      it("is no conflict: the newest text is saved at once over the 412's ETag", async () => {
        await stallThenType();
        saves[1]?.answer({ kind: "conflict", etag: '"e1"', content: "v1" });
        await vi.advanceTimersByTimeAsync(0);
        expect(doc.state.value.status).not.toBe("conflict");
        expect(saves[2]).toMatchObject({ content: "v2", base: '"e1"' });
        saves[2]?.answer({ kind: "saved", etag: '"e2"' });
        await vi.advanceTimersByTimeAsync(0);
        expect(doc.state.value.status).toBe("clean");
      });

      it("a 412 whose other version isn't text is a conflict, even when a stalled save sent the same (empty) text", async () => {
        type("");
        void doc.flush();
        saves[0]?.answer({ kind: "unknown" });
        await vi.advanceTimersByTimeAsync(0);
        type("");
        await vi.advanceTimersByTimeAsync(1000);
        // A binary body has no text to compare: content "" must not match the stalled "".
        saves[1]?.answer({ kind: "conflict", etag: '"e1"', content: "", binary: true });
        await vi.advanceTimersByTimeAsync(0);
        expect(doc.state.value).toMatchObject({ status: "conflict", conflict: { etag: '"e1"', binary: true } });
      });

      it("after two stalls in a row, a 412 with the first one's bytes is no conflict either", async () => {
        await stallThenType(); // v1 stalled (it landed); the retry of v2 is in flight
        saves[1]?.answer({ kind: "unknown" }); // v2 stalls too, and didn't land
        await vi.advanceTimersByTimeAsync(0);
        type("v3");
        await vi.advanceTimersByTimeAsync(2000);
        expect(saves[2]).toMatchObject({ content: "v3", base: '"e0"' });
        saves[2]?.answer({ kind: "conflict", etag: '"e1"', content: "v1" });
        await vi.advanceTimersByTimeAsync(0);
        expect(doc.state.value.status).not.toBe("conflict");
        expect(saves[3]).toMatchObject({ content: "v3", base: '"e1"' });
      });

      it("a draft restored during a stall goes through the conflict view, even on the stalled bytes (ADR-0023)", async () => {
        type("v1");
        void doc.flush();
        saves[0]?.answer({ kind: "unknown" }); // v1 landed, unknown to the tab
        await vi.advanceTimersByTimeAsync(0);
        doc.rebase('"e-draft"'); // the user restores a draft based on an older version
        type("draft text");
        await vi.advanceTimersByTimeAsync(1000);
        expect(saves[1]).toMatchObject({ content: "draft text", base: '"e-draft"' });
        saves[1]?.answer({ kind: "conflict", etag: '"e1"', content: "v1" });
        await vi.advanceTimersByTimeAsync(0);
        expect(doc.state.value.status).toBe("conflict");
      });

      it("a 412 with other bytes is still a conflict", async () => {
        await stallThenType();
        saves[1]?.answer({ kind: "conflict", etag: '"e1"', content: "someone else's" });
        await vi.advanceTimersByTimeAsync(0);
        expect(doc.state.value.status).toBe("conflict");
      });

      it("once a save lands, the stalled bytes count for nothing", async () => {
        type("v1");
        void doc.flush();
        saves[0]?.answer({ kind: "unknown" });
        await vi.advanceTimersByTimeAsync(1000);
        saves[1]?.answer({ kind: "saved", etag: '"e1"' });
        await vi.advanceTimersByTimeAsync(0);
        type("v2");
        void doc.flush();
        saves[2]?.answer({ kind: "conflict", etag: '"e9"', content: "v1" }); // reverted elsewhere
        await vi.advanceTimersByTimeAsync(0);
        expect(doc.state.value.status).toBe("conflict");
      });
    });
  });

  describe("a save that fails, then is retried", () => {
    it("doesn't announce 'saving' again on the retry: the failure stays shown until it lands", async () => {
      type("v1");
      void doc.flush();
      saves[0]?.answer({ kind: "error", message: "the server is busy", next: "Retrying", retryAfterMs: 0, retry: true });
      await vi.advanceTimersByTimeAsync(1000);
      expect(saves).toHaveLength(2);
      expect(doc.state.value).toMatchObject({ status: "error", message: "the server is busy", next: "Retrying" });
    });
  });

  describe("with the editor gone (no text to read)", () => {
    it("never saves or drafts: there is no text to fall back on", async () => {
      type("v1");
      text = undefined; // the editor was unmounted
      await vi.advanceTimersByTimeAsync(5000);
      void doc.flush();
      await vi.advanceTimersByTimeAsync(0);
      expect(saves).toEqual([]);
      expect(await doc.writeDraft()).toBe(false);
      expect(writes).toEqual([]);
      expect(doc.dirty).toBe(true);
    });

    it("tries again later, so an editor that mounts after the edit (a converted open) still saves", async () => {
      type("v1");
      text = undefined; // the edit came before the editor
      await vi.advanceTimersByTimeAsync(1000);
      expect(saves).toEqual([]);
      text = "v1"; // the editor mounted
      await vi.advanceTimersByTimeAsync(1000);
      expect(saves).toMatchObject([{ content: "v1", base: '"e0"' }]);
    });
  });

  describe("a draft write that fails (quota, private mode)", () => {
    it("isn't counted as a draft: writeDraft says so, and the next write tries again", async () => {
      failWrites = true;
      type("v1");
      expect(await doc.writeDraft()).toBe(false);
      failWrites = false;
      expect(await doc.writeDraft()).toBe(true); // the same generation, now kept
      expect(writes).toEqual([["v1", 1]]);
    });

    it("is retried on the next edit's draft", async () => {
      failWrites = true;
      type("v1");
      await vi.advanceTimersByTimeAsync(300);
      failWrites = false;
      type("v2");
      await vi.advanceTimersByTimeAsync(300);
      expect(writes).toEqual([["v2", 2]]);
    });

    it("raises the warning once, and clears it when a draft is written again", async () => {
      const seen: boolean[] = [];
      doc.draftFailed.subscribe((v) => seen.push(v));
      failWrites = true;
      type("v1");
      await vi.advanceTimersByTimeAsync(300);
      type("v2");
      await vi.advanceTimersByTimeAsync(300);
      expect(doc.draftFailed.value).toBe(true);
      failWrites = false;
      type("v3");
      await vi.advanceTimersByTimeAsync(300);
      expect(doc.draftFailed.value).toBe(false);
      expect(seen.filter((v) => v)).toHaveLength(1);
    });

    it("says true when there is nothing to keep (a clean note)", async () => {
      failWrites = true;
      expect(await doc.writeDraft()).toBe(true);
    });
  });
});
