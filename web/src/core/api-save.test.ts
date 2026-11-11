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

/** saveNote's result, or "still hanging" if it hasn't one within ms: a hang fails an assertion. */
async function within<T>(ms: number, p: Promise<T>): Promise<T | "still hanging"> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const late = new Promise<"still hanging">((r) => (timer = setTimeout(() => r("still hanging"), ms)));
  try {
    return await Promise.race([p, late]);
  } finally {
    clearTimeout(timer);
  }
}

const answer = (status: number, init: ResponseInit = {}, body: BodyInit | null = null) =>
  (() => Promise.resolve(new Response(body, { status, ...init }))) as typeof fetch;

describe("saveNote", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it("gives up on a save that doesn't answer, as an unknown outcome, never as saved or failed", async () => {
    vi.stubGlobal("fetch", hangingFetch());
    expect(await within(500, saveNote("a.md", "text", '"e0"', 20))).toEqual({ kind: "unknown" });
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
    expect(await within(500, saveNote("a.md", "text", '"e0"', 20))).toEqual({ kind: "unknown" });
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

  // What happened, and what happens next, apart: whether the edits are kept in this browser is
  // for the editor to say, which knows whether a draft holds them.
  it.each([
    ["a network error", (() => Promise.reject(new TypeError("Failed to fetch"))) as typeof fetch, "can't reach the server", "Retrying", true],
    ["a busy server", answer(503), "the server is busy", "Retrying", true],
    ["a server error", answer(500), "the server had an error", "Retrying", true],
    ["an expired session", answer(401), "your session expired", "Log in again", true],
    ["a note too large", answer(413), "the note is too large to save", undefined, false],
  ])("reports %s without claiming the edits are kept", async (_what, fetch, message, next, retry) => {
    vi.stubGlobal("fetch", fetch);
    const r = await saveNote("a.md", "text", '"e0"', 1000);
    expect(r).toMatchObject({ kind: "error", message, retry });
    expect(r.kind === "error" && r.next).toBe(next);
    expect(JSON.stringify(r)).not.toContain("kept");
  });
});
