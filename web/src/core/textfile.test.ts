import { describe, expect, it } from "vitest";
import {
  BinaryError,
  bomChar,
  decodeText,
  fromEditor,
  LineEndingsError,
  maxTextBytes,
  NotTextError,
  textFormat,
  toEditor,
  toLF,
  TooLargeError,
} from "./textfile";

const utf8 = (s: string) => new TextEncoder().encode(s);
const bom = [0xef, 0xbb, 0xbf];

/** The bytes a save of the unedited file sends: what the editor shows, encoded back. */
function roundTrip(bytes: Uint8Array): Uint8Array {
  const text = decodeText(bytes);
  const format = textFormat(text);
  return utf8(fromEditor(toEditor(text, format), format));
}

describe("decodeText: the open rule", () => {
  it("opens UTF-8 text of any kind", () => {
    expect(decodeText(utf8("apiVersion: v1\nkind: Pod\n"))).toBe("apiVersion: v1\nkind: Pod\n");
    expect(decodeText(utf8("#!/bin/sh\n\techo ünïcödé ✓\n"))).toBe("#!/bin/sh\n\techo ünïcödé ✓\n");
    expect(decodeText(new Uint8Array())).toBe("");
  });

  it("keeps a byte order mark as text, so a save writes it back", () => {
    expect(decodeText(new Uint8Array([...bom, 0x61]))).toBe(`${bomChar}a`);
  });

  it("refuses a file with a NUL byte", () => {
    expect(() => decodeText(new Uint8Array([0x61, 0x00, 0x62]))).toThrow(NotTextError);
    expect(() => decodeText(new Uint8Array([0x61, 0x00, 0x62]))).toThrow("it isn't UTF-8 text");
  });

  it("refuses bytes that aren't valid UTF-8", () => {
    // A PNG's signature, and Latin-1 "é".
    expect(() => decodeText(new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]))).toThrow("it isn't UTF-8 text");
    expect(() => decodeText(new Uint8Array([0x63, 0x61, 0x66, 0xe9]))).toThrow("it isn't UTF-8 text");
  });

  it("refuses a file over the note size limit, and opens one at it", () => {
    expect(() => decodeText(new Uint8Array(maxTextBytes + 1).fill(0x61))).toThrow("it's larger than 5 MiB");
    expect(decodeText(new Uint8Array(maxTextBytes).fill(0x61))).toHaveLength(maxTextBytes);
  });
});

describe("textFormat: the line endings and byte order mark a save keeps", () => {
  it("reads LF, CRLF and a byte order mark", () => {
    expect(textFormat("a\nb\n")).toEqual({ bom: false, eol: "\n" });
    expect(textFormat("a\r\nb\r\n")).toEqual({ bom: false, eol: "\r\n" });
    expect(textFormat(`${bomChar}a\r\n`)).toEqual({ bom: true, eol: "\r\n" });
    expect(textFormat("one line")).toEqual({ bom: false, eol: "\n" });
  });

  it("refuses mixed line endings and a lone CR: the editor would change them, so only the user may", () => {
    const mixed = () => textFormat("a\r\nb\nc");
    expect(mixed).toThrow(new LineEndingsError("mixed"));
    expect(mixed).toThrow("it mixes Windows (CRLF) and Unix (LF) line endings");
    expect(() => textFormat("a\rb")).toThrow(new LineEndingsError("cr"));
    expect(() => textFormat("a\r\r\nb")).toThrow("it has old Mac (CR) line endings");
  });

  it("toLF: what the user's 'convert' opens: LF lines, the byte order mark kept as it was", () => {
    expect(toLF(`${bomChar}a\r\nb\nc\rd\r\n`)).toEqual({ text: "a\nb\nc\nd\n", format: { bom: true, eol: "\n" } });
    expect(toLF("a\rb")).toEqual({ text: "a\nb", format: { bom: false, eol: "\n" } });
  });

  it("tells binary and too large apart from line endings: only line endings can be converted", () => {
    expect(() => decodeText(new Uint8Array([0]))).toThrow(BinaryError);
    expect(() => decodeText(new Uint8Array(maxTextBytes + 1))).toThrow(TooLargeError);
    expect(new LineEndingsError("cr")).toBeInstanceOf(NotTextError);
  });

  it("leaves a U+FEFF further on as text: it isn't a byte order mark", () => {
    expect(textFormat(`a${bomChar}b`)).toEqual({ bom: false, eol: "\n" });
    expect(toEditor(`a${bomChar}b`, { bom: false, eol: "\n" })).toBe(`a${bomChar}b`);
  });
});

describe("the editor's text", () => {
  it("has no byte order mark and LF lines; saving puts the file's back", () => {
    const f = { bom: true, eol: "\r\n" } as const;
    expect(toEditor(`${bomChar}a\r\nb\r\n`, f)).toBe("a\nb\n");
    expect(fromEditor("a\nb\nnew\n", f)).toBe(`${bomChar}a\r\nb\r\nnew\r\n`);
  });

  it.each([
    ["CRLF", utf8("apiVersion: v1\r\nkind: Pod\r\n")],
    ["a byte order mark", new Uint8Array([...bom, ...utf8("[core]\n\tbare = false\n")])],
    ["a byte order mark and CRLF", new Uint8Array([...bom, ...utf8("a\r\nb")])],
    ["no final newline", utf8("echo hi\n\techo there")],
    ["tabs and trailing spaces", utf8("\tkey: value  \n\n\n")],
    ["only a newline", utf8("\n")],
    ["nothing", new Uint8Array()],
  ])("a file with %s saves back byte for byte", (_, bytes) => {
    expect(roundTrip(bytes)).toEqual(bytes);
  });
});
