import { afterEach, describe, expect, it, vi } from "vitest";
import { readNote, saveNote } from "./api";
import { bomChar, NotTextError } from "./textfile";

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

  it("a conflict's other version keeps its byte order mark: a save that landed is recognised by its bytes", async () => {
    vi.stubGlobal("fetch", answer(412, withBom));
    expect(await saveNote("a.ini", "x", '"e0"')).toEqual({ kind: "conflict", etag: '"e9"', content: `${bomChar}a\r\n` });
  });

  it("a conflict whose other version is binary fails the save without offering to take it", async () => {
    vi.stubGlobal("fetch", answer(412, png));
    expect(await saveNote("a.ini", "x", '"e0"')).toEqual({
      kind: "error",
      message: "it changed outside this editor, and it isn't UTF-8 text",
      retryAfterMs: 0,
      retry: false,
    });
  });
});
