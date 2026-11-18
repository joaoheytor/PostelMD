import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readNote } from "./api";
import { abortableWait } from "./read-wait";

const busy = () => new Response("{}", { status: 503, headers: { "Retry-After": "5" } });

/** Settles into a box the test reads without awaiting: a read that never ends stays undefined. */
function track(p: Promise<unknown>): { value?: unknown } {
  const box: { value?: unknown } = {};
  p.then(
    (v) => (box.value = v),
    (e: unknown) => (box.value = e),
  );
  return box;
}

describe("a read's abort (CLAUDE.md rule 8)", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("stops a GET in flight: an AbortError, one request, no timer left", async () => {
    let calls = 0;
    vi.stubGlobal("fetch", ((_url: string, init?: RequestInit) => {
      calls++;
      return new Promise<Response>((_, reject) => {
        const signal = init?.signal;
        signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
      });
    }) as typeof fetch);
    const reading = new AbortController();
    const read = track(readNote("a.md", reading.signal));
    await vi.advanceTimersByTimeAsync(0);
    reading.abort();
    await vi.advanceTimersByTimeAsync(0);
    expect(read.value).toMatchObject({ name: "AbortError" });
    expect(calls).toBe(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("an abort after the 503 arrives, before its wait starts, still stops the read", async () => {
    let calls = 0;
    const reading = new AbortController();
    vi.stubGlobal("fetch", (() => {
      calls++;
      reading.abort(); // lands after the request, before readWaiting waits
      return Promise.resolve(busy());
    }) as typeof fetch);
    const read = track(readNote("a.md", reading.signal));
    await vi.advanceTimersByTimeAsync(30_000);
    expect(read.value).toMatchObject({ name: "AbortError" });
    expect(calls).toBe(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("a wait that ends removes its abort listener", async () => {
    const reading = new AbortController();
    const removed = vi.spyOn(reading.signal, "removeEventListener");
    const done = track(abortableWait(1_000, reading.signal));
    await vi.advanceTimersByTimeAsync(1_000);
    expect(done).toEqual({ value: undefined });
    expect("value" in done).toBe(true);
    expect(removed).toHaveBeenCalledWith("abort", expect.any(Function));
  });
});
