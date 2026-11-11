import { EditorState } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import { decodeText, fromEditor, textFormat, toEditor } from "../core/textfile";

const utf8 = (s: string) => new TextEncoder().encode(s);

/** Opens bytes in CodeMirror's document, as the editor does, and returns what a save would send. */
function throughCodeMirror(bytes: Uint8Array): Uint8Array {
  const text = decodeText(bytes);
  const format = textFormat(text);
  const doc = EditorState.create({ doc: toEditor(text, format) }).doc.toString();
  return utf8(fromEditor(doc, format));
}

describe("an unedited file through CodeMirror saves back byte for byte", () => {
  it.each([
    ["CRLF", utf8("apiVersion: v1\r\nkind: Pod\r\n")],
    ["a byte order mark", new Uint8Array([0xef, 0xbb, 0xbf, ...utf8("# Note\n\nbody\n")])],
    ["no final newline", utf8("#!/bin/sh\necho hi")],
    ["tabs", utf8("all:\n\tgo build ./...\n")],
  ])("%s", (_, bytes) => {
    expect(throughCodeMirror(bytes)).toEqual(bytes);
  });

  it("CodeMirror itself turns CRLF into LF: the reason textfile.ts puts it back", () => {
    expect(EditorState.create({ doc: "a\r\nb" }).doc.toString()).toBe("a\nb");
  });
});
