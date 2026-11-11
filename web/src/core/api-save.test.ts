import { afterEach, describe, expect, it, vi } from "vitest";
import { saveNote } from "./api";

/** A fetch that never answers, but gives up like the browser's when its signal aborts. */
function hangingFetch(): typeof fetch {
  return (_url, init) =>
    new Promise((_resolve, reject) => {
      const signal = init?.signal;
      signal?.addEventListener("abort", () => reject(signal.reason as Error), { once: true });
    });
}

describe("saveNote", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("gives up on a save that doesn't answer, as an unknown outcome, never as saved or failed", async () => {
    vi.stubGlobal("fetch", hangingFetch());
    const r = await saveNote("a.md", "text", '"e0"', 20);
    expect(r).toEqual({ kind: "unknown" });
  });

  it("gives up when the answer's body never arrives either (a 412 that stalls)", async () => {
    vi.stubGlobal("fetch", ((_url, init) => {
      const body = new ReadableStream({
        start(c) {
          init?.signal?.addEventListener("abort", () => c.error(init.signal?.reason), { once: true });
        },
      });
      return Promise.resolve(new Response(body, { status: 412, headers: { ETag: '"e9"' } }));
    }) as typeof fetch);
    expect(await saveNote("a.md", "text", '"e0"', 20)).toEqual({ kind: "unknown" });
  });

  it("still reports a network error as a retryable error", async () => {
    vi.stubGlobal("fetch", (() => Promise.reject(new TypeError("Failed to fetch"))) as typeof fetch);
    expect(await saveNote("a.md", "text", '"e0"', 1000)).toMatchObject({ kind: "error", retry: true });
  });

  it("times out after 15 s by default", async () => {
    let signal: AbortSignal | undefined;
    vi.stubGlobal("fetch", ((_url, init) => {
      signal = init?.signal ?? undefined;
      return Promise.resolve(new Response(null, { status: 204, headers: { ETag: '"e1"' } }));
    }) as typeof fetch);
    const timeout = vi.spyOn(AbortSignal, "timeout");
    expect(await saveNote("a.md", "text", '"e0"')).toEqual({ kind: "saved", etag: '"e1"' });
    expect(timeout).toHaveBeenCalledWith(15_000);
    expect(signal).toBeDefined();
  });
});
