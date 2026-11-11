/**
 * Which files the editor opens, and how their text goes in and out of it unchanged. A file
 * opens as it is only when a save of it, unedited, writes back the very bytes it was read
 * from: UTF-8 text, no NUL byte, one kind of line ending, at most the note size limit. A file
 * whose line endings the editor can't keep opens only once the user asks to convert them.
 */

/** The note size limit (vault.MaxNoteSize): the server refuses to save more. */
export const maxTextBytes = 5 << 20;

/** The file can't be opened as it is; the message says why, in words for the user. */
export class NotTextError extends Error {}

/** Bytes that aren't UTF-8 text (a NUL byte, an invalid sequence): nothing can open them. */
export class BinaryError extends NotTextError {
  constructor() {
    super("it isn't UTF-8 text");
  }
}

/** Over the note size limit: the server would refuse the save. */
export class TooLargeError extends NotTextError {
  constructor() {
    super("it's larger than 5 MiB, the most PostelMD edits");
  }
}

/** Text whose line endings the editor can't keep: only the user may convert them (toLF). */
export class LineEndingsError extends NotTextError {
  constructor(readonly kind: "mixed" | "cr") {
    super(kind === "mixed" ? "it mixes Windows (CRLF) and Unix (LF) line endings" : "it has old Mac (CR) line endings");
  }
}

/** U+FEFF, what a UTF-8 byte order mark decodes to (spelt out: the character is invisible). */
export const bomChar = String.fromCharCode(0xfeff);

/**
 * The file's text, exactly: a byte order mark stays as its first character, so a save writes
 * it back. Throws BinaryError or TooLargeError.
 */
export function decodeText(bytes: Uint8Array): string {
  if (bytes.length > maxTextBytes) throw new TooLargeError();
  if (bytes.includes(0)) throw new BinaryError();
  try {
    return new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(bytes);
  } catch {
    throw new BinaryError();
  }
}

/** What a save keeps of the file that the editor doesn't show. */
export interface TextFormat {
  /** The file starts with a UTF-8 byte order mark. */
  bom: boolean;
  /** Its line ending: the editor's lines always end in LF. */
  eol: "\n" | "\r\n";
}

/**
 * The file's format; throws LineEndingsError when the editor's LF lines can't give the same
 * text back (mixed line endings, a lone CR): such a file never opens as it is, so no save
 * converts it unasked.
 */
export function textFormat(text: string): TextFormat {
  const bom = text.startsWith(bomChar);
  const crlf = text.includes("\r\n");
  const lf = /(^|[^\r])\n/.test(text);
  if (/\r(?!\n)/.test(text)) throw new LineEndingsError("cr");
  if (crlf && lf) throw new LineEndingsError("mixed");
  return { bom, eol: crlf ? "\r\n" : "\n" };
}

/**
 * What a file opens as when the user converts its line endings: the editor's text, every
 * CRLF and CR made LF, and a format that saves LF. A byte order mark is no line ending: it
 * stays as it was.
 */
export function toLF(text: string): { text: string; format: TextFormat } {
  const bom = text.startsWith(bomChar);
  return { text: (bom ? text.slice(1) : text).replace(/\r\n?/g, "\n"), format: { bom, eol: "\n" } };
}

/** The text the editor shows: no byte order mark, LF lines. */
export function toEditor(text: string, format: TextFormat): string {
  const body = format.bom && text.startsWith(bomChar) ? text.slice(1) : text;
  return format.eol === "\r\n" ? body.replaceAll("\r\n", "\n") : body;
}

/** The editor's text for any file text (a draft's): its byte order mark dropped, CRLF made LF. */
export function normalize(text: string): string {
  return toEditor(text, { bom: true, eol: "\r\n" });
}

/** The file's text for the editor's: its byte order mark and line ending put back. */
export function fromEditor(text: string, format: TextFormat): string {
  const body = format.eol === "\r\n" ? text.replaceAll("\n", "\r\n") : text;
  return format.bom ? bomChar + body : body;
}
