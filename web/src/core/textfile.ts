/**
 * Which files the editor opens, and how their text goes in and out of it unchanged. A file
 * opens only when a save of it, unedited, writes back the very bytes it was read from: UTF-8
 * text, no NUL byte, one kind of line ending, at most the note size limit.
 */

/** The note size limit (vault.MaxNoteSize): the server refuses to save more. */
export const maxTextBytes = 5 << 20;

/** The file can't be opened as text; the message says why, in words for the user. */
export class NotTextError extends Error {}

/** U+FEFF, what a UTF-8 byte order mark decodes to (spelt out: the character is invisible). */
export const bomChar = String.fromCharCode(0xfeff);

/**
 * The file's text, exactly: a byte order mark stays as its first character, so a save writes
 * it back. Throws NotTextError for anything the editor would change on save.
 */
export function decodeText(bytes: Uint8Array): string {
  if (bytes.length > maxTextBytes) throw new NotTextError("it's larger than 5 MiB, the most PostelMD edits");
  if (bytes.includes(0)) throw new NotTextError("it isn't UTF-8 text");
  try {
    return new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(bytes);
  } catch {
    throw new NotTextError("it isn't UTF-8 text");
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
 * The file's format; throws NotTextError when the editor's LF lines can't give the same text
 * back (mixed line endings, a lone CR): such a file never opens, so no save converts it.
 */
export function textFormat(text: string): TextFormat {
  const bom = text.startsWith(bomChar);
  const crlf = text.includes("\r\n");
  const lf = /(^|[^\r])\n/.test(text);
  if (/\r(?!\n)/.test(text)) throw new NotTextError("it has CR line endings, which the editor would change");
  if (crlf && lf) throw new NotTextError("it mixes CRLF and LF line endings, which the editor would change");
  return { bom, eol: crlf ? "\r\n" : "\n" };
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
