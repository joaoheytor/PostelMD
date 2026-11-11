/** The workspace's failures in words, and the names it picks for a recovered copy. */
import { ApiError } from "./api";
import { NotTextError } from "./textfile";
import { parent } from "./tree";

export function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

/** Why path didn't open: a file the editor can't hold says so, as a reason, not a failure. */
export function openError(path: string, err: unknown): string {
  if (err instanceof NotTextError) return `PostelMD can't open ${path}: ${err.message}.`;
  return `Couldn't open ${path}: ${message(err)}`;
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
