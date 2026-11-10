import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { SaveResult } from "./api";
import { DocumentSession } from "./document";

/** A fake server: each save waits until the test answers it. */
class FakeServer {
  calls: { content: string; base: string; answer: (r: SaveResult) => void }[] = [];
  save = (_path: string, content: string, base: string): Promise<SaveResult> =>
    new Promise((answer) => this.calls.push({ content, base, answer }));
  last() {
    const c = this.calls.at(-1);
    if (!c) throw new Error("no save was sent");
    return c;
  }
}

const tick = () => vi.advanceTimersByTimeAsync(0);

describe("DocumentSession", () => {
  let server: FakeServer;
  let text: string;
  let doc: DocumentSession;

  beforeEach(() => {
    vi.useFakeTimers();
    server = new FakeServer();
    text = "v0";
    doc = new DocumentSession("a.md", '"e0"', { save: server.save, read: () => text, debounceMs: 1000 });
  });
  afterEach(() => {
    doc.dispose();
    vi.useRealTimers();
  });

  const type = (t: string) => {
    text = t;
    doc.edit();
  };

  it("debounces: one save after the last keystroke, with If-Match", async () => {
    type("v1");
    await vi.advanceTimersByTimeAsync(500);
    type("v2");
    await vi.advanceTimersByTimeAsync(999);
    expect(server.calls).toHaveLength(0);
    expect(doc.state.value.status).toBe("dirty");
    await vi.advanceTimersByTimeAsync(1);
    expect(server.calls).toHaveLength(1);
    expect(server.last()).toMatchObject({ content: "v2", base: '"e0"' });
    expect(doc.state.value.status).toBe("saving");
    server.last().answer({ kind: "saved", etag: '"e1"' });
    await tick();
    expect(doc.state.value.status).toBe("clean");
    expect(doc.dirty).toBe(false);
  });

  it("never lets an older save's response clear a newer edit (ADR-0003 §7)", async () => {
    type("v1");
    await vi.advanceTimersByTimeAsync(1000);
    type("v2"); // typed while v1 is in flight
    expect(server.calls).toHaveLength(1); // one request in flight per document
    server.last().answer({ kind: "saved", etag: '"e1"' });
    await tick();
    expect(doc.dirty).toBe(true);
    expect(doc.state.value.status).toBe("dirty");
    await vi.advanceTimersByTimeAsync(1000);
    expect(server.calls).toHaveLength(2);
    expect(server.last()).toMatchObject({ content: "v2", base: '"e1"' }); // the new base
    server.last().answer({ kind: "saved", etag: '"e2"' });
    await tick();
    expect(doc.state.value.status).toBe("clean");
  });

  it("flush saves at once, and again for edits made during the save", async () => {
    type("v1");
    const flushed = doc.flush();
    await tick();
    expect(server.calls).toHaveLength(1);
    type("v2");
    server.last().answer({ kind: "saved", etag: '"e1"' });
    await tick();
    expect(server.calls).toHaveLength(2);
    server.last().answer({ kind: "saved", etag: '"e2"' });
    await flushed;
    expect(doc.dirty).toBe(false);
  });

  it("a conflict stops autosave until the user picks a side", async () => {
    type("mine");
    await vi.advanceTimersByTimeAsync(1000);
    server.last().answer({ kind: "conflict", etag: '"t1"', content: "theirs" });
    await tick();
    expect(doc.state.value).toMatchObject({ status: "conflict", conflict: { content: "theirs" } });
    type("mine, more");
    await vi.advanceTimersByTimeAsync(5000);
    expect(server.calls).toHaveLength(1);

    doc.keepMine();
    await tick();
    expect(server.calls).toHaveLength(2);
    expect(server.last()).toMatchObject({ content: "mine, more", base: '"t1"' });
    server.last().answer({ kind: "saved", etag: '"e2"' });
    await tick();
    expect(doc.state.value.status).toBe("clean");
  });

  it("taking theirs replaces the buffer and leaves the note clean", async () => {
    type("mine");
    await vi.advanceTimersByTimeAsync(1000);
    server.last().answer({ kind: "conflict", etag: '"t1"', content: "theirs" });
    await tick();
    expect(doc.takeTheirs()).toBe("theirs");
    text = "theirs";
    expect(doc.state.value.status).toBe("clean");
    type("theirs, edited");
    await vi.advanceTimersByTimeAsync(1000);
    expect(server.last()).toMatchObject({ base: '"t1"' });
  });

  it("a deleted note is never recreated (ADR-0003 §7)", async () => {
    type("v1");
    await vi.advanceTimersByTimeAsync(1000);
    server.last().answer({ kind: "gone" });
    await tick();
    expect(doc.state.value.status).toBe("gone");
    type("v2");
    await doc.flush();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(server.calls).toHaveLength(1);
    expect(doc.dirty).toBe(true);
  });

  it("retries a busy or failed save with backoff, honouring Retry-After", async () => {
    type("v1");
    await vi.advanceTimersByTimeAsync(1000);
    server.last().answer({ kind: "error", message: "busy, retry", retryAfterMs: 3000, retry: true });
    await tick();
    expect(doc.state.value).toMatchObject({ status: "error", message: "busy, retry" });
    await vi.advanceTimersByTimeAsync(2999);
    expect(server.calls).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(server.calls).toHaveLength(2);
    server.last().answer({ kind: "error", message: "offline", retryAfterMs: 0, retry: true });
    await tick();
    await vi.advanceTimersByTimeAsync(1999); // the second failure waits 2 s
    expect(server.calls).toHaveLength(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(server.calls).toHaveLength(3);
    server.last().answer({ kind: "saved", etag: '"e1"' });
    await tick();
    expect(doc.state.value.status).toBe("clean");
  });

  it("doesn't retry an error that won't fix itself, until the next edit", async () => {
    type("huge");
    await vi.advanceTimersByTimeAsync(1000);
    server.last().answer({ kind: "error", message: "note too large", retryAfterMs: 0, retry: false });
    await tick();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(server.calls).toHaveLength(1);
    type("smaller");
    await vi.advanceTimersByTimeAsync(1000);
    expect(server.calls).toHaveLength(2);
  });

  it("dispose cancels pending timers", async () => {
    type("v1");
    doc.dispose();
    await vi.advanceTimersByTimeAsync(5000);
    expect(server.calls).toHaveLength(0);
  });
});
