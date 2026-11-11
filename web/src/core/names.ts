import { isNote } from "./tree";

/**
 * What a typed note name means. Only a trailing ".md" counts as the extension: a name like
 * "nginx.conf" or "v1.2" is a title with a dot in it, and still makes a note.
 */

/** The file a new note's name means: ".md" added unless it already ends in .md (any case). */
export function noteFile(name: string): string {
  return /\.md$/i.test(name) ? name : `${name}.md`;
}

/** The title a new note's name gives: its last part, without a trailing ".md". */
export function noteTitle(name: string): string {
  return name.slice(name.lastIndexOf("/") + 1).replace(/\.md$/i, "");
}

/**
 * Why a note can't go at path, or undefined. The one rule for New note, Rename or move and
 * Restore: a note's own name (the last part) can't start with a dot, since most tools hide
 * it. Its folders may (".github/pr-checklist.md"), and folders and other files aren't
 * checked: the server refuses only its reserved names (.git, its trash), as not found.
 */
export function nameProblem(path: string): string | undefined {
  const name = path.slice(path.lastIndexOf("/") + 1);
  if (!isNote(name) || !name.startsWith(".")) return undefined;
  return `A note name can't start with a dot ("${name}"). Remove the dot, or put it later in the name.`;
}
