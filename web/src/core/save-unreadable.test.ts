import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { saveNote } from "./api";
import { DocumentSession } from "./document";
import type { NoteDrafts } from "./drafts";

/**
 * Something outside PostelMD grew the open file past the server's read limit, and the next
 * save gets a 412 without the current note or an ETag (ADR-0029, #157). That is a conflict
 * whose other version can't be opened, like a binary one: never retried, the edits kept as a
 * draft, and the only way out is the user's text as a new file.
 */
describe("a save over a file the server can't read", () => {
  let puts: number;
  let draft: { saved: number[]; dropped: number; writes: string[] };
  let text: string;
  let doc: DocumentSession;

  beforeEach(() => {
    vi.useFakeTimers();
    puts = 0;
    vi.stubGlobal("fetch", (() => {
      puts++;
      const body = JSON.stringify({ code: "too-large-to-read", error: "the file on disk is larger than 64 MiB" });
      return Promise.resolve(new Response(body, { status: 412, headers: { "Content-Type": "application/json" } }));
    }) as typeof fetch);
    draft = { saved: [], dropped: 0, writes: [] };
    const hooks: NoteDrafts = {
      write: (t) => {
        draft.writes.push(t);
        return Promise.resolve();
      },
      saved: (gen) => void draft.saved.push(gen),
      drop: () => void draft.dropped++,
      close: () => {},
    };
    text = "v0";
    doc = new DocumentSession("a.md", '"e0"', { save: saveNote, read: () => text, debounceMs: 1000, draftMs: 300, draft: hooks });
  });
  afterEach(() => {
    doc.dispose();
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("is a conflict with nothing to take or compare, not the user's note being too large", async () => {
    // Not its JSON body as the other version's text: "Use the other version" would put it in the editor.
    expect(await saveNote("a.md", "x", '"e0"')).toEqual({ kind: "conflict", etag: "", binary: true });
  });

  it("is never retried, and the edits stay unsaved and in the draft", async () => {
    text = "v1";
    doc.edit();
    await vi.advanceTimersByTimeAsync(1000);
    expect(puts).toBe(1);
    expect(doc.state.value).toEqual({ status: "conflict", conflict: { etag: "", binary: true } });
    await vi.advanceTimersByTimeAsync(120_000);
    expect(puts).toBe(1);
    expect(doc.dirty).toBe(true);
    expect(draft.writes).toEqual(["v1"]);
    expect(draft).toMatchObject({ saved: [], dropped: 0 });
  });
});
