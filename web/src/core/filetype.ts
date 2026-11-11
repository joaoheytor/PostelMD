import { isNote } from "./tree";

/**
 * How the editor treats a file. "markdown" is a note: Markdown highlighting, list
 * continuation and the preview. The others only highlight their syntax; "plain" doesn't.
 */
export type Mode =
  | "markdown"
  | "yaml"
  | "json"
  | "shell"
  | "dockerfile"
  | "toml"
  | "properties"
  | "python"
  | "go"
  | "javascript"
  | "jsx"
  | "typescript"
  | "tsx"
  | "plain";

// Maps, not objects: a file named "constructor" must not find Object.prototype's.
const byExtension = new Map<string, Mode>([
  ["yaml", "yaml"],
  ["yml", "yaml"],
  ["json", "json"],
  ["sh", "shell"],
  ["bash", "shell"],
  ["zsh", "shell"],
  ["env", "shell"],
  ["dockerfile", "dockerfile"],
  ["toml", "toml"],
  ["ini", "properties"],
  ["properties", "properties"],
  ["cfg", "properties"],
  ["py", "python"],
  ["go", "go"],
  ["js", "javascript"],
  ["mjs", "javascript"],
  ["cjs", "javascript"],
  ["jsx", "jsx"],
  ["ts", "typescript"],
  ["mts", "typescript"],
  ["cts", "typescript"],
  ["tsx", "tsx"],
]);

const byName = new Map<string, Mode>([
  ["dockerfile", "dockerfile"],
  ["containerfile", "dockerfile"],
  [".env", "shell"],
]);

/** The mode of the file at path, by its name. */
export function modeOf(path: string): Mode {
  const name = path.slice(path.lastIndexOf("/") + 1);
  if (isNote(name)) return "markdown";
  const lower = name.toLowerCase();
  const dot = lower.lastIndexOf(".");
  return byName.get(lower) ?? (dot > 0 ? byExtension.get(lower.slice(dot + 1)) : undefined) ?? "plain";
}
