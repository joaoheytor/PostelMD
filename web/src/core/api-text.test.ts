import { afterEach, describe, expect, it, vi } from "vitest";
import { createNote, type OtherVersion, readNote, saveNote } from "./api";
import { bomChar, NotTextError, TooLargeError } from "./textfile";

const answer = (status: number, body: Uint8Array<ArrayBuffer>) =>
  (() => Promise.resolve(new Response(body, { status, headers: { ETag: '"e9"' } }))) as typeof fetch;

const withBom = new Uint8Array([0xef, 0xbb, 0xbf, ...new TextEncoder().encode("a\r\n")]);
const png = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00]);

describe("reading a file's text", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("keeps a byte order mark and CRLF, which resp.text() would drop or keep by chance", async () => {
    vi.stubGlobal("fetch", answer(200, withBom));
    expect(await readNote("a.ini")).toEqual({ content: `${bomChar}a\r\n`, etag: '"e9"' });
  });

  it("refuses a binary file", async () => {
    vi.stubGlobal("fetch", answer(200, png));
    await expect(readNote("logo.png")).rejects.toThrow(NotTextError);
  });

  // A path held by a rename rule (ADR-0031) answers 503 with Retry-After until it resolves,
  // within SecondPass (10 s): the read waits for it instead of failing the open.
  it("waits out a 503 with Retry-After, as the server says, then reads", async () => {
    vi.useFakeTimers();
    try {
      const busy = () => new Response('{"error":"busy, retry"}', { status: 503, headers: { "Retry-After": "2" } });
      const calls: number[] = [];
      vi.stubGlobal("fetch", (() => {
        calls.push(Date.now());
        return Promise.resolve(calls.length < 3 ? busy() : new Response("# A\n", { status: 200, headers: { ETag: '"e1"' } }));
      }) as typeof fetch);
      const read = readNote("a.md");
      await vi.advanceTimersByTimeAsync(4_000);
      expect(await read).toEqual({ content: "# A\n", etag: '"e1"' });
      const [first = 0] = calls;
      expect(calls.map((t) => t - first)).toEqual([0, 2_000, 4_000]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("gives up after 20 s of 503s, and a 503 without Retry-After at once", async () => {
    vi.useFakeTimers();
    try {
      let calls = 0;
      const busy = (retry: boolean) =>
        (() => (calls++, Promise.resolve(new Response("{}", { status: 503, headers: retry ? { "Retry-After": "5" } : {} })))) as typeof fetch;
      vi.stubGlobal("fetch", busy(true));
      const held = readNote("a.md").catch((e: unknown) => e);
      await vi.advanceTimersByTimeAsync(30_000);
      expect(await held).toMatchObject({ status: 503 });
      expect(calls).toBe(5); // at 0, 5, 10, 15 and 20 s
      calls = 0;
      vi.stubGlobal("fetch", busy(false));
      await expect(readNote("a.md")).rejects.toMatchObject({ status: 503 });
      expect(calls).toBe(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("stops waiting when its signal aborts: no more requests, no timer left (CLAUDE.md rule 8)", async () => {
    vi.useFakeTimers();
    try {
      let calls = 0;
      vi.stubGlobal("fetch", ((_url: string, init?: RequestInit) => {
        calls++;
        if (init?.signal?.aborted) return Promise.reject(new DOMException("aborted", "AbortError"));
        return Promise.resolve(new Response("{}", { status: 503, headers: { "Retry-After": "5" } }));
      }) as typeof fetch);
      const reading = new AbortController();
      const read = readNote("a.md", reading.signal).catch((e: unknown) => e);
      await vi.advanceTimersByTimeAsync(1_000);
      expect(vi.getTimerCount()).toBe(1); // waiting out the first 503
      reading.abort();
      expect(await read).toMatchObject({ name: "AbortError" });
      expect(vi.getTimerCount()).toBe(0);
      await vi.advanceTimersByTimeAsync(30_000);
      expect(calls).toBe(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("a file the server won't read for its size is too large, like one it sends (#144)", async () => {
    vi.stubGlobal("fetch", answer(413, new TextEncoder().encode('{"error":"too large"}')));
    await expect(readNote("huge.log")).rejects.toThrow(TooLargeError);
  });

  it("a conflict's other version keeps its byte order mark: a save that landed is recognised by its bytes", async () => {
    vi.stubGlobal("fetch", answer(412, withBom));
    expect(await saveNote("a.ini", "x", '"e0"')).toEqual({ kind: "conflict", etag: '"e9"', content: `${bomChar}a\r\n` });
  });

  it("a conflict whose other version is binary is a conflict with nothing to take or compare", async () => {
    vi.stubGlobal("fetch", answer(412, png));
    // No content at all: a "" would compare equal to an empty text.
    expect(await saveNote("a.ini", "x", '"e0"')).toEqual({ kind: "conflict", etag: '"e9"', binary: true });
  });

  it("a binary other version has no content to read: code that compares it doesn't compile (tsc)", () => {
    const other = (binary: boolean): OtherVersion => (binary ? { etag: '"e9"', binary: true } : { etag: '"e9"', content: "" });
    const c = other(true);
    // @ts-expect-error: content exists only once binary is ruled out (#139)
    expect(c.content).toBeUndefined();
    if (!c.binary) expect(c.content).toBe(""); // narrowed: compiles
  });
});

describe("the Content-Type a save sends", () => {
  afterEach(() => vi.unstubAllGlobals());

  /** The Content-Type of each request fetch was called with. */
  function sentTypes(): string[] {
    const types: string[] = [];
    vi.stubGlobal("fetch", ((_url: string, init?: RequestInit) => {
      types.push(new Headers(init?.headers).get("Content-Type") ?? "");
      return Promise.resolve(new Response(null, { status: 204, headers: { ETag: '"e1"' } }));
    }) as typeof fetch);
    return types;
  }

  it("is Markdown for a note and plain bytes for any other file", async () => {
    const types = sentTypes();
    await saveNote("a.md", "x", '"e0"');
    await saveNote("deploy.yaml", "x", '"e0"');
    await createNote("b.MD", "x");
    await createNote("run.sh", "x");
    // A note is what the editor treats as Markdown (modeOf): .markdown and .txt too.
    await saveNote("docs/guide.markdown", "x", '"e0"');
    await saveNote("todo.TXT", "x", '"e0"');
    expect(types).toEqual([
      "text/markdown; charset=utf-8",
      "application/octet-stream",
      "text/markdown; charset=utf-8",
      "application/octet-stream",
      "text/markdown; charset=utf-8",
      "text/markdown; charset=utf-8",
    ]);
  });
});
