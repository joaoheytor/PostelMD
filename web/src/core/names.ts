/**
 * What a typed note name means. Only a trailing ".md" counts as the extension: a name like
 * "nginx.conf" or "v1.2" is a title with a dot in it, and still makes a note.
 */

/** The file a new note's name means: ".md" added unless it already ends in .md (any case). */
export function noteFile(name: string): string {
  return /\.md$/i.test(name) ? name : `${name}.md`;
}

/** The title a new note's name gives: the name without a trailing ".md". */
export function noteTitle(name: string): string {
  return name.replace(/\.md$/i, "");
}

/**
 * Why a typed note name can't be used, or undefined. A name, or a folder in it, that starts
 * with a dot is refused: most tools hide it, and the vault reserves some such names (.git, its
 * trash), which the server answers as not found.
 */
export function nameProblem(name: string): string | undefined {
  if (name.split("/").some((part) => part.startsWith("."))) {
    return `A note name can't start with a dot ("${name}"). Remove the dot, or put it later in the name.`;
  }
  return undefined;
}
