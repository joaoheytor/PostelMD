import { afterEach, describe, expect, it, vi } from "vitest";
import { createNote, readNote, saveNote } from "./api";
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
    expect(await saveNote("a.ini", "x", '"e0"')).toEqual({ kind: "conflict", etag: '"e9"', content: "", binary: true });
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
