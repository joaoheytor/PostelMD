/** The workspace's failures in words, and the names it picks for a recovered copy. */
import { ApiError } from "./api";
import { BinaryError, LineEndingsError, NotTextError } from "./textfile";
import { parent } from "./tree";

export function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

/**
 * Why path didn't open, as the workspace shows it. A file the editor can't hold says so, as a
 * reason, not a failure; one refused for its line endings offers to convert them (convert).
 */
export function openFailure(path: string, err: unknown): { error: string; convert?: string } {
  if (err instanceof LineEndingsError) {
    const why = `${err.message}. Convert them to LF to edit it here. This saves the file with LF line endings.`;
    return { error: `PostelMD can't open ${path} as it is: ${why}`, convert: path };
  }
  if (err instanceof BinaryError) return { error: `PostelMD opens only UTF-8 text files, and ${path} isn't one.` };
  if (err instanceof NotTextError) return { error: `PostelMD can't open ${path}: ${err.message}.` };
  return { error: `Couldn't open ${path}: ${message(err)}` };
}

/** Why a move failed, in words: the server's 404 doesn't say which side is missing. */
export function moveError(err: unknown, from: string, to: string): string {
  if (!(err instanceof ApiError)) return message(err);
  if (err.status === 409) return `${to} already exists`;
  if (err.status === 404) return `${from} or the folder ${parent(to) || "it goes into"} doesn't exist any more`;
  return message(err);
}

/** Where a copy of path's text goes ("x.md" → "x-recovered.md"). */
export function recoveredPath(path: string): string {
  return path.replace(/(\.[a-z]+)?$/i, (ext) => `-recovered${ext || ".md"}`);
}
