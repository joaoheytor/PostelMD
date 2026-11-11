import type { Note } from "./api";
import { type DocOptions, DocumentSession } from "./document";
import type { NoteDrafts } from "./drafts";
import { fromEditor, type TextFormat, textFormat, toEditor, toLF } from "./textfile";

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

export interface SessionDeps {
  /** The editor's text; undefined with no editor, and then nothing is saved or drafted. */
  reader: () => string | undefined;
  save: DocOptions["save"];
  draft?: NoteDrafts | undefined;
}

/**
 * Starts the session of the file read as note. As it is, the file must have a format a save
 * keeps: textFormat throws LineEndingsError otherwise. Converted (only at the user's click),
 * it opens with LF lines as an unsaved edit, which saves through the normal path with
 * If-Match note.etag: a file changed since gets the conflict view.
 */
export function openSession(path: string, note: Note, convert: boolean, deps: SessionDeps): OpenNote {
  const { text: shown, format } = convert ? toLF(note.content) : asIs(note.content);
  // Saves and drafts hold the file's text, its byte order mark and line endings put back. The
  // format is read through the holder, which a taken other version updates (takeTheirs).
  const holder: { open?: OpenNote } = {};
  const read = () => {
    const text = deps.reader();
    return text === undefined || !holder.open ? undefined : fromEditor(text, holder.open.format);
  };
  const doc = new DocumentSession(path, note.etag, { save: deps.save, read, ...(deps.draft && { draft: deps.draft }) });
  holder.open = { doc, initial: note.content, shown, format };
  if (convert) doc.edit(); // the conversion is the user's edit: saved and drafted like one
  return holder.open;
}

function asIs(text: string): { text: string; format: TextFormat } {
  const format = textFormat(text);
  return { text: toEditor(text, format), format };
}

/** The file's text for the editor's text, as a save writes it. */
export function fileText(open: OpenNote, editorText: string): string {
  return fromEditor(editorText, open.format);
}

/**
 * Resolves the conflict with the other version: returns the editor's text for it, and saves
 * keep its format from now on. As it is, throws LineEndingsError (the conflict stays) when the
 * editor couldn't save that version back unchanged; converted, it is made LF, and the caller
 * records that as an edit, saved over the other version's ETag.
 */
export function takeTheirs(open: OpenNote, theirs: string, convert: boolean): string {
  const { text, format } = convert ? toLF(theirs) : asIs(theirs);
  // Mutated, not replaced: a new OpenNote would remount the editor and lose its undo history,
  // which brings the user's text back (Ctrl+Z). Nothing renders from format; only saves and
  // drafts read it, when they start.
  open.format = format;
  open.doc.takeTheirs();
  return text;
}
