import type { DocumentSession } from "./document";
import { fromEditor, type TextFormat, textFormat, toEditor } from "./textfile";

/** The file open in the editor, with the text it was loaded with. */
export interface OpenNote {
  doc: DocumentSession;
  /** The file's text as read, a byte order mark and CRLF included. */
  initial: string;
  /** The text the editor starts with: no byte order mark, LF lines (textfile.ts). */
  shown: string;
  /** What a save puts back of the file: the other version's, once the user takes it. */
  format: TextFormat;
}

/** The open file for doc, read as text in format. */
export function openFile(doc: DocumentSession, text: string, format: TextFormat): OpenNote {
  return { doc, initial: text, shown: toEditor(text, format), format };
}

/** The file's text for the editor's text, as a save writes it. */
export function fileText(open: OpenNote, editorText: string): string {
  return fromEditor(editorText, open.format);
}

/**
 * Resolves the conflict with the other version: returns the editor's text for it, and saves
 * keep its format from now on. Throws NotTextError, leaving the conflict as it is, when the
 * editor couldn't save that version back unchanged.
 */
export function takeTheirs(open: OpenNote, theirs: string): string {
  const format = textFormat(theirs);
  open.format = format;
  return toEditor(open.doc.takeTheirs(), format);
}
